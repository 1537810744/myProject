// 【阅读顺序 09】本文件是交易模块的核心：双腿对冲执行引擎。
//
// 目的（对应《第一次更新》第 1 条）：
// 原来的交易引擎只有“两腿市价单”，过于简陋。参考成熟方案，升级为：
//
//	腿配置：优先腿（现货）先执行，对冲腿（合约）在优先腿成交后【实时跟进】；
//	下单方式：Maker（限价单，挂在盘口前 N 档）或 Taker（市价单）；
//	掉档追价：挂单掉出前 N 档 -> 自动撤单、按第 1 档重挂；
//	最大追价次数：超过后自动转 Taker 市价吃单（保证成交）；
//	最大净敞口：两腿成交量差超过阈值 -> 停止执行并告警；
//	最大重试：下单类失败最多重试 N 次。
//
// 阅读提示：
//   - 对外入口是 HedgeOnce（执行一轮双腿），trade.go 的拆单循环逐轮调用它；
//   - execLeg 是单腿执行的调度（按配置走 Maker 或 Taker）；
//   - execMaker 是 Maker 核心循环（挂单 -> 轮询 -> 掉档追价 -> 超限转 Taker）。
package trade

import (
	"fmt"
	"math"
	"time"

	"deltacrypto/internal/exchange"
)

// EngineConfig 引擎整体配置（对应参考图的「执行控制」区）
type EngineConfig struct {
	MaxNetExposure float64 // 最大净敞口（币数量），0 = 不限；超出自动停止并告警
	MaxRetry       int     // 下单/查询失败的最大重试次数
	PollInterval   time.Duration // 订单状态轮询间隔（低频工具 1 秒足够）
}

// LegConfig 单条腿的执行配置（对应参考图的「腿A/腿B配置」区）。
// 当前实现两腿共用一套配置（需求是“参考”，保持简单）；
// 结构上预留了双腿独立配置的扩展空间。
type LegConfig struct {
	OrderMethod  string // 下单方式：maker / taker
	Level        int    // 盘口档位：Maker 挂单落在前 N 档，掉出则追价
	MaxChase     int    // 最大追价次数：超过后按 ChaseToTaker 处理
	ChaseToTaker bool   // 追价超限后是否转 Taker（市价单保证成交）
}

// Fill 一笔实际成交明细（用于写 trade_fill 表 + 聚合腿均价）
type Fill struct {
	Exchange    string    // 交易所
	MarketType  string    // spot / swap
	Side        string    // buy / sell
	Price       float64   // 成交均价
	Amount      float64   // 成交币数量
	CostUSDT    float64   // 名义价值
	Fee         float64   // 手续费
	FeeCurrency string    // 手续费币种
	OrderID     string    // 订单号
	Maker       bool      // true=Maker 成交 / false=Taker 成交
	Time        time.Time // 成交时间
}

// LegOutcome 单条腿的执行结果（成交汇总 + 明细）
type LegOutcome struct {
	Fills    []Fill  // 全部成交明细
	Amount   float64 // 总成交币数量
	AvgPrice float64 // 加权均价
	CostUSDT float64 // 总名义价值
	Fee      float64 // 手续费合计（同一腿通常同一币种）
	FeeCurrency string
}

// Engine 双腿对冲执行引擎：现货=优先腿，合约=对冲腿
type Engine struct {
	spot *exchange.Exchange // 优先腿（现货）
	swap *exchange.Exchange // 对冲腿（合约）
	cfg  EngineConfig
	leg  LegConfig
	// logf 日志回调：引擎不直接写库，由 trade.Service 注入（统一进 trade_log 表）
	logf func(level, action, symbol, msg string)
}

// NewEngine 创建引擎
func NewEngine(spot, swap *exchange.Exchange, cfg EngineConfig, leg LegConfig,
	logf func(level, action, symbol, msg string)) *Engine {
	return &Engine{spot: spot, swap: swap, cfg: cfg, leg: leg, logf: logf}
}

// HedgeOnce 执行一轮双腿对冲（优先腿先执行，对冲腿实时跟进优先腿的成交量）。
//
// 参数：
//
//	symbol     内部币对（BTC/USDT）
//	spotSide   现货腿方向：建仓 buy / 平仓 sell
//	swapSide   合约腿方向：建仓 sell（开空）/ 平仓 buy（平空）
//	amount     本轮现货腿计划成交的币数量
//	reduceOnly 平仓时合约腿附加 reduceOnly（只减仓不开新仓）
//
// 返回两腿成交结果；若双腿成交量差超过最大净敞口，返回错误（敞口情况已写日志，需人工关注）。
func (e *Engine) HedgeOnce(symbol, spotSide, swapSide string, amount float64, reduceOnly bool) (spotOut, swapOut *LegOutcome, err error) {
	// ---------- 第 1 步：优先腿（现货） ----------
	spotOut, err = e.execWithRetry(e.spot, "spot", symbol, spotSide, amount, false)
	if err != nil {
		return spotOut, nil, fmt.Errorf("优先腿（现货 %s）失败: %w", spotSide, err)
	}
	if spotOut.Amount <= 0 {
		return spotOut, nil, fmt.Errorf("优先腿未成交任何数量")
	}

	// ---------- 第 2 步：对冲腿（合约）实时跟进 ----------
	// 实时对冲的含义：优先腿实际成交多少，对冲腿就立即对冲多少，不多不少
	swapOut, err = e.execWithRetry(e.swap, "swap", symbol, swapSide, spotOut.Amount, reduceOnly)
	if err != nil {
		e.logf("error", "hedge", symbol,
			fmt.Sprintf("对冲腿（合约 %s %.6f）失败: %v —— 存在净敞口 %.6f 币，请人工处理！",
				swapSide, spotOut.Amount, err, spotOut.Amount))
		return spotOut, nil, fmt.Errorf("对冲腿（合约 %s）失败，净敞口 %.6f: %w", swapSide, spotOut.Amount, err)
	}

	// ---------- 第 3 步：净敞口检查 ----------
	// 两腿都完成，比较成交量差（部分成交/精度损失可能造成微小差异）
	exposure := math.Abs(spotOut.Amount - swapOut.Amount)
	if e.cfg.MaxNetExposure > 0 && exposure > e.cfg.MaxNetExposure {
		e.logf("error", "exposure", symbol,
			fmt.Sprintf("净敞口 %.6f 超过上限 %.6f，自动停止，请人工平仓对齐！", exposure, e.cfg.MaxNetExposure))
		return spotOut, swapOut, fmt.Errorf("净敞口 %.6f 超过上限 %.6f，执行已停止", exposure, e.cfg.MaxNetExposure)
	}
	if exposure > 0 {
		e.logf("warn", "exposure", symbol, fmt.Sprintf("存在微小净敞口 %.8f（精度差），继续执行", exposure))
	}
	return spotOut, swapOut, nil
}

// execWithRetry 单腿执行入口：失败按 MaxRetry 重试（网络抖动/交易所限流的容错）
func (e *Engine) execWithRetry(ex *exchange.Exchange, marketType, symbol, side string, amount float64, reduceOnly bool) (*LegOutcome, error) {
	var err error
	var out *LegOutcome
	maxRetry := e.cfg.MaxRetry
	if maxRetry < 1 {
		maxRetry = 1
	}
	for attempt := 1; attempt <= maxRetry; attempt++ {
		out, err = e.execLeg(ex, marketType, symbol, side, amount, reduceOnly)
		if err == nil {
			return out, nil
		}
		e.logf("warn", "retry", symbol, fmt.Sprintf("%s 腿第 %d/%d 次执行失败: %v", marketType, attempt, maxRetry, err))
		time.Sleep(e.cfg.PollInterval)
	}
	return nil, err
}

// execLeg 单腿执行调度：按配置走 Taker（市价）或 Maker（限价追价）
func (e *Engine) execLeg(ex *exchange.Exchange, marketType, symbol, side string, amount float64, reduceOnly bool) (*LegOutcome, error) {
	if e.leg.OrderMethod == "taker" {
		return e.execTaker(ex, marketType, symbol, side, amount, reduceOnly)
	}
	return e.execMaker(ex, marketType, symbol, side, amount, reduceOnly)
}

// execTaker 市价单执行：一次下单，直接成交（原来的简陋方式，保留为可选项与兜底）
func (e *Engine) execTaker(ex *exchange.Exchange, marketType, symbol, side string, amount float64, reduceOnly bool) (*LegOutcome, error) {
	amount = ex.AmountToPrecision(marketType, symbol, amount)
	if amount <= 0 {
		return nil, fmt.Errorf("数量精度换算后为 0")
	}
	var order *exchange.MarketOrderResult
	var err error
	if reduceOnly {
		order, err = ex.CreateMarketOrderWithParams(symbol, side, amount, map[string]any{"reduceOnly": true})
	} else {
		order, err = ex.CreateMarketOrder(marketType, symbol, side, amount)
	}
	if err != nil {
		return nil, err
	}
	out := &LegOutcome{Amount: order.Filled, AvgPrice: order.AvgPrice, CostUSDT: order.Cost}
	out.Fills = append(out.Fills, Fill{
		Exchange: ex.ID(), MarketType: marketType, Side: side,
		Price: order.AvgPrice, Amount: order.Filled, CostUSDT: order.Cost,
		OrderID: order.OrderID, Maker: false, Time: time.Now(),
	})
	return out, nil
}

// execMaker Maker 核心循环：挂单 -> 轮询成交 -> 掉档追价 -> 超限转 Taker。
//
// 对应参考图行为：
//
//	盘口档位 N：限价单挂在第 N 档价格（排队列中，不完全贴最优价，兼顾成交率与价格优势）；
//	掉出前 N 档自动追价到第 1 档：行情走动后我的价格不再是前 N 档，撤单按第 1 档重挂；
//	最大追价次数：超过后转 Taker（市价单保证成交，避免一直挂不到）。
func (e *Engine) execMaker(ex *exchange.Exchange, marketType, symbol, side string, amount float64, reduceOnly bool) (*LegOutcome, error) {
	out := &LegOutcome{FeeCurrency: "USDT"}
	remaining := amount // 剩余待成交数量
	chase := 0          // 已追价次数

	for remaining > 0 {
		// 数量按交易所精度截断；已为 0 说明剩余是粉尘，直接结束
		amountToPlace := ex.AmountToPrecision(marketType, symbol, remaining)
		if amountToPlace <= 0 {
			break
		}

		// 第 1 步：取盘口第 N 档价格挂单（买单看 bids，卖单看 asks）
		price, err := ex.FetchLevelPrice(marketType, symbol, side, e.leg.Level)
		if err != nil {
			return out, fmt.Errorf("读取盘口失败: %w", err)
		}
		orderID, err := ex.CreateLimitOrder(marketType, symbol, side, amountToPlace, price)
		if err != nil {
			return out, fmt.Errorf("挂限价单失败: %w", err)
		}
		e.logf("info", "order_placed", symbol,
			fmt.Sprintf("%s 腿 Maker 挂单 %s %.6f @ %.8f（前%d档）", marketType, side, amountToPlace, price, e.leg.Level))

		// 第 2 步：轮询订单，直到成交 / 掉档追价
		filledThisOrder := false
		for !filledThisOrder {
			time.Sleep(e.cfg.PollInterval)

			status, err := ex.FetchOrderStatus(marketType, symbol, orderID)
			if err != nil {
				// 查单失败不致命（可能订单刚撤），跳出重挂
				e.logf("warn", "order_query", symbol, fmt.Sprintf("查询订单 %s 失败（将重新挂单）: %v", orderID, err))
				break
			}

			// 全量成交 -> 该单结束
			if status.Filled >= amountToPlace || status.Status == "closed" {
				e.recordFill(out, ex, marketType, side, status.AvgPrice, status.Filled, status.Fee, status.FeeCurrency, orderID, true)
				remaining -= status.Filled
				filledThisOrder = true
				continue
			}

			// 未全量成交 -> 检查是否掉出前 N 档（需要追价）
			outOfLevel, err := ex.IsOutOfLevel(marketType, symbol, side, e.leg.Level, price)
			if err != nil {
				continue // 盘口读取失败，下一轮再判断
			}
			if !outOfLevel {
				continue // 价格仍在前 N 档，继续等待成交
			}

			// 掉档：撤单，统计部分成交，追价次数 +1
			_ = ex.CancelOrder(marketType, symbol, orderID)
			// 撤单后再查一次最终成交量（撤单前可能已有部分成交）
			if final, err := ex.FetchOrderStatus(marketType, symbol, orderID); err == nil && final.Filled > 0 {
				e.recordFill(out, ex, marketType, side, final.AvgPrice, final.Filled, final.Fee, final.FeeCurrency, orderID, true)
				remaining -= final.Filled
			}
			chase++
			e.logf("info", "chase", symbol,
				fmt.Sprintf("%s 腿订单 @ %.8f 掉出前%d档，已撤单追价（第 %d/%d 次）", marketType, price, e.leg.Level, chase, e.leg.MaxChase))

			// 追价超限：按配置转 Taker 或报错
			if chase > e.leg.MaxChase {
				if !e.leg.ChaseToTaker {
					return out, fmt.Errorf("追价超过 %d 次仍未成交，且未开启超限转 Taker", e.leg.MaxChase)
				}
				e.logf("warn", "chase_to_taker", symbol, fmt.Sprintf("%s 腿追价超限，剩余 %.6f 转 Taker 市价成交", marketType, remaining))
				takerOut, err := e.execTaker(ex, marketType, symbol, side, remaining, reduceOnly)
				if err != nil {
					return out, err
				}
				out.Fills = append(out.Fills, takerOut.Fills...)
				e.recompute(out)
				remaining = 0
			}
			break // 退出轮询，回到外层循环按最新盘口重新挂单
		}
	}

	e.recompute(out)
	if out.Amount <= 0 {
		return out, fmt.Errorf("Maker 执行结束但无任何成交")
	}
	return out, nil
}

// recordFill 把一笔成交追加到腿结果中，并实时重算汇总
func (e *Engine) recordFill(out *LegOutcome, ex *exchange.Exchange, marketType, side string,
	price, amount, fee float64, feeCurrency, orderID string, maker bool) {
	if amount <= 0 {
		return
	}
	if price <= 0 {
		price = 0 // 均价缺失时保留 0，recompute 时用 cost/amount 反推
	}
	out.Fills = append(out.Fills, Fill{
		Exchange: ex.ID(), MarketType: marketType, Side: side,
		Price: price, Amount: amount, CostUSDT: price * amount,
		Fee: fee, FeeCurrency: feeCurrency,
		OrderID: orderID, Maker: maker, Time: time.Now(),
	})
	e.recompute(out)
}

// recompute 由成交明细重算腿的汇总值（总量、加权均价、名义价值、手续费合计）
func (e *Engine) recompute(out *LegOutcome) {
	var amount, cost, fee float64
	for _, f := range out.Fills {
		amount += f.Amount
		cost += f.CostUSDT
		fee += f.Fee
		if f.FeeCurrency != "" {
			out.FeeCurrency = f.FeeCurrency
		}
	}
	out.Amount = amount
	out.CostUSDT = cost
	out.Fee = fee
	if amount > 0 {
		out.AvgPrice = cost / amount
	}
}
