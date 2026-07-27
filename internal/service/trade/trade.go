// Package trade 模块 3：交易模块（建仓/平仓共用的对冲执行器）。
//
// 需求要点：
//   - 填入总量（组容量）与每笔原子交易的量（原子单位），拆成多轮执行；
//   - 优先腿是现货，对冲腿是合约（两腿对冲，保持 Delta 中性）；
//   - 粉尘处理：剩余价值低于阈值时一并带走；
//   - “牛吃草”：每下几笔原子单停顿一下看行情，不抢一秒；
//   - 下单引擎不自研：底层直接复用 ccxt 的 CreateOrder，
//     本模块只负责双腿协调、拆单、汇总与落库（持仓表/日志表）。
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

// execute 建仓/平仓的统一执行流程（方向由 isOpen 区分）
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
		fmt.Sprintf("开始%s：总量 %.2fU，原子 %.2fU", actionName(isOpen), req.TotalUSDT, req.AtomUSDT))

	// 建仓前设置合约杠杆（失败仅警告，不阻断）
	if isOpen {
		lev := int64(s.settings.GetInt(settings.KeyLeverage))
		if err := swapEx.SetLeverage(req.Symbol, lev); err != nil {
			s.log("warn", action, req.Symbol, fmt.Sprintf("设置杠杆 %dx 失败（继续下单）: %v", lev, err))
		}
	}

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

		// 第 1 腿（优先腿）：现货。建仓买、平仓卖
		spotSide := "buy"
		if !isOpen {
			spotSide = "sell"
		}
		spotOrder, err := spotEx.CreateMarketOrder("spot", req.Symbol, spotSide, amount)
		if err != nil {
			s.log("error", action, req.Symbol, fmt.Sprintf("第 %d 轮现货腿(%s)失败: %v", round, spotSide, err))
			return result, fmt.Errorf("现货腿下单失败: %w", err)
		}
		result.SpotLeg.Side = spotSide
		accumulateLeg(result.SpotLeg, spotOrder)

		// 第 2 腿（对冲腿）：合约。建仓空、平仓买（reduceOnly）
		swapSide := "sell"
		if !isOpen {
			swapSide = "buy"
		}
		swapOrder, err := s.createSwapOrder(swapEx, req.Symbol, swapSide, spotOrder.Filled, isOpen)
		if err != nil {
			// 合约腿失败 => 产生净敞口！尝试回滚现货腿（把刚成交的现货反向操作回去）
			s.log("error", action, req.Symbol,
				fmt.Sprintf("第 %d 轮合约腿失败: %v，尝试回滚现货腿 %.6f", round, err, spotOrder.Filled))
			if _, rbErr := spotEx.CreateMarketOrder("spot", req.Symbol, swapSide, spotOrder.Filled); rbErr != nil {
				s.log("error", action, req.Symbol, fmt.Sprintf("现货腿回滚也失败（存在净敞口，请人工处理）: %v", rbErr))
			} else {
				s.log("warn", action, req.Symbol, "现货腿已回滚，净敞口已消除")
			}
			return result, fmt.Errorf("合约腿下单失败（已尝试回滚现货）: %w", err)
		}
		result.SwapLeg.Side = swapSide
		accumulateLeg(result.SwapLeg, swapOrder)

		remainingUSDT -= roundUSDT
		s.log("info", action, req.Symbol,
			fmt.Sprintf("第 %d 轮成交：现货 %s %.6f @ %.6f，合约 %s %.6f @ %.6f",
				round, spotSide, spotOrder.Filled, spotOrder.AvgPrice, swapSide, swapOrder.Filled, swapOrder.AvgPrice))

		// 牛吃草：抬头看一眼（轮间停顿，低频工具 1 秒足够）
		if remainingUSDT > 0 {
			time.Sleep(1 * time.Second)
			// 更新现货价，避免拆单期间价格漂移导致数量失真
			if p, err := spotEx.FetchLastPrice("spot", req.Symbol); err == nil && p > 0 {
				spotPrice = p
			}
		}
	}

	// —— 落库：持仓表 ——
	if isOpen {
		basis := 0.0
		if result.SpotLeg.AvgPrice > 0 {
			basis = (result.SwapLeg.AvgPrice - result.SpotLeg.AvgPrice) / result.SpotLeg.AvgPrice * 100
		}
		if err := s.insertPosition(req, result, basis); err != nil {
			s.log("error", action, req.Symbol, "持仓写入数据库失败: "+err.Error())
		}
	} else if positionID > 0 {
		if err := s.closePosition(positionID); err != nil {
			s.log("error", action, req.Symbol, "持仓状态更新失败: "+err.Error())
		}
	}

	result.Success = true
	result.Message = fmt.Sprintf("%s完成：现货 %.6f @ %.6f，合约 %.6f @ %.6f",
		actionName(isOpen),
		result.SpotLeg.Amount, result.SpotLeg.AvgPrice,
		result.SwapLeg.Amount, result.SwapLeg.AvgPrice)
	s.log("info", action, req.Symbol, result.Message)
	return result, nil
}

// createSwapOrder 合约腿下单；平仓时附带 reduceOnly 确保只减仓不开新仓
func (s *Service) createSwapOrder(swapEx *exchange.Exchange, symbol, side string, amount float64, isOpen bool) (*exchange.MarketOrderResult, error) {
	amount = swapEx.AmountToPrecision("swap", symbol, amount)
	if amount <= 0 {
		return nil, fmt.Errorf("合约数量精度换算后为 0")
	}
	// reduceOnly 由 ccxt 参数透传；市价平仓单附加该参数防止误开反向仓
	if !isOpen {
		return swapEx.CreateMarketOrderWithParams(symbol, side, amount, map[string]any{"reduceOnly": true})
	}
	return swapEx.CreateMarketOrder("swap", symbol, side, amount)
}

// accumulateLeg 把一笔成交累计到腿结果中（数量累加、均价按量加权）
func accumulateLeg(leg *model.LegResult, order *exchange.MarketOrderResult) {
	prevCost := leg.AvgPrice * leg.Amount
	leg.Amount += order.Filled
	leg.CostUSDT += order.Cost
	if leg.Amount > 0 {
		leg.AvgPrice = (prevCost + order.Cost) / leg.Amount
	}
	leg.OrderIDs = append(leg.OrderIDs, order.OrderID)
}

// insertPosition 建仓成功后写入持仓表
func (s *Service) insertPosition(req model.TradeRequest, result *model.TradeResult, basisPct float64) error {
	_, err := s.db.Exec(
		`INSERT INTO hedge_position(symbol, spot_exchange, swap_exchange, spot_amount, swap_amount,
			spot_entry_price, swap_entry_price, entry_basis_pct, status) VALUES(?,?,?,?,?,?,?,?, 'open')`,
		req.Symbol, result.SpotLeg.Exchange, result.SwapLeg.Exchange,
		result.SpotLeg.Amount, result.SwapLeg.Amount,
		result.SpotLeg.AvgPrice, result.SwapLeg.AvgPrice, basisPct)
	return err
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
