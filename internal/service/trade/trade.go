// 【阅读顺序 10】模块 3：交易模块（建仓/平仓的统一入口）。
//
// 本文件职责：对外提供 Open/Close 两个入口，内部是“拆单循环”，
// 每一轮的双腿执行委托给【阅读顺序 09】的引擎（engine.go）。
// 阅读目的：抓住两条线——
//
//	执行线：参数兜底 → 组装引擎 → 拆单循环（总量切原子量、粉尘带走、轮间停顿 1 秒看行情）
//	落库线：执行中收集成交明细(Fill) → 结束后写持仓表 + trade_fill 成交记录表
//	         （成交记录是持仓详情页“成交记录/手续费”的数据来源）。
//
// 上下游：被 httpserver（手动交易）与 autotrade（自动交易）调用；
// 持仓表 hedge_position 同时被 account/alert/autotrade 读取。
package trade

import (
	"fmt"
	"math"
	"time"

	"deltacrypto/internal/database"
	"deltacrypto/internal/exchange"
	"deltacrypto/internal/model"
	"deltacrypto/internal/service/settings"
)

// Service 交易模块服务
type Service struct {
	db       *database.DB
	hub      *exchange.Hub
	settings *settings.Service
}

// New 创建交易模块
func New(db *database.DB, hub *exchange.Hub, settings *settings.Service) *Service {
	return &Service{db: db, hub: hub, settings: settings}
}

// Open 建仓：买现货（gate）+ 空合约（binance），按原子单位拆单执行。
// 成功后写入 hedge_position 持仓表，供账户/预警/自动交易模块使用。
func (s *Service) Open(req model.TradeRequest) (*model.TradeResult, error) {
	return s.execute(req, true, 0)
}

// Close 平仓：卖现货 + 平空合约（reduceOnly），成功后把持仓置为 closed。
// positionID 用于更新持仓表；传入 0 表示纯手动平仓（不更新表）。
func (s *Service) Close(req model.TradeRequest, positionID int64) (*model.TradeResult, error) {
	return s.execute(req, false, positionID)
}

// execute 建仓/平仓的统一执行流程（方向由 isOpen 区分）。
//
// 执行结构（对应参考方案）：
//
//	拆单循环（本函数）：把总量按原子单位切成多轮，逐轮执行，轮间停顿看行情（牛吃草）；
//	每轮双腿（engine.HedgeOnce）：优先腿现货先走（Maker 挂单追价或 Taker），
//	  对冲腿合约实时跟进优先腿的成交量，净敞口超阈值自动停止告警。
func (s *Service) execute(req model.TradeRequest, isOpen bool, positionID int64) (*model.TradeResult, error) {
	spotEx, err := s.hub.Spot()
	if err != nil {
		return nil, err
	}
	swapEx, err := s.hub.Swap()
	if err != nil {
		return nil, err
	}

	// 参数兜底：未显式指定时从设置模块读取
	if req.TotalUSDT <= 0 {
		req.TotalUSDT = s.settings.GetFloat(settings.KeyGroupSizeUSDT)
	}
	if req.AtomUSDT <= 0 {
		req.AtomUSDT = s.settings.GetFloat(settings.KeyAtomSizeUSDT)
	}
	if req.DustUSDT <= 0 {
		req.DustUSDT = s.settings.GetFloat(settings.KeyDustUSDT)
	}

	action := "close"
	if isOpen {
		action = "open"
	}
	s.log("info", action, req.Symbol,
		fmt.Sprintf("开始%s：总量 %.2fU，原子 %.2fU，下单方式 %s（前%d档，最多追价%d次）",
			actionName(isOpen), req.TotalUSDT, req.AtomUSDT,
			s.settings.Get(settings.KeyOrderMethod),
			s.settings.GetInt(settings.KeyOrderbookLevel),
			s.settings.GetInt(settings.KeyMaxChaseCount)))

	// 建仓前设置合约杠杆（失败仅警告，不阻断）
	if isOpen {
		lev := int64(s.settings.GetInt(settings.KeyLeverage))
		if err := swapEx.SetLeverage(req.Symbol, lev); err != nil {
			s.log("warn", action, req.Symbol, fmt.Sprintf("设置杠杆 %dx 失败（继续下单）: %v", lev, err))
		}
	}

	// —— 组装交易引擎（全部参数来自设置模块，前端设置页可调） ——
	engine := NewEngine(spotEx, swapEx,
		EngineConfig{
			MaxNetExposure: s.settings.GetFloat(settings.KeyMaxNetExposure),
			MaxRetry:       s.settings.GetInt(settings.KeyMaxRetry),
			PollInterval:   1 * time.Second, // 订单轮询间隔：低频工具 1 秒
		},
		LegConfig{
			OrderMethod:  s.settings.Get(settings.KeyOrderMethod),
			Level:        s.settings.GetInt(settings.KeyOrderbookLevel),
			MaxChase:     s.settings.GetInt(settings.KeyMaxChaseCount),
			ChaseToTaker: s.settings.GetInt(settings.KeyChaseToTaker) == 1,
		},
		s.log,
	)

	// 以现货最新价折算币数量
	spotPrice, err := spotEx.FetchLastPrice("spot", req.Symbol)
	if err != nil || spotPrice <= 0 {
		return nil, fmt.Errorf("获取 %s 现货价格失败: %v", req.Symbol, err)
	}

	result := &model.TradeResult{
		SpotLeg:   &model.LegResult{Exchange: spotEx.ID(), MarketType: "spot"},
		SwapLeg:   &model.LegResult{Exchange: swapEx.ID(), MarketType: "swap"},
		Timestamp: time.Now(),
	}
	// 收集全部成交明细（执行完成后统一写 trade_fill 表，关联持仓）
	var allFills []Fill

	// 双腿方向：建仓=现货买+合约空；平仓=现货卖+合约买回（reduceOnly）
	spotSide, swapSide := "buy", "sell"
	if !isOpen {
		spotSide, swapSide = "sell", "buy"
	}

	// —— 拆单循环：牛吃草，吃一口抬头看一眼 ——
	remainingUSDT := req.TotalUSDT
	round := 0
	for remainingUSDT > 0 {
		// 本轮计划量：min(原子单位, 剩余)
		roundUSDT := math.Min(req.AtomUSDT, remainingUSDT)
		// 粉尘处理：本轮执行后剩余低于粉尘阈值，则一并带走
		if rest := remainingUSDT - roundUSDT; rest > 0 && rest < req.DustUSDT {
			roundUSDT = remainingUSDT
		}
		round++

		// 折算币数量并按交易所精度截断
		amount := spotEx.AmountToPrecision("spot", req.Symbol, roundUSDT/spotPrice)
		if amount <= 0 {
			return result, fmt.Errorf("第 %d 轮数量精度换算后为 0，终止", round)
		}

		// 引擎执行本轮双腿（优先腿现货 -> 对冲腿合约实时跟进）
		spotOut, swapOut, err := engine.HedgeOnce(req.Symbol, spotSide, swapSide, amount, !isOpen)
		if spotOut != nil {
			mergeLegOutcome(result.SpotLeg, spotOut)
			allFills = append(allFills, spotOut.Fills...)
		}
		if swapOut != nil {
			mergeLegOutcome(result.SwapLeg, swapOut)
			allFills = append(allFills, swapOut.Fills...)
		}
		if err != nil {
			// 引擎内部已写明失败原因与净敞口告警；这里直接终止本组执行
			return result, fmt.Errorf("第 %d 轮执行失败: %w", round, err)
		}

		remainingUSDT -= roundUSDT
		s.log("info", action, req.Symbol,
			fmt.Sprintf("第 %d 轮对冲完成：现货成交 %.6f，合约成交 %.6f",
				round, spotOut.Amount, swapOut.Amount))

		// 牛吃草：抬头看一眼（轮间停顿，低频工具 1 秒足够）
		if remainingUSDT > 0 {
			time.Sleep(1 * time.Second)
			// 更新现货价，避免拆单期间价格漂移导致数量失真
			if p, err := spotEx.FetchLastPrice("spot", req.Symbol); err == nil && p > 0 {
				spotPrice = p
			}
		}
	}

	// —— 落库：持仓表 + 成交记录表 ——
	var savedPositionID = positionID
	if isOpen {
		basis := 0.0
		if result.SpotLeg.AvgPrice > 0 {
			basis = (result.SwapLeg.AvgPrice - result.SpotLeg.AvgPrice) / result.SpotLeg.AvgPrice * 100
		}
		id, err := s.insertPosition(req, result, basis)
		if err != nil {
			s.log("error", action, req.Symbol, "持仓写入数据库失败: "+err.Error())
		} else {
			savedPositionID = id
		}
	} else if positionID > 0 {
		if err := s.closePosition(positionID); err != nil {
			s.log("error", action, req.Symbol, "持仓状态更新失败: "+err.Error())
		}
	}
	// 成交明细写表（持仓详情页“成交记录”页签的数据来源）
	if err := s.saveFills(savedPositionID, req.Symbol, allFills); err != nil {
		s.log("error", action, req.Symbol, "成交记录写入失败: "+err.Error())
	}

	result.Success = true
	result.Message = fmt.Sprintf("%s完成：现货 %.6f @ %.6f，合约 %.6f @ %.6f",
		actionName(isOpen),
		result.SpotLeg.Amount, result.SpotLeg.AvgPrice,
		result.SwapLeg.Amount, result.SwapLeg.AvgPrice)
	s.log("info", action, req.Symbol, result.Message)
	return result, nil
}

// mergeLegOutcome 把引擎一轮的腿执行结果合并进 TradeResult 的腿汇总（数量、加权均价、订单号）
func mergeLegOutcome(leg *model.LegResult, out *LegOutcome) {
	prevCost := leg.AvgPrice * leg.Amount
	leg.Amount += out.Amount
	leg.CostUSDT += out.CostUSDT
	if leg.Amount > 0 {
		leg.AvgPrice = (prevCost + out.CostUSDT) / leg.Amount
	}
	leg.Side = out.Fills[len(out.Fills)-1].Side
	for _, f := range out.Fills {
		leg.OrderIDs = append(leg.OrderIDs, f.OrderID)
	}
}

// insertPosition 建仓成功后写入持仓表，返回持仓 ID（成交记录关联用）
func (s *Service) insertPosition(req model.TradeRequest, result *model.TradeResult, basisPct float64) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO hedge_position(symbol, spot_exchange, swap_exchange, spot_amount, swap_amount,
			spot_entry_price, swap_entry_price, entry_basis_pct, status) VALUES(?,?,?,?,?,?,?,?, 'open')`,
		req.Symbol, result.SpotLeg.Exchange, result.SwapLeg.Exchange,
		result.SpotLeg.Amount, result.SwapLeg.Amount,
		result.SpotLeg.AvgPrice, result.SwapLeg.AvgPrice, basisPct)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// saveFills 把一轮执行的成交明细批量写入 trade_fill 表（持仓详情页“成交记录”页签）
func (s *Service) saveFills(positionID int64, symbol string, fills []Fill) error {
	for _, f := range fills {
		maker := 0
		if f.Maker {
			maker = 1
		}
		if _, err := s.db.Exec(
			`INSERT INTO trade_fill(position_id, symbol, exchange, market_type, side, price, amount,
				cost_usdt, fee, fee_currency, order_id, maker, traded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			positionID, symbol, f.Exchange, f.MarketType, f.Side, f.Price, f.Amount,
			f.CostUSDT, f.Fee, f.FeeCurrency, f.OrderID, maker, f.Time); err != nil {
			return err
		}
	}
	return nil
}

// closePosition 平仓成功后把持仓置为 closed
func (s *Service) closePosition(positionID int64) error {
	_, err := s.db.Exec(
		`UPDATE hedge_position SET status = 'closed', closed_at = CURRENT_TIMESTAMP WHERE id = ?`, positionID)
	return err
}

// OpenPositions 查询当前全部 open 持仓（账户/预警/自动交易模块共用）
func (s *Service) OpenPositions() ([]model.HedgePosition, error) {
	rows, err := s.db.Query(
		`SELECT id, symbol, spot_exchange, swap_exchange, spot_amount, swap_amount,
			spot_entry_price, swap_entry_price, entry_basis_pct, status, opened_at
		 FROM hedge_position WHERE status = 'open' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.HedgePosition, 0) // 初始化为空数组，JSON 返回 [] 而非 null
	for rows.Next() {
		var p model.HedgePosition
		if err := rows.Scan(&p.ID, &p.Symbol, &p.SpotExchange, &p.SwapExchange,
			&p.SpotAmount, &p.SwapAmount, &p.SpotEntryPrice, &p.SwapEntryPrice,
			&p.EntryBasisPct, &p.Status, &p.OpenedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FeeBySymbol 某币对的手续费合计（只统计 USDT 部分；持仓详情页用）
func (s *Service) FeeBySymbol(symbol string) (float64, error) {
	var sum float64
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(fee), 0) FROM trade_fill
		 WHERE symbol = ? AND (fee_currency = 'USDT' OR fee_currency = 'USD' OR fee_currency = '')`,
		symbol).Scan(&sum)
	return sum, err
}

// Fills 某币对的成交记录（持仓详情页“成交记录”页签）
func (s *Service) Fills(symbol string, limit int) ([]model.PositionFill, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(
		`SELECT id, position_id, symbol, exchange, market_type, side, price, amount,
			cost_usdt, fee, fee_currency, order_id, maker, traded_at
		 FROM trade_fill WHERE symbol = ? ORDER BY id DESC LIMIT ?`, symbol, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.PositionFill, 0)
	for rows.Next() {
		var f model.PositionFill
		var maker int
		if err := rows.Scan(&f.ID, &f.PositionID, &f.Symbol, &f.Exchange, &f.MarketType,
			&f.Side, &f.Price, &f.Amount, &f.CostUSDT, &f.Fee, &f.FeeCurrency,
			&f.OrderID, &maker, &f.TradedAt); err != nil {
			return nil, err
		}
		f.Maker = maker == 1
		out = append(out, f)
	}
	return out, rows.Err()
}

// Logs 查询最近的操作日志（前端日志页展示）
func (s *Service) Logs(limit int) ([]model.TradeLog, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(
		`SELECT id, time, module, level, action, symbol, message FROM trade_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.TradeLog, 0) // 初始化为空数组，JSON 返回 [] 而非 null
	for rows.Next() {
		var l model.TradeLog
		if err := rows.Scan(&l.ID, &l.Time, &l.Module, &l.Level, &l.Action, &l.Symbol, &l.Message); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// log 写一条交易模块日志
func (s *Service) log(level, action, symbol, message string) {
	s.db.Exec(`INSERT INTO trade_log(module, level, action, symbol, message) VALUES('trade', ?, ?, ?, ?)`,
		level, action, symbol, message)
}

// LogExternal 供其他模块（自动交易/预警）写日志到同一张表，前端统一展示
func (s *Service) LogExternal(module, level, action, symbol, message string) {
	s.db.Exec(`INSERT INTO trade_log(module, level, action, symbol, message) VALUES(?, ?, ?, ?, ?)`,
		module, level, action, symbol, message)
}

// actionName 动作中文名
func actionName(isOpen bool) string {
	if isOpen {
		return "建仓"
	}
	return "平仓"
}
