// Package account 模块 4：账户信息模块。
//
// 需求要点：
//   - 展示 binance 与 gate 各自账户的资金、持仓；
//   - 聚合账户：总资金、购买力（min(合约账户×杠杆, 现货账户)）；
//   - 当前持有的对冲交易对、合约实时持仓；
//   - 程序运行时长。
//
// 调用方：前端刷新 / 自动交易模块 / 预警模块。
package account

import (
	"time"

	"deltacrypto/internal/exchange"
	"deltacrypto/internal/model"
	"deltacrypto/internal/service/settings"
	"deltacrypto/internal/service/trade"
)

// Service 账户信息模块服务
type Service struct {
	hub      *exchange.Hub
	settings *settings.Service
	trade    *trade.Service // 持仓数据来自交易模块落库的 hedge_position 表
	startAt  time.Time      // 程序启动时间（计算运行时长）
}

// New 创建账户信息模块
func New(hub *exchange.Hub, settings *settings.Service, tradeSvc *trade.Service) *Service {
	return &Service{hub: hub, settings: settings, trade: tradeSvc, startAt: time.Now()}
}

// Overview 聚合账户总览（前端账户页 & 自动交易买入判断的核心输入）
func (s *Service) Overview() (*model.AccountOverview, error) {
	// 各切片初始化为空数组，保证 JSON 返回 [] 而非 null
	out := &model.AccountOverview{
		RunningSince:  s.startAt,
		Balances:      make([]model.ExchangeBalance, 0),
		Hedges:        make([]model.HedgePosition, 0),
		SwapPositions: make([]model.SwapPositionInfo, 0),
	}

	// 现货腿交易所资金（gate）
	if spotEx, err := s.hub.Spot(); err == nil {
		free, used, total, err := spotEx.FetchUSDTBalance()
		if err == nil {
			out.Balances = append(out.Balances, model.ExchangeBalance{
				Exchange: spotEx.ID(), MarketType: "spot",
				USDTFree: free, USDTUsed: used, USDTTotal: total,
			})
		}
	}
	// 合约腿交易所资金（binance）
	if swapEx, err := s.hub.Swap(); err == nil {
		free, used, total, err := swapEx.FetchUSDTBalance()
		if err == nil {
			out.Balances = append(out.Balances, model.ExchangeBalance{
				Exchange: swapEx.ID(), MarketType: "swap",
				USDTFree: free, USDTUsed: used, USDTTotal: total,
			})
		}
		// 合约实时持仓（含强平价、未实现盈亏）
		if positions, err := swapEx.FetchSwapPositions(); err == nil {
			out.SwapPositions = positions
		}
	}

	// 聚合总资金
	for _, b := range out.Balances {
		out.TotalUSDT += b.USDTTotal
	}

	// 购买力 = min(合约账户×杠杆, 现货账户)（需求文档明确公式）
	leverage := s.settings.GetFloat(settings.KeyLeverage)
	if leverage <= 0 {
		leverage = 4
	}
	var spotTotal, swapFree float64
	for _, b := range out.Balances {
		if b.MarketType == "spot" {
			spotTotal = b.USDTTotal // 现货购买力用全部现货资金
		} else {
			swapFree = b.USDTFree // 合约端用可用保证金
		}
	}
	out.PurchasingPower = minFloat(spotTotal, swapFree*leverage)

	// 当前持有的对冲交易对（数据库）
	if hedges, err := s.trade.OpenPositions(); err == nil && hedges != nil {
		out.Hedges = hedges
	}
	return out, nil
}

// PurchasingPower 单独提供购买力查询（自动交易模块高频调用，避免拉全量）
func (s *Service) PurchasingPower() (float64, error) {
	spotEx, err := s.hub.Spot()
	if err != nil {
		return 0, err
	}
	swapEx, err := s.hub.Swap()
	if err != nil {
		return 0, err
	}
	_, _, spotTotal, err := spotEx.FetchUSDTBalance()
	if err != nil {
		return 0, err
	}
	swapFree, _, _, err := swapEx.FetchUSDTBalance()
	if err != nil {
		return 0, err
	}
	leverage := s.settings.GetFloat(settings.KeyLeverage)
	if leverage <= 0 {
		leverage = 4
	}
	return minFloat(spotTotal, swapFree*leverage), nil
}

// BalanceRatio 计算 现货资金 : 合约资金 的实际比例与目标偏差百分比（预警模块用）。
// 目标：合约:现货 = 1:N（N 来自设置 balance_ratio，默认 4，即现货是合约的 4 倍）。
// 返回偏差百分比 = |实际现货 - 目标现货| / 目标现货 * 100。
func (s *Service) BalanceRatio() (spotTotal, swapTotal, deviationPct float64, err error) {
	spotEx, err := s.hub.Spot()
	if err != nil {
		return 0, 0, 0, err
	}
	swapEx, err := s.hub.Swap()
	if err != nil {
		return 0, 0, 0, err
	}
	_, _, spotTotal, err = spotEx.FetchUSDTBalance()
	if err != nil {
		return 0, 0, 0, err
	}
	_, _, swapTotal, err = swapEx.FetchUSDTBalance()
	if err != nil {
		return 0, 0, 0, err
	}
	ratio := s.settings.GetFloat(settings.KeyBalanceRatio)
	if ratio <= 0 {
		ratio = 4
	}
	targetSpot := swapTotal * ratio // 目标现货资金
	if targetSpot <= 0 {
		return spotTotal, swapTotal, 0, nil
	}
	deviationPct = absFloat(spotTotal-targetSpot) / targetSpot * 100
	return spotTotal, swapTotal, deviationPct, nil
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func absFloat(a float64) float64 {
	if a < 0 {
		return -a
	}
	return a
}
