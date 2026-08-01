// 【阅读顺序 12】账户模块的持仓详情聚合（怎么算钱全在这）。
// 数据来源：交易所实时（合约持仓/现货余额/最新价）+ 本地库（成交记录/资金费流水/快照）。
// 套利账本先理解再读代码：
//
//	一笔对冲持仓 = 现货买 + 合约空，各持有某币同数量。两类收益：
//	① 期现收益 basis_pnl = 合约浮动盈亏 + 现货浮动盈亏（方向相反、涨跌相抵，
//	   剩下的差值才是期现收敛的利润）；
//	② 费率收益 funding_pnl = 空头收的资金费累计。
//	净收益 = 期现 + 费率 - 手续费；收益率 = 净收益 / 两腿占用资金；年化 = 收益率×365/持有天数。
//
// 语法点预览：聚合循环、safeDiv 安全除法、math.Abs/Max、time.Since、map[string]float64、
// 内联接口 interface{ ID() string }。
package account

// import 导入用到的包。
import (
	"fmt"  // 格式化
	"math" // 数学
	"time" // 时间

	"deltacrypto/internal/model" // 数据结构
)

// PositionDetail 聚合某个币对的持仓详情（详情接口主入口）。
func (s *Service) PositionDetail(symbol string) (*model.PositionDetail, error) {
	detail := &model.PositionDetail{Symbol: symbol, Status: "open"} // 初始化结果

	// ---- 第 1 步：本地持仓记录（可能同币多组，聚合数量与成本）----
	// 为什么可能“同币多组”？—— 自动交易每轮买一组，同一个币可能在不同轮各买
	// 了一组（50U），数据库里就是多行。这里按币把多组合并成一条。
	positions, err := s.trade.OpenPositions()
	if err != nil {
		return nil, err
	}
	var spotAmount, swapAmount, spotCost, swapCost float64 // 4 个累加器
	var openedAt time.Time                                 // 最早开仓时间（零值 = 0001-01-01）
	for _, p := range positions {                          // 遍历所有持仓
		if p.Symbol != symbol { // 不是目标币对
			continue // 跳过
		}
		spotAmount += p.SpotAmount                  // 累加现货数量
		swapAmount += p.SwapAmount                  // 累加合约数量
		spotCost += p.SpotAmount * p.SpotEntryPrice // 现货成本：每组“数量×各自成本”累加
		swapCost += p.SwapAmount * p.SwapEntryPrice // 合约成本：同样的加权累加
		// 注意：不能只累加“均价”，因为每组数量不同，必须用“数量×价格”累加出
		// 总成本，最后再用 总成本/总数量 算加权均价（见下面 spotEntry/swapEntry）。
		// openedAt.IsZero()：判断时间是不是【零值】（0001-01-01，即从没赋值过）；
		// p.OpenedAt.Before(openedAt)：判断这个持仓的开仓时间是否【早于】已记录的最早时间。
		// 两者取“或”（||）就是：第一次遇到这个币（还没记录过），或这个币更早开仓 → 更新最早时间。
		if openedAt.IsZero() || p.OpenedAt.Before(openedAt) {
			openedAt = p.OpenedAt // 取最早开仓时间作为整组运行起点
		}
	}
	if spotAmount == 0 && swapAmount == 0 { // 目标币没有持仓
		return nil, fmt.Errorf("未找到 %s 的持仓", symbol)
	}
	spotEntry := safeDiv(spotCost, spotAmount) // 现货成本均价（加权）
	swapEntry := safeDiv(swapCost, swapAmount) // 合约开仓均价（加权）

	// ---- 第 2 步：交易所实时数据 ----
	now := time.Now() // 取当前时间（后面多处用，统一一个）
	spotEx, spotErr := s.hub.Spot()
	swapEx, swapErr := s.hub.Swap()
	// 连接可能失败（没配凭证），用 err 变量记着，后面各自降级。

	// 合约腿实时：标记价、浮动盈亏（从交易所持仓接口拿）。
	var markPrice, swapUnrealized, leverage float64
	if swapErr == nil { // 合约连接可用
		if swapPositions, err := swapEx.FetchSwapPositions(); err == nil { // 拉持仓
			for _, p := range swapPositions { // 找目标币
				if p.Symbol == symbol {
					markPrice = p.MarkPrice          // 标记价
					swapUnrealized = p.UnrealizedPnl // 未实现盈亏
					leverage = p.Leverage            // 杠杆
					break                            // 找到就跳出循环（break）
				}
			}
		}
		// 若持仓接口里查不到该币（如已部分平仓），用最新价兜底。
		if markPrice == 0 {
			markPrice, _ = swapEx.FetchLastPrice("swap", symbol)
		}
	}
	// 现货腿实时：最新价（算持仓价值与浮动盈亏）。
	var spotLast float64
	if spotErr == nil {
		spotLast, _ = spotEx.FetchLastPrice("spot", symbol)
	}
	if leverage <= 0 { // 没拿到杠杆
		leverage = s.settings.GetFloat("leverage") // 从设置读
		if leverage <= 0 {
			leverage = 4 // 兜底
		}
	}

	// 现货浮动盈亏 =（最新价 - 成本价）× 数量。
	spotUnrealized := (spotLast - spotEntry) * spotAmount
	// 期现收益 = 合约浮动盈亏 + 现货浮动盈亏（见文件头：两腿相抵后剩价差利润）。
	basisPnl := swapUnrealized + spotUnrealized

	// ---- 第 3 步：手续费 / 资金费（本地库）----
	feeUSDT, _ := s.trade.FeeBySymbol(symbol) // 手续费合计（USDT 部分）
	fundingCum, _ := s.FundingSum(symbol)     // 资金费累计（同步自交易所流水）

	// ---- 第 4 步：汇总统计 ----
	swapMargin := safeDiv(swapEntry*swapAmount, leverage)        // 合约占用保证金 = 名义价值/杠杆
	spotUsed := spotCost                                         // 现货占用资金 = 买入成本
	totalUsed := swapMargin + spotUsed                           // 两腿总占用
	netProfit := basisPnl + fundingCum - feeUSDT                 // 净收益
	yieldPct := safeDiv(netProfit, totalUsed) * 100              // 收益率（百分数）
	runDays := math.Max(time.Since(openedAt).Hours()/24, 1.0/24) // 至少按 1 小时计
	// time.Since(openedAt)：从开仓到现在经过多久；.Hours() 转成小时数。
	// 为什么至少按 1/24 天？—— 刚开仓 1 分钟就显示“年化 +50000%”只会吓到人。
	// 年化是“把当前收益假设维持一年”的外推，持有太短时没意义，设下限让数字不荒唐。
	annualized := yieldPct * 365 / runDays // 年化 = 收益率 × 365 / 持有天数

	// 下次费率预估 = 当前费率% × 合约名义价值。
	currentRate, _ := s.CurrentFundingRate(symbol)               // 当前费率
	nextFundingEst := currentRate * markPrice * swapAmount / 100 // 费率%×名义价值/100

	detail.Stats = model.PositionDetailStats{ // 填顶部统计
		SwapMarginUsed: swapMargin,
		SpotCostUsed:   spotUsed,
		BasisPnl:       basisPnl,
		FundingPnl:     fundingCum,
		NetProfit:      netProfit,
		FeeUSDT:        feeUSDT,
		YieldPct:       yieldPct,
		AnnualizedPct:  annualized,
		RunDuration:    humanDuration(time.Since(openedAt)),
		NetExposure:    math.Abs(spotAmount - swapAmount), // 敞口 = 两腿数量差的绝对值
		NextFundingEst: nextFundingEst,
	}
	detail.SwapLeg = model.PositionLegDetail{ // 填合约腿详情
		Exchange:       detailExchangeName(swapEx, swapErr),
		Amount:         swapAmount,
		AvgPrice:       swapEntry,
		MarkPrice:      markPrice,
		ValueUSDT:      markPrice * swapAmount,
		UnrealizedPnl:  swapUnrealized,
		RealizedPnl:    0, // 平仓前不核算已实现盈亏（个人工具简化）
		NextFundingPct: currentRate,
		NextSettleAt:   nextFundingTime(now).Format("2006-01-02 15:04:05"), // 下一个资金费结算时刻
		// ⚠️ "2006-01-02 15:04:05" 是 Go 的【固定参考时间】模板（=2006年1月2日15点04分05秒）。
		// Go 不用 %Y-%m-%d 那套占位符，而是用这个固定日期当模板：数字摆哪个位置就输出哪部分，
		// 2006=年、01=月、02=日、15=时(24小时制)、04=分、05=秒。所以这句输出“年-月-日 时:分:秒”。
		LastSyncAt: now.Format("2006-01-02 15:04:05"),
	}
	detail.SpotLeg = model.PositionLegDetail{ // 填现货腿详情
		Exchange:      detailExchangeName(spotEx, spotErr),
		Amount:        spotAmount,
		AvgPrice:      spotEntry,
		MarkPrice:     spotLast,
		ValueUSDT:     spotLast * spotAmount,
		UnrealizedPnl: spotUnrealized,
		// 同样的参考时间模板（见上面合约腿的说明）："2006-01-02 15:04:05" = 年-月-日 时:分:秒。
		LastSyncAt: now.Format("2006-01-02 15:04:05"),
	}
	detail.Exposure = map[string]float64{ // 敞口分析（map 字面量）
		"swap_amount":  swapAmount,                        // 合约持仓
		"spot_amount":  spotAmount,                        // 现货持仓
		"net_exposure": math.Abs(spotAmount - swapAmount), // 净敞口
	}
	return detail, nil
}

// CurrentFundingRate 查询当前资金费率（%）（详情页用；公开接口无需凭证）。
func (s *Service) CurrentFundingRate(symbol string) (float64, error) {
	swapEx, err := s.hub.PublicSwap()
	if err != nil {
		return 0, err
	}
	rates, err := swapEx.FetchFundingRates([]string{symbol}) // 只查一个币
	if err != nil {
		return 0, err
	}
	return rates[symbol], nil // map 取值
}

// ---------- 资金费流水：同步与查询 ----------

// SyncFundingPayments 从合约交易所同步资金费流水入库。
func (s *Service) SyncFundingPayments() {
	swapEx, err := s.hub.Swap()
	if err != nil {
		return // 没配凭证直接跳过——尽力而为的同步，不打扰
	}
	payments, err := swapEx.FetchFundingPayments("", 100) // 拉最近 100 条（空币对=全部）
	if err != nil {
		return
	}
	for _, p := range payments { // 遍历
		// INSERT OR IGNORE + 表的 UNIQUE(exchange, symbol, income_at) 约束：
		// 同一笔结算重复同步会被数据库挡下。“让数据库保证不重复”比“先查再决定插不插”
		// 更可靠（原子，且并发也安全）。
		s.db.Exec(
			`INSERT OR IGNORE INTO funding_payment(exchange, symbol, amount, income_id, income_at) VALUES(?,?,?,?,?)`,
			swapEx.ID(), p.Symbol, p.Amount, p.ID, p.Time)
	}
}

// FundingSum 某币对资金费累计（USDT）。
func (s *Service) FundingSum(symbol string) (float64, error) {
	var sum float64
	err := s.db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM funding_payment WHERE symbol = ?`, symbol).Scan(&sum)
	return sum, err
}

// FundingRecords 某币对的资金费流水明细（详情页“资金费率流水”页签）。
func (s *Service) FundingRecords(symbol string, limit int) ([]model.FundingPaymentRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT id, exchange, symbol, amount, income_at FROM funding_payment
		 WHERE symbol = ? ORDER BY income_at DESC LIMIT ?`, symbol, limit)
	if err != nil {
		return nil, err
	}
	// defer rows.Close()：rows 是查询结果的【游标】，占着底层连接，用完必须关，
	// 否则连接池会被占满。defer 保证无论从哪一行 return 都会关闭。
	out := make([]model.FundingPaymentRecord, 0) // 空切片而非 nil：JSON 输出 [] 而非 null（前端遍历安全）
	for rows.Next() {                            // 游标推进：还有下一行就继续
		var r model.FundingPaymentRecord
		// rows.Scan：把当前行的列按顺序填进结构体字段（传 & 指针才能写入）。
		if err := rows.Scan(&r.ID, &r.Exchange, &r.Symbol, &r.Amount, &r.IncomeAt); err != nil {
			return nil, err
		}
		out = append(out, r) // 收集这一行
	}
	// rows.Err()：遍历完后检查中途错误（连接断开等），游标模式的标准收尾。
	return out, rows.Err()
}

// ---------- 收益快照：记录与查询 ----------

// SnapshotProfit 为全部 open 持仓各记一条收益快照（自动交易每轮调用）。
// 目的：攒“收益曲线”的时间序列，前端画折线。每轮把当前净收益等数据插表，
// 一段时间后就攒出一条随时间变化的曲线。
func (s *Service) SnapshotProfit() {
	positions, err := s.trade.OpenPositions()
	if err != nil || len(positions) == 0 { // 出错或没持仓
		return
	}
	// 先收集所有涉及的币（去重，map 当集合）。
	symbols := make(map[string]bool) // 用 map[string]bool 当集合
	for _, p := range positions {
		symbols[p.Symbol] = true // 标记出现过（去重：重复赋值没影响）
	}
	for symbol := range symbols { // 遍历集合（只取键）
		// 复用 PositionDetail 的计算结果。
		detail, err := s.PositionDetail(symbol)
		if err != nil {
			continue // 单币计算失败不影响其它币
		}
		s.db.Exec(
			`INSERT INTO profit_snapshot(symbol, net_profit, basis_pnl, funding_cum, fee_cum) VALUES(?,?,?,?,?)`,
			symbol, detail.Stats.NetProfit, detail.Stats.BasisPnl, detail.Stats.FundingPnl, detail.Stats.FeeUSDT)
	}
}

// ProfitHistory 某币对的收益曲线数据（详情页“收益曲线”页签）。
func (s *Service) ProfitHistory(symbol string, limit int) ([]model.ProfitPoint, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(
		`SELECT ts, net_profit, basis_pnl, funding_cum, fee_cum FROM profit_snapshot
		 WHERE symbol = ? ORDER BY ts ASC LIMIT ?`, symbol, limit)
	if err != nil {
		return nil, err
	}
	// 又是同一个“SQL 游标”套路（本项目每个列表查询都长这样）：
	// rows.Close() 释放游标占用的连接（defer 保证一定执行）；
	// make([]T, 0) 空切片保证 JSON 输出 []；rows.Next() 逐行推进；Scan 填行数据。
	defer rows.Close()
	out := make([]model.ProfitPoint, 0)
	for rows.Next() {
		var p model.ProfitPoint
		if err := rows.Scan(&p.Time, &p.NetProfit, &p.BasisPnl, &p.FundingCum, &p.FeeCum); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err() // 同上：检查遍历中途错误，游标模式的收尾动作

}

// ---------- 工具函数 ----------

// safeDiv 安全除法（除数为 0 返回 0）。
// 为什么到处用它？—— 价格/数量可能是 0（没拉到时），直接除会得到无穷大/NaN，污染 UI。
func safeDiv(a, b float64) float64 {
	if b == 0 { // 分母为 0
		return 0 // 返回 0 而不是报错
	}
	return a / b // 正常除
}

// humanDuration 人性化时长：1天2小时 / 3小时25分。
func humanDuration(d time.Duration) string {
	if d < 0 { // 负数防御（时间回拨等极端情况）
		d = 0
	}
	hours := int(d.Hours()) // 总小时数（int(...) 是类型转换：float64 → int 截断取整）
	days := hours / 24      // 换算成天
	if days > 0 {
		// 注意这里有两个含义不同的 %：格式串里的 %d = 整数占位符；hours%24 里的 % = 取余运算符。
		return fmt.Sprintf("%d天%d小时", days, hours%24)
	}
	if hours > 0 {
		return fmt.Sprintf("%d小时%d分", hours, int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d分钟", int(d.Minutes()))
}

// nextFundingTime 算币安下一个资金费结算点（UTC 0/8/16 点）。
// 为什么是 0/8/16？—— 币安永续资金费每 8 小时结算一次，结算时刻是 UTC 的 0/8/16 点。
func nextFundingTime(now time.Time) time.Time {
	utc := now.UTC() // 转成 UTC 时区
	// time.Date(...)：手动构造一个时间（今天 0 点整）。
	next := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	for !next.After(utc) { // 只要还没超过当前时刻
		next = next.Add(8 * time.Hour) // 就加 8 小时（跳到下一个结算点）
	}
	return next.Local() // 转本地时区显示给用户
}

// detailExchangeName 取交易所名（连接不可用时返回占位符“-”）。
// 参数类型 interface{ ID() string } 是【内联接口】：只要传入对象有 ID() string 方法
// 即可（*exchange.Exchange 满足）。这样本函数不用 import exchange 包，只依赖
// “最小方法集合”——弱耦合。
func detailExchangeName(ex interface{ ID() string }, err error) string {
	if err != nil || ex == nil { // 出错 或 对象为空
		return "-"
	}
	return ex.ID() // 调接口方法拿名字
}
