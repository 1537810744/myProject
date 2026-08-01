// 【阅读顺序 08】模块 2：行情模块（策略的眼睛）。
// 职责：从交易所拉公共数据，按四重约束筛出“优质待买入列表”。
// 本文件全程用【公开连接】（PublicSpot/PublicSwap）——行情是公共接口，没配 API 也能看行情。
// 语法点预览：goroutine、channel、WaitGroup、信号量、map 当集合、sort.Slice、切片切分、
// for range、if 带初始化、命名结构体（局部 type）。
package market

// import 导入用到的包。
import (
	"sort" // 排序：sort.Slice
	"sync" // 并发同步：sync.WaitGroup
	"time" // 时间

	"deltacrypto/internal/exchange"         // 交易所抽象层
	"deltacrypto/internal/model"            // 数据结构
	"deltacrypto/internal/service/settings" // 设置模块（读阈值）
)

// Service 行情模块服务。只依赖 hub（取连接）和 settings（读阈值）。
type Service struct {
	hub      *exchange.Hub     // 交易所连接管理器
	settings *settings.Service // 参数中心
}

// New 创建行情模块。
func New(hub *exchange.Hub, settings *settings.Service) *Service {
	return &Service{hub: hub, settings: settings}
}

// Candidates 获取通过全部约束的待买入列表（按当前资金费率降序）。
// 前端行情页和自动交易模块都调它——它是策略筛选的核心入口。
func (s *Service) Candidates() ([]model.MarketCandidate, error) {
	spotEx, err := s.hub.PublicSpot() // 公开连接：无需凭证（行情是公共数据）
	if err != nil {
		return nil, err
	}
	swapEx, err := s.hub.PublicSwap()
	if err != nil {
		return nil, err
	}

	// 阈值全部从设置模块读（设置页可改、即时生效），不写死。
	n := int64(s.settings.GetInt(settings.KeyFundingCount)) // 最近 N 次费率
	// 注意这里的 int64(...)：GetInt 返回的是 int，但下面 FetchFundingHistory 的参数
	// 要求 int64。类型不匹配编译器会直接报错，所以必须显式转换——Go 的类型转换
	// 写法就是“目标类型(值)”，比如 int64(x)、int(x)、float64(x)。
	minVolume := s.settings.GetFloat(settings.KeyMinQuoteVolume24h) // 流通量下限
	minAvg := s.settings.GetFloat(settings.KeyMinFundingAvgPct)     // 费率均值下限
	minBasis := s.settings.GetFloat(settings.KeyMinBasisPct)        // 基差下限

	// ---- 第 1 步：拉合约端全部 USDT 永续行情（一次批量请求，先拿到“候选全集”）----
	swapSymbols, err := swapEx.SwapSymbols() // 全部 USDT 永续币对
	if err != nil {
		return nil, err
	}
	swapCcxtSymbols := make([]string, len(swapSymbols)) // 分配同长度切片
	for i, sym := range swapSymbols {                   // 遍历
		swapCcxtSymbols[i] = exchange.BaseToSwap(sym) // 内部币对转合约符号，写回下标 i
	}
	swapTickers, err := swapEx.FetchTickers(swapCcxtSymbols) // 一次批量拉行情
	if err != nil {
		return nil, err
	}

	// ---- 第 2 步：流通量粗筛（最便宜的条件，先过滤掉绝大部分）----
	// 为什么这一步放最前？—— 它是【一次批量请求 + 本地过滤】，代价几乎为零，
	// 却能把几千个合约过滤到只剩几十个。过滤器排序原则：最便宜的先执行。
	volumeOK := make(map[string]float64)                               // 内部币对 -> 合约价（存过滤结果）
	for base, ccxtSym := range zipBase(swapSymbols, swapCcxtSymbols) { // 遍历合并后的 map
		t, ok := swapTickers[ccxtSym] // map 取值带 ok：行情里有没有这个币
		if !ok || t.Last <= 0 {       // 没有 或 价格异常
			continue // 跳过（continue = 进入下一轮循环）
		}
		if t.QuoteVolume > minVolume { // 24H 成交额达标
			volumeOK[base] = t.Last // 记下：这个币可用，并保存它的合约价
		}
	}

	// ---- 第 3 步：与现货端取交集（只有两边都有的币才能对冲）----
	spotSymbols, err := spotEx.SpotSymbols() // 全部 USDT 现货币对
	if err != nil {
		return nil, err
	}
	// 把现货列表放进 map 做快速查找。map[string]struct{} 是 Go 里“集合”的惯用法：
	// struct{} 占 0 字节（空结构体），比 map[string]bool 省内存，语义就是“只看在不在”。
	spotSet := make(map[string]struct{}, len(spotSymbols))
	for _, sym := range spotSymbols {
		spotSet[sym] = struct{}{} // struct{}{} 是空结构体字面量：不占内存的“标记”
	}
	var both []string            // 两边都有的币
	for base := range volumeOK { // 遍历 map（只有键，没有值——range 一个值时就是键）
		if _, ok := spotSet[base]; ok { // 现货集合里有没有这个币
			both = append(both, base) // 有：加入交集
		}
	}
	if len(both) == 0 {
		return []model.MarketCandidate{}, nil // 空结果也返回 []，前端不报错
	}

	// ---- 第 4 步：批量拉当前费率（一次请求，用于展示与排序）----
	// 忽略错误（_）：当前费率只是“展示/排序用”，拉失败后面第 5 步的历史费率也够用。
	currentRates, _ := swapEx.FetchFundingRates(both)

	// ---- 第 5 步：并发拉历史费率，做“趋势上升 + 均值”过滤 ----
	// 交集可能有上百个币，每个要单独拉一次历史费率（网络往返）。串行要等 N 次往返，
	// 并发拉总耗时 ≈ 1 次请求。三个 Go 并发原语：
	//   goroutine：go func() 启动的轻量线程
	//   channel：goroutine 之间传数据的管道
	//   WaitGroup：等一组 goroutine 全部干完
	type frResult struct { // 【局部 type】在函数内定义一个临时结构体，装并发结果
		base  string    // 币对
		rates []float64 // 历史费率
		avg   float64   // 均值
	}
	results := make(chan frResult, len(both)) // 有缓冲 channel：容量正好装下所有可能结果
	sem := make(chan struct{}, 8)             // 信号量：只允许 8 个请求同时在飞
	// 为什么限流？—— 上百个 goroutine 同时打交易所 = 请求风暴，会被封禁。
	// “sem <- struct{}{}” 塞得进去说明没到 8 个，塞不进去就阻塞等空位。
	var wg sync.WaitGroup // 等待组
	for _, base := range both {
		wg.Add(1) // 启动一个 goroutine 前计数 +1
		// ⚠️ 把 base【按值传参】进 goroutine（go func(base string){...}(base)）。
		// 直接引用外面的 base 是经典闭包坑：Go 1.21 及以前循环变量每轮复用同一地址，
		// 所有 goroutine 读到同一个“最后一轮的值”。传参 = 每人拿自己的副本。
		go func(base string) {
			defer wg.Done()          // goroutine 结束前计数 -1（defer 保证一定执行）
			sem <- struct{}{}        // 占一个信号量位置（阻塞直到有空位）
			defer func() { <-sem }() // 干完把位置让出来（defer：函数结束时执行）

			rates, err := swapEx.FetchFundingHistory(base, n) // 拉这个币的历史费率
			if err != nil || len(rates) == 0 {                // 失败或没数据
				return // 直接返回（该币放弃，不阻塞整体）
			}
			if !isRising(rates) { // 趋势约束：费率要处于上升期
				return
			}
			avg := average(rates) // 算均值
			if avg <= minAvg {    // 均值约束：N 次平均要高于下限
				return
			}
			results <- frResult{base: base, rates: rates, avg: avg} // 通过 channel 送回结果
		}(base) // ← 立即调用这个匿名函数（传参 base）
	}
	wg.Wait()      // 阻塞直到所有 goroutine 都 Done
	close(results) // 关 channel：下面 range 读到空为止就正常结束（不 close 会永远等）
	// 为什么必须 close？—— range 一个未关闭的 channel 会一直等新数据，永远不结束。

	passed := make(map[string]frResult) // 收集通过过滤的结果
	var passedBases []string
	for r := range results { // 从 channel 收结果（channel 关闭后读完就退出）
		passed[r.base] = r                        // 存结果
		passedBases = append(passedBases, r.base) // 记币对
	}
	if len(passedBases) == 0 {
		return []model.MarketCandidate{}, nil
	}

	// ---- 第 6 步：拉现货行情，算基差，做最后的基差过滤 ----
	spotTickers, err := spotEx.FetchTickers(passedBases) // 批量拉现货行情
	if err != nil {
		return nil, err
	}
	now := time.Now() // 取一次当前时间，所有结果共用（避免每条都取，浪费且不一致）
	out := make([]model.MarketCandidate, 0, len(passedBases))
	for _, base := range passedBases {
		spotTicker, ok := spotTickers[base] // 现货行情里有没有这个币
		if !ok || spotTicker.Last <= 0 {
			continue
		}
		swapPrice := volumeOK[base]                                       // 第 2 步存的合约价
		basisPct := (swapPrice - spotTicker.Last) / spotTicker.Last * 100 // 算基差百分数
		if basisPct <= minBasis {                                         // 基差约束：合约比现货贵得够多才有的赚
			continue
		}
		fr := passed[base]                // 取第 5 步的过滤结果
		currentRate := currentRates[base] // 取当前费率
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
			AnnualizedPct:  currentRate * 3 * 365, // 8 小时结算一次，一天 3 次 → 年化
			QuoteVolume24h: swapTickers[exchange.BaseToSwap(base)].QuoteVolume,
			Direction:      "空" + swapEx.ID() + "合约 / 多" + spotEx.ID() + "现货",
			UpdatedAt:      now,
		})
	}

	// 按当前资金费率降序（表现好的排前面）。
	// sort.Slice 传一个【比较函数】闭包：返回 true 表示 i 应排在 j 前面。
	// ⚠️ 注意 map 的遍历顺序是随机的，必须显式排序，别依赖遍历顺序。
	sort.Slice(out, func(i, j int) bool { return out[i].FundingRate > out[j].FundingRate })
	return out, nil
}

// CurrentFundingRate 查询单币对当前资金费率（%）（autotrade 卖出判断用，走公开连接）。
func (s *Service) CurrentFundingRate(baseSymbol string) (float64, error) {
	swapEx, err := s.hub.PublicSwap()
	if err != nil {
		return 0, err
	}
	rates, err := swapEx.FetchFundingRates([]string{baseSymbol}) // 只查一个币
	if err != nil {
		return 0, err
	}
	return rates[baseSymbol], nil // map 取值：费率
}

// CurrentBasisPct 查询单币对当前基差（%）（autotrade slow sell 判断用，走公开连接）。
func (s *Service) CurrentBasisPct(baseSymbol string) (float64, error) {
	spotEx, err := s.hub.PublicSpot()
	if err != nil {
		return 0, err
	}
	swapEx, err := s.hub.PublicSwap()
	if err != nil {
		return 0, err
	}
	spotPrice, err := spotEx.FetchLastPrice("spot", baseSymbol) // 现货价
	if err != nil {
		return 0, err
	}
	swapPrice, err := swapEx.FetchLastPrice("swap", baseSymbol) // 合约价
	if err != nil {
		return 0, err
	}
	if spotPrice <= 0 {
		return 0, nil // 防御：现货价异常时返回 0（当作没有基差），不报错
	}
	return (swapPrice - spotPrice) / spotPrice * 100, nil // 基差百分数
}

// zipBase 把“内部币对列表”和“ccxt 符号列表”一一对应成 map。
// 第 1 步产出了两个平行列表，这里 zip 成一个 map 方便按内部币对查 ccxt 符号。
func zipBase(bases, ccxtSymbols []string) map[string]string {
	out := make(map[string]string, len(bases)) // 预分配容量
	for i, b := range bases {                  // 按下标对应
		out[b] = ccxtSymbols[i] // 内部币对 -> 对应位置的 ccxt 符号
	}
	return out
}

// isRising 判断费率趋势是否上升：前一半均值 < 后一半均值。
// rates[:mid] / rates[mid:] 是切片的【切分语法】：[:mid]=前一半，[mid:]=后一半。
func isRising(rates []float64) bool {
	if len(rates) < 2 {
		return true // 数据太少无法判断，放行——宁可让后面更严的过滤来把关
	}
	mid := len(rates) / 2
	return average(rates[:mid]) < average(rates[mid:]) // 前一半均值 < 后一半均值 = 上升
}

// average 求均值。
func average(xs []float64) float64 {
	if len(xs) == 0 {
		return 0 // 防御：空切片返回 0，避免除 0
	}
	sum := 0.0
	for _, x := range xs { // 累加
		sum += x
	}
	return sum / float64(len(xs)) // 总和 / 个数（len 是 int，要转成 float64 才能除）
}
