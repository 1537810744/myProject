// 【阅读顺序 12】本文件是账户信息模块的持仓详情聚合。
//
// 目的（对应《第一次更新》第 2 条）：
// 原来的账户页只有“资金卡片 + 合约持仓表”，看不出每个对冲持仓对到底赚不赚钱。
// 参考成熟方案，为每个持仓对聚合出完整详情：
//
//	顶部统计：合约/现货占用资金、期现收益、费率收益、净收益、手续费、收益率、年化、
//	          运行时长、敞口、下次费率预估；
//	双腿详情：合约腿（数量/均价/标记价/浮动盈亏/下次费率/下次结算）与
//	          现货腿（数量/成本/最新价/持仓价值/浮动盈亏）；
//	敞口分析：合约持仓 vs 现货持仓 vs 净敞口；
//	三个页签：成交记录、资金费率流水、收益曲线（快照）。
//
// 数据来源：交易所实时（合约持仓/现货余额/最新价）+ 本地库（成交记录/资金费流水/快照）。
package account

import (
	"fmt"
	"math"
	"time"

	"deltacrypto/internal/model"
)

// PositionDetail 聚合某个币对的持仓详情（详情接口主入口）
func (s *Service) PositionDetail(symbol string) (*model.PositionDetail, error) {
	detail := &model.PositionDetail{Symbol: symbol, Status: "open"}

	// ---------- 第 1 步：本地持仓记录（可能同币多组，聚合数量与成本） ----------
	positions, err := s.trade.OpenPositions()
	if err != nil {
		return nil, err
	}
	var spotAmount, swapAmount, spotCost, swapCost float64
	var openedAt time.Time
	for _, p := range positions {
		if p.Symbol != symbol {
			continue
		}
		spotAmount += p.SpotAmount
		swapAmount += p.SwapAmount
		spotCost += p.SpotAmount * p.SpotEntryPrice
		swapCost += p.SwapAmount * p.SwapEntryPrice
		if openedAt.IsZero() || p.OpenedAt.Before(openedAt) {
			openedAt = p.OpenedAt
		}
	}
	if spotAmount == 0 && swapAmount == 0 {
		return nil, fmt.Errorf("未找到 %s 的持仓", symbol)
	}
	spotEntry := safeDiv(spotCost, spotAmount) // 现货成本均价（加权）
	swapEntry := safeDiv(swapCost, swapAmount) // 合约开仓均价（加权）

	// ---------- 第 2 步：交易所实时数据 ----------
	now := time.Now()
	spotEx, spotErr := s.hub.Spot()
	swapEx, swapErr := s.hub.Swap()

	// 合约腿实时：标记价、浮动盈亏（从交易所持仓接口）
	var markPrice, swapUnrealized, leverage float64
	if swapErr == nil {
		if swapPositions, err := swapEx.FetchSwapPositions(); err == nil {
			for _, p := range swapPositions {
				if p.Symbol == symbol {
					markPrice = p.MarkPrice
					swapUnrealized = p.UnrealizedPnl
					leverage = p.Leverage
					break
				}
			}
		}
		// 若该所在持仓接口中查不到（如已部分平仓），用最新价兜底
		if markPrice == 0 {
			markPrice, _ = swapEx.FetchLastPrice("swap", symbol)
		}
	}
	// 现货腿实时：最新价（算持仓价值与浮动盈亏）
	var spotLast float64
	if spotErr == nil {
		spotLast, _ = spotEx.FetchLastPrice("spot", symbol)
	}
	if leverage <= 0 {
		leverage = s.settings.GetFloat("leverage")
		if leverage <= 0 {
			leverage = 4
		}
	}

	// 现货浮动盈亏 = (最新价 - 成本价) × 数量
	spotUnrealized := (spotLast - spotEntry) * spotAmount
	// 期现收益 = 合约浮动盈亏 + 现货浮动盈亏
	basisPnl := swapUnrealized + spotUnrealized

	// ---------- 第 3 步：手续费 / 资金费（本地库） ----------
	feeUSDT, _ := s.trade.FeeBySymbol(symbol)          // 手续费合计（USDT 部分）
	fundingCum, _ := s.FundingSum(symbol)              // 资金费累计（同步自交易所流水）

	// ---------- 第 4 步：汇总统计 ----------
	swapMargin := safeDiv(swapEntry*swapAmount, leverage) // 合约占用保证金
	spotUsed := spotCost                                  // 现货占用资金
	totalUsed := swapMargin + spotUsed
	netProfit := basisPnl + fundingCum - feeUSDT
	yieldPct := safeDiv(netProfit, totalUsed) * 100
	runDays := math.Max(time.Since(openedAt).Hours()/24, 1.0/24) // 至少按 1 小时计，避免年化爆炸
	annualized := yieldPct * 365 / runDays

	// 下次费率预估 = 当前费率% × 合约名义价值
	currentRate, _ := s.CurrentFundingRate(symbol)
	nextFundingEst := currentRate * markPrice * swapAmount / 100

	detail.Stats = model.PositionDetailStats{
		SwapMarginUsed: swapMargin,
		SpotCostUsed:   spotUsed,
		BasisPnl:       basisPnl,
		FundingPnl:     fundingCum,
		NetProfit:      netProfit,
		FeeUSDT:        feeUSDT,
		YieldPct:       yieldPct,
		AnnualizedPct:  annualized,
		RunDuration:    humanDuration(time.Since(openedAt)),
		NetExposure:    math.Abs(spotAmount - swapAmount),
		NextFundingEst: nextFundingEst,
	}
	detail.SwapLeg = model.PositionLegDetail{
		Exchange:       detailExchangeName(swapEx, swapErr),
		Amount:         swapAmount,
		AvgPrice:       swapEntry,
		MarkPrice:      markPrice,
		ValueUSDT:      markPrice * swapAmount,
		UnrealizedPnl:  swapUnrealized,
		RealizedPnl:    0, // 平仓前不核算已实现盈亏（个人工具简化）
		NextFundingPct: currentRate,
		NextSettleAt:   nextFundingTime(now).Format("2006-01-02 15:04:05"),
		LastSyncAt:     now.Format("2006-01-02 15:04:05"),
	}
	detail.SpotLeg = model.PositionLegDetail{
		Exchange:      detailExchangeName(spotEx, spotErr),
		Amount:        spotAmount,
		AvgPrice:      spotEntry,
		MarkPrice:     spotLast,
		ValueUSDT:     spotLast * spotAmount,
		UnrealizedPnl: spotUnrealized,
		LastSyncAt:    now.Format("2006-01-02 15:04:05"),
	}
	detail.Exposure = map[string]float64{
		"swap_amount":  swapAmount,
		"spot_amount":  spotAmount,
		"net_exposure": math.Abs(spotAmount - swapAmount),
	}
	return detail, nil
}

// CurrentFundingRate 查询当前资金费率（%）（详情页用；公开接口无需凭证）
func (s *Service) CurrentFundingRate(symbol string) (float64, error) {
	swapEx, err := s.hub.PublicSwap()
	if err != nil {
		return 0, err
	}
	rates, err := swapEx.FetchFundingRates([]string{symbol})
	if err != nil {
		return 0, err
	}
	return rates[symbol], nil
}

// ---------- 资金费流水：同步与查询 ----------

// SyncFundingPayments 从合约交易所同步资金费流水入库（INSERT OR IGNORE 去重）。
// 无凭证时静默跳过（公开模式也能用详情页，只是没有资金费数据）。
func (s *Service) SyncFundingPayments() {
	swapEx, err := s.hub.Swap()
	if err != nil {
		return
	}
	payments, err := swapEx.FetchFundingPayments("", 100)
	if err != nil {
		return
	}
	for _, p := range payments {
		s.db.Exec(
			`INSERT OR IGNORE INTO funding_payment(exchange, symbol, amount, income_id, income_at) VALUES(?,?,?,?,?)`,
			swapEx.ID(), p.Symbol, p.Amount, p.ID, p.Time)
	}
}

// FundingSum 某币对资金费累计（USDT）
func (s *Service) FundingSum(symbol string) (float64, error) {
	var sum float64
	err := s.db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM funding_payment WHERE symbol = ?`, symbol).Scan(&sum)
	return sum, err
}

// FundingRecords 某币对的资金费流水明细（详情页“资金费率流水”页签）
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
	defer rows.Close()
	out := make([]model.FundingPaymentRecord, 0)
	for rows.Next() {
		var r model.FundingPaymentRecord
		if err := rows.Scan(&r.ID, &r.Exchange, &r.Symbol, &r.Amount, &r.IncomeAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---------- 收益快照：记录与查询 ----------

// SnapshotProfit 为全部 open 持仓各记录一条收益快照（自动交易模块每轮调用）。
// 目的：攒出“收益曲线”的时间序列数据，前端画折线。
func (s *Service) SnapshotProfit() {
	positions, err := s.trade.OpenPositions()
	if err != nil || len(positions) == 0 {
		return
	}
	// 逐币对聚合快照（复用详情计算，失败跳过该币）
	symbols := make(map[string]bool)
	for _, p := range positions {
		symbols[p.Symbol] = true
	}
	for symbol := range symbols {
		detail, err := s.PositionDetail(symbol)
		if err != nil {
			continue
		}
		s.db.Exec(
			`INSERT INTO profit_snapshot(symbol, net_profit, basis_pnl, funding_cum, fee_cum) VALUES(?,?,?,?,?)`,
			symbol, detail.Stats.NetProfit, detail.Stats.BasisPnl, detail.Stats.FundingPnl, detail.Stats.FeeUSDT)
	}
}

// ProfitHistory 某币对的收益曲线数据（详情页“收益曲线”页签）
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
	defer rows.Close()
	out := make([]model.ProfitPoint, 0)
	for rows.Next() {
		var p model.ProfitPoint
		if err := rows.Scan(&p.Time, &p.NetProfit, &p.BasisPnl, &p.FundingCum, &p.FeeCum); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------- 工具函数 ----------

// safeDiv 安全除法（除数为 0 返回 0）
func safeDiv(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}

// humanDuration 人性化时长：1天2小时 / 3小时25分
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	hours := int(d.Hours())
	days := hours / 24
	if days > 0 {
		return fmt.Sprintf("%d天%d小时", days, hours%24)
	}
	if hours > 0 {
		return fmt.Sprintf("%d小时%d分", hours, int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d分钟", int(d.Minutes()))
}

// nextFundingTime 计算币安资金费下一个结算点（UTC 0/8/16 点，转本地时区显示）
func nextFundingTime(now time.Time) time.Time {
	utc := now.UTC()
	next := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	for !next.After(utc) {
		next = next.Add(8 * time.Hour)
	}
	return next.Local()
}

// detailExchangeName 取交易所名（连接不可用时返回占位符）
func detailExchangeName(ex interface{ ID() string }, err error) string {
	if err != nil || ex == nil {
		return "-"
	}
	return ex.ID()
}
