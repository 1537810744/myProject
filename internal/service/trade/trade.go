// 【阅读顺序 10】模块 3：交易模块（建仓/平仓的统一入口）。
// 对外提供 Open/Close 两个入口，内部是“拆单循环”，每轮双腿的执行交给 engine.go 的引擎。
// 拆两个文件是【职责单一】：本文件管“整体编排”（金额怎么切、切几轮、结果写库），
// engine.go 管“一笔怎么成交”。
// 语法点预览：多返回值、math.Min、for 条件循环、continue/break/return、fmt.Sprintf、
// %w 错误包装、time.Sleep、append、bool→int 转换。
package trade

// import 导入用到的包。
import (
	"fmt"  // 格式化
	"math" // 数学：math.Min
	"time" // 时间

	"deltacrypto/internal/database"         // 数据库
	"deltacrypto/internal/exchange"         // 交易所抽象层
	"deltacrypto/internal/model"            // 数据结构
	"deltacrypto/internal/service/settings" // 设置模块
)

// Service 交易模块服务。
type Service struct {
	db       *database.DB      // 数据库
	hub      *exchange.Hub     // 交易所连接管理器
	settings *settings.Service // 参数中心
}

// New 创建交易模块。
func New(db *database.DB, hub *exchange.Hub, settings *settings.Service) *Service {
	return &Service{db: db, hub: hub, settings: settings}
}

// Open 建仓：买现货（gate）+ 空合约（binance）。成功后写持仓表。
// positionID 传 0：建仓还没有持仓 id。
func (s *Service) Open(req model.TradeRequest) (*model.TradeResult, error) {
	return s.execute(req, true, 0) // 转发给统一执行函数：isOpen=true, positionID=0
}

// Close 平仓：卖现货 + 平空合约（reduceOnly）。成功后把持仓置 closed。
// positionID 用于更新持仓表；传 0 表示纯手动平仓（不更新表）。
func (s *Service) Close(req model.TradeRequest, positionID int64) (*model.TradeResult, error) {
	return s.execute(req, false, positionID) // 转发：isOpen=false
}

// execute 建仓/平仓的统一执行流程（isOpen 区分方向）。
func (s *Service) execute(req model.TradeRequest, isOpen bool, positionID int64) (*model.TradeResult, error) {
	spotEx, err := s.hub.Spot() // 取现货腿连接
	if err != nil {
		return nil, err
	}
	swapEx, err := s.hub.Swap() // 取合约腿连接
	if err != nil {
		return nil, err
	}

	// 参数兜底：调用方可以只传币对，金额类参数从设置模块取全局默认。
	if req.TotalUSDT <= 0 { // 组容量没传
		req.TotalUSDT = s.settings.GetFloat(settings.KeyGroupSizeUSDT) // 用默认 50U
	}
	if req.AtomUSDT <= 0 {
		req.AtomUSDT = s.settings.GetFloat(settings.KeyAtomSizeUSDT) // 用默认 5U
	}
	if req.DustUSDT <= 0 {
		req.DustUSDT = s.settings.GetFloat(settings.KeyDustUSDT) // 用默认 5U
	}

	// —— 幂等防重（风险控制）——
	// 调用方传了 RequestID：先在 request_log 表登记。如果这个 id 已经登记过
	// （INSERT OR IGNORE 受影响行数 = 0），说明同一次业务请求已经被处理过
	// （典型场景：网络超时后客户端用同一个 id 重发），直接返回"已处理"，绝不再下一单。
	if req.RequestID != "" {
		res, err := s.db.Exec(
			`INSERT OR IGNORE INTO request_log(request_id, action, symbol) VALUES(?,?,?)`,
			req.RequestID, req.Action, req.Symbol)
		if err != nil {
			s.log("error", req.Action, req.Symbol, "幂等登记失败: "+err.Error())
		} else if n, _ := res.RowsAffected(); n == 0 { // 影响 0 行 = 这个 id 之前已登记过
			s.log("warn", req.Action, req.Symbol, fmt.Sprintf("重复请求 %s 已忽略（之前已处理）", req.RequestID))
			return &model.TradeResult{
				Success:   true,
				Message:   "重复请求已忽略（该 request_id 之前已处理）",
				Timestamp: time.Now(),
			}, nil
		}
	}

	action := "close" // 默认动作（日志用）
	if isOpen {
		action = "open" // 建仓
	}

	// —— 模拟盘（dry_run）——
	// 设置 dry_run=1 时不下真单：模拟成交并记日志，不写持仓/成交表。
	// 用途：在不动真金白银的情况下验证策略逻辑和自动交易循环（上线前先跑它）。
	if s.settings.DryRun() {
		s.log("warn", action, req.Symbol,
			fmt.Sprintf("【模拟盘】%s %s：计划成交 %.2fU（原子 %.2fU），未下真单",
				actionName(isOpen), req.Symbol, req.TotalUSDT, req.AtomUSDT))
		return &model.TradeResult{
			Success:   true,
			Message:   "模拟盘：未下真单",
			Timestamp: time.Now(),
		}, nil
	}

	s.log("info", action, req.Symbol, // 写一条开始日志（s.log 方法见下）
		fmt.Sprintf("开始%s：总量 %.2fU，原子 %.2fU，下单方式 %s（前%d档，最多追价%d次）",
			actionName(isOpen), req.TotalUSDT, req.AtomUSDT,
			s.settings.Get(settings.KeyOrderMethod),
			s.settings.GetInt(settings.KeyOrderbookLevel),
			s.settings.GetInt(settings.KeyMaxChaseCount)))

	// 建仓前设置合约杠杆。失败只警告不阻断——倍数不对只是收益/保证金占用变化。
	if isOpen {
		lev := int64(s.settings.GetInt(settings.KeyLeverage)) // 杠杆从设置读
		// int64(...) 类型转换：GetInt 返回 int，而 SetLeverage 的杠杆参数要求 int64，
		// 类型对不上会编译报错，所以转一下。Go 的类型转换写法 = “目标类型(值)”。
		if err := swapEx.SetLeverage(req.Symbol, lev); err != nil {
			s.log("warn", action, req.Symbol, fmt.Sprintf("设置杠杆 %dx 失败（继续下单）: %v", lev, err))
		}
	}

	// —— 组装交易引擎：全部参数来自设置模块（前端设置页可调）——
	// 注意 logf 参数是个【回调函数】——引擎不直接写库，把日志内容“交回”给
	// trade.Service 去写。为什么？引擎是纯逻辑，不该依赖数据库；把副作用（写库）
	// 通过回调注入，引擎保持可测试、可复用。这叫【依赖反转】。
	// EngineConfig{...} 是【结构体字面量】：用花括号给结构体的字段逐个赋值。
	// 为什么这里要填 3 个字段？—— 因为 engine.go 的 Engine 结构体里存了 cfg 这个
	// EngineConfig，这些字段决定引擎“什么算超出敞口、失败重试几次、隔多久看一次单”。
	// 每个值都从设置模块读（前端设置页可改、即时生效）。
	engine := NewEngine(spotEx, swapEx,
		EngineConfig{
			MaxNetExposure: s.settings.GetFloat(settings.KeyMaxNetExposure), // 最大净敞口
			MaxRetry:       s.settings.GetInt(settings.KeyMaxRetry),         // 最大重试
			PollInterval:   1 * time.Second,                                 // 订单轮询间隔：低频工具 1 秒足够
		},
		LegConfig{ // LegConfig 结构体字面量（单腿配置）
			OrderMethod:  s.settings.Get(settings.KeyOrderMethod),          // maker / taker
			Level:        s.settings.GetInt(settings.KeyOrderbookLevel),    // 挂单档位
			MaxChase:     s.settings.GetInt(settings.KeyMaxChaseCount),     // 最大追价次数
			ChaseToTaker: s.settings.GetInt(settings.KeyChaseToTaker) == 1, // 超限转市价
		},
		s.log, // 把本模块的 log 方法当回调传进去
	)

	// 以现货最新价折算币数量（总量是 U，下单要的是币数量）。
	spotPrice, err := spotEx.FetchLastPrice("spot", req.Symbol)
	if err != nil || spotPrice <= 0 { // 拉价失败 或 价格异常
		return nil, fmt.Errorf("获取 %s 现货价格失败: %v", req.Symbol, err)
	}

	result := &model.TradeResult{ // 初始化结果对象（两条腿先占位）
		SpotLeg:   &model.LegResult{Exchange: spotEx.ID(), MarketType: "spot"},
		SwapLeg:   &model.LegResult{Exchange: swapEx.ID(), MarketType: "swap"},
		Timestamp: time.Now(),
	}
	// 攒所有成交明细，最后统一写 trade_fill 表。为什么攒着最后写？
	// —— 成交记录要关联持仓 id，而持仓 id 要等建仓完成后才知道，只能先攒、落库阶段写。
	var allFills []Fill // 空切片（var 声明的 nil 切片可以 append）

	// 双腿方向：建仓 = 现货买 + 合约空；平仓 = 现货卖 + 合约买回（平空）。
	spotSide, swapSide := "buy", "sell" // := 同时声明两个变量
	if !isOpen {                        // 平仓时
		spotSide, swapSide = "sell", "buy" // 反向
	}

	// —— 拆单循环（牛吃草：吃几口，抬头看一眼）——
	// 为什么要拆单？—— 总量一次性下单太大，价格滑点/成交深度都受影响；拆成原子单位
	// 逐轮成交，每轮之间看一眼最新价，数量才贴近目标金额。
	remainingUSDT := req.TotalUSDT // 剩余待成交金额
	round := 0                     // 轮次计数
	for remainingUSDT > 0 {        // for 条件循环（= 其它语言的 while）
		// 本轮计划量 = min(原子单位, 剩余)，并做粉尘处理（抽成纯函数，见文件末尾）。
		// 粉尘逻辑：本轮执行后剩下的零头低于粉尘阈值，就一并带走。
		// 例：总 52U、原子 5U → 5×10=50 剩 2U。单独再下 2U 不划算（可能达不到
		// 交易所最小下单量），干脆最后一轮从 5U 加到 7U 一次清空。
		roundUSDT := computeRoundUSDT(req.AtomUSDT, remainingUSDT, req.DustUSDT)
		round++ // round = round + 1 的简写

		// 金额 → 币数量，并按交易所精度截断（多一位小数会被拒单）。
		amount := spotEx.AmountToPrecision("spot", req.Symbol, roundUSDT/spotPrice)
		if amount <= 0 { // 精度截断后为 0（币太贵/金额太小）
			return result, fmt.Errorf("第 %d 轮数量精度换算后为 0，终止", round)
		}

		// 引擎执行本轮双腿：现货腿先走，合约腿实时跟进现货的实际成交量。
		spotOut, swapOut, err := engine.HedgeOnce(req.Symbol, spotSide, swapSide, amount, !isOpen)
		if spotOut != nil { // 现货腿有结果
			mergeLegOutcome(result.SpotLeg, spotOut)      // 合并进汇总
			allFills = append(allFills, spotOut.Fills...) // 追加成交明细（... 展开切片）
		}
		if swapOut != nil {
			mergeLegOutcome(result.SwapLeg, swapOut)
			allFills = append(allFills, swapOut.Fills...)
		}
		if err != nil { // 引擎返回错误
			// 引擎内部已写明失败原因；这里直接终止整组执行。
			// “失败即停”原则：绝不带着错误继续拆下一轮。
			return result, fmt.Errorf("第 %d 轮执行失败: %w", round, err)
		}

		remainingUSDT -= roundUSDT // 扣掉本轮已成交金额
		s.log("info", action, req.Symbol,
			fmt.Sprintf("第 %d 轮对冲完成：现货成交 %.6f，合约成交 %.6f",
				round, spotOut.Amount, swapOut.Amount))

		// 牛吃草：抬头看一眼——轮间停 1 秒，并用最新价刷新折算基准。
		if remainingUSDT > 0 { // 还有剩余才停（最后一轮不用等）
			time.Sleep(1 * time.Second) // 睡 1 秒（给市场一点时间消化，也防连珠炮下单被限频）
			// 为什么刷新价格？总量折算成币数量用的是开头的价，拆单跨好几秒，
			// 价格已变还按旧价折算会买多/买少。每轮前拿最新价，数量才准。
			if p, err := spotEx.FetchLastPrice("spot", req.Symbol); err == nil && p > 0 {
				spotPrice = p // 更新折算基准价
			}
		}
	}

	// —— 落库：持仓表 + 成交记录表 ——
	var savedPositionID = positionID // 默认用传入的 positionID
	if isOpen {                      // 建仓：要写持仓表
		basis := 0.0                     // 入场基差
		if result.SpotLeg.AvgPrice > 0 { // 现货均价有效才算（防除 0）
			// 基差 =（合约均价 - 现货均价）/ 现货均价 × 100。
			// 这是“入场基差”，autotrade 的 slow sell 用它判断：
			// “当前基差 < 入场基差”说明价差在收敛，平仓顺带赚差价。
			basis = (result.SwapLeg.AvgPrice - result.SpotLeg.AvgPrice) / result.SpotLeg.AvgPrice * 100
		}
		id, err := s.insertPosition(req, result, basis) // 写持仓表，拿到新 id
		if err != nil {
			s.log("error", action, req.Symbol, "持仓写入数据库失败: "+err.Error())
		} else {
			savedPositionID = id // 用新 id 关联成交记录
		}
	} else if positionID > 0 { // 平仓且传了 id
		if err := s.closePosition(positionID); err != nil { // 把持仓置 closed
			s.log("error", action, req.Symbol, "持仓状态更新失败: "+err.Error())
		}
	}
	// 成交明细写表（详情页“成交记录”页签的数据来源）。
	if err := s.saveFills(savedPositionID, req.Symbol, allFills); err != nil {
		s.log("error", action, req.Symbol, "成交记录写入失败: "+err.Error())
	}
	// ⚠️ 上面所有写库失败都只 log 不 return error——为什么？
	// 币已经真实成交了，只是“记账失败”。如果因此把整单标成失败，调用方可能重复下单
	// （再平一次），更糟。所以：交易结果照常成功，记账失败用日志让人发现。

	result.Success = true // 标记成功
	result.Message = fmt.Sprintf("%s完成：现货 %.6f @ %.6f，合约 %.6f @ %.6f",
		actionName(isOpen),
		result.SpotLeg.Amount, result.SpotLeg.AvgPrice,
		result.SwapLeg.Amount, result.SwapLeg.AvgPrice)
	s.log("info", action, req.Symbol, result.Message)
	return result, nil
}

// mergeLegOutcome 把引擎“一轮”的腿结果合并进 TradeResult 的腿汇总。
// 一个持仓要拆多轮执行，每轮都有结果，这里累加成整条腿的汇总：总数量、加权均价、订单号。
func mergeLegOutcome(leg *model.LegResult, out *LegOutcome) {
	prevCost := leg.AvgPrice * leg.Amount // 先算出“已有总成本”，才能算加权
	leg.Amount += out.Amount              // 累加数量
	leg.CostUSDT += out.CostUSDT          // 累加成本
	if leg.Amount > 0 {
		leg.AvgPrice = (prevCost + out.CostUSDT) / leg.Amount // 加权均价 = 累计成本/累计数量
	}
	leg.Side = out.Fills[len(out.Fills)-1].Side // 取最后一笔的方向
	for _, f := range out.Fills {               // 追加所有订单号
		leg.OrderIDs = append(leg.OrderIDs, f.OrderID)
	}
}

// insertPosition 建仓成功后写持仓表，返回持仓 ID（成交记录要关联它）。
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
	return res.LastInsertId() // 取自增主键（SQLite 支持）
}

// saveFills 把一轮的成交明细批量写进 trade_fill 表。
func (s *Service) saveFills(positionID int64, symbol string, fills []Fill) error {
	for _, f := range fills { // 遍历每笔成交
		maker := 0 // 默认 0（Taker）
		if f.Maker {
			maker = 1 // bool → 0/1：SQLite 没有真正的布尔类型
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

// closePosition 平仓成功后把持仓置为 closed。
func (s *Service) closePosition(positionID int64) error {
	_, err := s.db.Exec(
		`UPDATE hedge_position SET status = 'closed', closed_at = CURRENT_TIMESTAMP WHERE id = ?`, positionID)
	return err
}

// OpenPositions 查询当前全部 open 持仓（账户/预警/自动交易模块共用）。
func (s *Service) OpenPositions() ([]model.HedgePosition, error) {
	rows, err := s.db.Query(
		`SELECT id, symbol, spot_exchange, swap_exchange, spot_amount, swap_amount,
			spot_entry_price, swap_entry_price, entry_basis_pct, status, opened_at
		 FROM hedge_position WHERE status = 'open' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()                    // rows 用完必须关（defer 保证）
	out := make([]model.HedgePosition, 0) // 空数组而非 nil：JSON 返回 []
	for rows.Next() {                     // 游标推进
		var p model.HedgePosition
		if err := rows.Scan(&p.ID, &p.Symbol, &p.SpotExchange, &p.SwapExchange,
			&p.SpotAmount, &p.SwapAmount, &p.SpotEntryPrice, &p.SwapEntryPrice,
			&p.EntryBasisPct, &p.Status, &p.OpenedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err() // 遍历完再查一次错误
}

// FeeBySymbol 某币对的手续费合计（只统计 USDT 部分；持仓详情页用）。
// 为什么只统计 USDT？—— 不同币种手续费不能直接相加（比如 ETH 手续费是 ETH），
// 要算“U 账”只能统计 USDT 计价的那些。
func (s *Service) FeeBySymbol(symbol string) (float64, error) {
	var sum float64 // 存放合计
	// COALESCE(SUM(fee), 0)：没有匹配行时 SUM 返回 NULL，COALESCE 转成 0，避免 Scan 报错。
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(fee), 0) FROM trade_fill
		 WHERE symbol = ? AND (fee_currency = 'USDT' OR fee_currency = 'USD' OR fee_currency = '')`,
		symbol).Scan(&sum)
	return sum, err
}

// Fills 某币对的成交记录（详情页“成交记录”页签）。
func (s *Service) Fills(symbol string, limit int) ([]model.PositionFill, error) {
	if limit <= 0 {
		limit = 200 // 默认 200 条，防止一次拉太多
	}
	rows, err := s.db.Query(
		`SELECT id, position_id, symbol, exchange, market_type, side, price, amount,
			cost_usdt, fee, fee_currency, order_id, maker, traded_at
		 FROM trade_fill WHERE symbol = ? ORDER BY id DESC LIMIT ?`, symbol, limit)
	if err != nil {
		return nil, err
	}
	// 同 trade.go 上面的列表查询：defer rows.Close() 释放游标连接；
	// make([]T, 0) 空切片 → JSON 输出 []（不是 null，前端遍历才安全）。
	defer rows.Close()
	out := make([]model.PositionFill, 0)
	for rows.Next() { // rows.Next()：游标还有没有下一行
		var f model.PositionFill
		var maker int // SQLite 里 maker 是整数
		if err := rows.Scan(&f.ID, &f.PositionID, &f.Symbol, &f.Exchange, &f.MarketType,
			&f.Side, &f.Price, &f.Amount, &f.CostUSDT, &f.Fee, &f.FeeCurrency,
			&f.OrderID, &maker, &f.TradedAt); err != nil {
			return nil, err
		}
		f.Maker = maker == 1 // int 转回 bool
		out = append(out, f)
	}
	// rows.Err()：遍历结束后再查一次中途有没有出错（比如连接断开）——只查循环里的
	// Scan 错误不够，遍历过程中的错误存在 rows 里，必须最后取出来。游标模式的标准收尾。
	return out, rows.Err()
}

// Logs 查询最近的操作日志（前端日志页展示）。
func (s *Service) Logs(limit int) ([]model.TradeLog, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(
		`SELECT id, time, module, level, action, symbol, message FROM trade_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	// 同一个游标套路：defer rows.Close()（释放连接）、make([]T, 0)（JSON 输出 []）、
	// rows.Next() 逐行推进、rows.Scan 填行数据、最后 return rows.Err() 检查遍历错误。
	defer rows.Close()
	out := make([]model.TradeLog, 0)
	for rows.Next() {
		var l model.TradeLog
		if err := rows.Scan(&l.ID, &l.Time, &l.Module, &l.Level, &l.Action, &l.Symbol, &l.Message); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	// 和上面一样：rows.Err() 检查遍历中途是否有错误（连接断开等），是游标模式的收尾。
	return out, rows.Err()
}

// log 写一条交易模块日志。
// 忽略 Exec 错误：日志写失败不该中断主流程——“没记上日志”是小事。
func (s *Service) log(level, action, symbol, message string) {
	s.db.Exec(`INSERT INTO trade_log(module, level, action, symbol, message) VALUES('trade', ?, ?, ?, ?)`,
		level, action, symbol, message)
}

// LogExternal 供其他模块（自动交易/预警）写日志到同一张表，前端统一展示。
// 为什么由交易模块提供日志入口？—— 写日志的 SQL 只有一份（在这），其它模块一行调用即可。
func (s *Service) LogExternal(module, level, action, symbol, message string) {
	s.db.Exec(`INSERT INTO trade_log(module, level, action, symbol, message) VALUES(?, ?, ?, ?, ?)`,
		module, level, action, symbol, message)
}

// computeRoundUSDT 计算本轮下单金额（含粉尘处理）。纯函数——输入相同输出就相同，
// 不碰外部状态，所以单测最方便。见 execute() 拆单循环里的调用。
func computeRoundUSDT(atom, remaining, dust float64) float64 {
	round := math.Min(atom, remaining) // 本轮计划量 = min(原子单位, 剩余)
	if rest := remaining - round; rest > 0 && rest < dust {
		round = remaining // 剩余零头低于粉尘阈值：本轮全下，一次清空
	}
	return round
}

// CloseAllPositions 紧急全平：把当前所有 open 持仓全部平掉（"杀开关"/一键清仓）。
// 用途：熔断触发后，或用户手动决定立即退出全部仓位时的应急操作。
// 逐仓平，返回每仓的结果描述；单仓失败不阻断其它仓（记到返回列表里）。
func (s *Service) CloseAllPositions() []string {
	positions, err := s.OpenPositions()
	if err != nil {
		return []string{"读取持仓失败: " + err.Error()}
	}
	if len(positions) == 0 {
		return []string{"当前没有持仓，无需平仓"}
	}
	var out []string
	for _, p := range positions {
		totalUSDT := p.SpotAmount * p.SpotEntryPrice
		if totalUSDT <= 0 {
			totalUSDT = s.settings.GetFloat(settings.KeyGroupSizeUSDT)
		}
		result, err := s.Close(model.TradeRequest{
			Symbol:    p.Symbol,
			Action:    "close",
			TotalUSDT: totalUSDT,
		}, p.ID)
		if err != nil {
			out = append(out, fmt.Sprintf("【全平失败】%s: %v", p.Symbol, err))
			continue
		}
		out = append(out, fmt.Sprintf("【全平】%s: %s", p.Symbol, result.Message))
	}
	return out
}

// actionName 动作的中文名（日志/邮件正文用）。
func actionName(isOpen bool) string {
	if isOpen { // 判断布尔
		return "建仓"
	}
	return "平仓"
}
