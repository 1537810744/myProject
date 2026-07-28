// 【阅读顺序 08】模块 2：行情模块（策略的眼睛）。
//
// 本文件职责：从两个交易所拉公共数据，按四重约束筛出“优质待买入列表”。
// 阅读目的：重点看 Candidates() 的六步过滤流水线——
//
//	① 拉币安全部 USDT 永续行情 → ② 流通量粗筛（最便宜的条件先过）
//	③ 与 Gate 现货取交集 → ④ 批量当前费率 → ⑤ 并发拉历史费率做趋势+均值过滤
//	⑥ 拉现货价算基差做最终过滤。
//
// 为什么这样排序：把“一次请求就能筛掉一大片”的条件放前面，把“一币一次请求”
// 的历史费率放后面且并发——这是“拿全量再筛 vs 调用时就筛”的取舍（需求原文提到可优化）。
// 注意：本模块全程使用【公开连接】，不配置 API 也能看行情（需求更新第 3 条）。
package market

import (
	"sort"
	"sync"
	"time"

	"deltacrypto/internal/exchange"
	"deltacrypto/internal/model"
	"deltacrypto/internal/service/settings"
)

// Service 行情模块服务
type Service struct {
	hub      *exchange.Hub
	settings *settings.Service
}

// New 创建行情模块
func New(hub *exchange.Hub, settings *settings.Service) *Service {
	return &Service{hub: hub, settings: settings}
}

// Candidates 获取通过全部约束的待买入列表（按当前资金费率降序）。
// 这是行情模块的核心入口，前端与自动交易模块都调它。
//
// 注意：这里使用【公开连接】（PublicSpot/PublicSwap）。
// 目的（需求更新第 3 条）：行情数据全是交易所公共接口，不需要 API 凭证，
// 即使还没配置任何账户 Key，用户也能打开行情页查看待买入列表。
func (s *Service) Candidates() ([]model.MarketCandidate, error) {
	spotEx, err := s.hub.PublicSpot()
	if err != nil {
		return nil, err
	}
	swapEx, err := s.hub.PublicSwap()
	if err != nil {
		return nil, err
	}

	// —— 从设置模块（数据库）读取全部阈值 ——
	n := int64(s.settings.GetInt(settings.KeyFundingCount))         // 最近 N 次费率
	minVolume := s.settings.GetFloat(settings.KeyMinQuoteVolume24h) // 流通量下限
	minAvg := s.settings.GetFloat(settings.KeyMinFundingAvgPct)     // 费率均值下限
	minBasis := s.settings.GetFloat(settings.KeyMinBasisPct)        // 基差下限

	// 第 1 步：拉合约端全部 USDT 永续 symbol 与行情（一次批量请求）
	swapSymbols, err := swapEx.SwapSymbols()
	if err != nil {
		return nil, err
	}
	swapCcxtSymbols := make([]string, len(swapSymbols))
	for i, sym := range swapSymbols {
		swapCcxtSymbols[i] = exchange.BaseToSwap(sym)
	}
	swapTickers, err := swapEx.FetchTickers(swapCcxtSymbols)
	if err != nil {
		return nil, err
	}

	// 第 2 步：流通量粗筛（最便宜的条件，先过滤掉绝大部分）
	volumeOK := make(map[string]float64) // 内部币对 -> 合约价
	for base, ccxtSym := range zipBase(swapSymbols, swapCcxtSymbols) {
		t, ok := swapTickers[ccxtSym]
		if !ok || t.Last <= 0 {
			continue
		}
		if t.QuoteVolume > minVolume {
			volumeOK[base] = t.Last
		}
	}

	// 第 3 步：与现货端取交集（只有两边都有的币才能对冲）
	spotSymbols, err := spotEx.SpotSymbols()
	if err != nil {
		return nil, err
	}
	spotSet := make(map[string]struct{}, len(spotSymbols))
	for _, sym := range spotSymbols {
		spotSet[sym] = struct{}{}
	}
	var both []string
	for base := range volumeOK {
		if _, ok := spotSet[base]; ok {
			both = append(both, base)
		}
	}
	if len(both) == 0 {
		return []model.MarketCandidate{}, nil
	}

	// 第 4 步：批量拉当前资金费率（一次请求，用于展示与排序）
	currentRates, _ := swapEx.FetchFundingRates(both) // 失败不致命，后面历史里也有

	// 第 5 步：并发拉历史费率，做“趋势上升 + 均值”过滤
	// （交集可能有上百个币，逐个请求较慢，用小并发加速；低频工具不需要大并发）
	type frResult struct {
		base  string
		rates []float64
		avg   float64
	}
	results := make(chan frResult, len(both))
	sem := make(chan struct{}, 8) // 并发度 8
	var wg sync.WaitGroup
	for _, base := range both {
		wg.Add(1)
		go func(base string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rates, err := swapEx.FetchFundingHistory(base, n)
			if err != nil || len(rates) == 0 {
				return
			}
			if !isRising(rates) { // 趋势约束
				return
			}
			avg := average(rates)
			if avg <= minAvg { // 均值约束
				return
			}
			results <- frResult{base: base, rates: rates, avg: avg}
		}(base)
	}
	wg.Wait()
	close(results)

	passed := make(map[string]frResult)
	var passedBases []string
	for r := range results {
		passed[r.base] = r
		passedBases = append(passedBases, r.base)
	}
	if len(passedBases) == 0 {
		return []model.MarketCandidate{}, nil
	}

	// 第 6 步：拉现货行情，计算基差，做最后的基差过滤
	spotTickers, err := spotEx.FetchTickers(passedBases)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]model.MarketCandidate, 0, len(passedBases))
	for _, base := range passedBases {
		spotTicker, ok := spotTickers[base]
		if !ok || spotTicker.Last <= 0 {
			continue
		}
		swapPrice := volumeOK[base]
		basisPct := (swapPrice - spotTicker.Last) / spotTicker.Last * 100 // 基差%
		if basisPct <= minBasis {                                         // 基差约束
			continue
		}
		fr := passed[base]
		currentRate := currentRates[base]
		out = append(out, model.MarketCandidate{
			Symbol:         base,
			SwapExchange:   swapEx.ID(),
			SpotExchange:   spotEx.ID(),
			SwapPrice:      swapPrice,
			SpotPrice:      spotTicker.Last,
			BasisPct:       basisPct,
			FundingRate:    currentRate,
			FundingAvgPct:  fr.avg,
			FundingRates:   fr.rates,
			AnnualizedPct:  currentRate * 3 * 365, // 8 小时结算一次，一天 3 次
			QuoteVolume24h: swapTickers[exchange.BaseToSwap(base)].QuoteVolume,
			Direction:      "空" + swapEx.ID() + "合约 / 多" + spotEx.ID() + "现货",
			UpdatedAt:      now,
		})
	}

	// 按当前资金费率降序（表现好的排前面，对应需求“在上升的里面选择表现好的”）
	sort.Slice(out, func(i, j int) bool { return out[i].FundingRate > out[j].FundingRate })
	return out, nil
}

// CurrentFundingRate 查询单个币对的当前资金费率（%）（卖出判断用，走公开连接）
func (s *Service) CurrentFundingRate(baseSymbol string) (float64, error) {
	swapEx, err := s.hub.PublicSwap()
	if err != nil {
		return 0, err
	}
	rates, err := swapEx.FetchFundingRates([]string{baseSymbol})
	if err != nil {
		return 0, err
	}
	return rates[baseSymbol], nil
}

// CurrentBasisPct 查询单个币对当前基差（%）（slow sell 判断用，走公开连接）
func (s *Service) CurrentBasisPct(baseSymbol string) (float64, error) {
	spotEx, err := s.hub.PublicSpot()
	if err != nil {
		return 0, err
	}
	swapEx, err := s.hub.PublicSwap()
	if err != nil {
		return 0, err
	}
	spotPrice, err := spotEx.FetchLastPrice("spot", baseSymbol)
	if err != nil {
		return 0, err
	}
	swapPrice, err := swapEx.FetchLastPrice("swap", baseSymbol)
	if err != nil {
		return 0, err
	}
	if spotPrice <= 0 {
		return 0, nil
	}
	return (swapPrice - spotPrice) / spotPrice * 100, nil
}

// zipBase 把内部币对列表与 ccxt 符号列表一一对应成 map
func zipBase(bases, ccxtSymbols []string) map[string]string {
	out := make(map[string]string, len(bases))
	for i, b := range bases {
		out[b] = ccxtSymbols[i]
	}
	return out
}

// isRising 判断费率趋势是否上升：前一半均值 < 后一半均值
// （对应需求“前 N < 后 N，在上升的里面选择资金费率表现好的”）
func isRising(rates []float64) bool {
	if len(rates) < 2 {
		return true // 数据太少无法判断，放行
	}
	mid := len(rates) / 2
	return average(rates[:mid]) < average(rates[mid:])
}

// average 求均值
func average(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}
