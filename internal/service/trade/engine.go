// 【阅读顺序 09】交易模块核心：双腿对冲执行引擎（项目灵魂）。
// 职责：执行“一轮双腿对冲”。相比原来“两腿市价单”，升级为：
//   - 优先腿（现货）先执行，对冲腿（合约）按现货【实际成交量】实时跟进；
//   - Maker 模式：限价挂前 N 档，掉档自动撤单追价到第 1 档，超限转 Taker；
//   - 最大净敞口：两腿量差超阈值自动停止告警。
//
// 为什么现货先成交、合约跟进？—— 现货流动性不如合约，先知道“实际成交多少”，
// 合约再按这个量对冲，才能做到两腿相等（净敞口≈0）。反过来先下合约，现货买不满就留敞口。
// 语法点预览：命名返回值、for 条件循环、布尔标志驱动循环、continue/break/return、
// 类型断言（在 exchange.go）、%w 错误包装、回调函数字段。
package trade

// import 导入用到的包。
import (
	"fmt"     // 格式化
	"math"    // 数学：math.Abs
	"strings" // 字符串：错误分类匹配
	"time"    // 时间

	"deltacrypto/internal/exchange" // 交易所抽象层
)

// Exchange 是引擎对交易所连接的最小依赖接口。
// 为什么定义接口而不是直接用 *exchange.Exchange？—— 依赖倒置 + 可测试性：
//
//	引擎只依赖"下单/撤单/查单/盘口/精度"这一组方法；任何实现了这些方法的对象都能
//	驱动引擎。单测时注入一个假实现（fake）就能离线测试引擎逻辑，不用真连交易所。
//
// *exchange.Exchange 天然满足这个接口（Go 鸭子类型，无需声明 implements）。
type Exchange interface {
	ID() string
	AmountToPrecision(marketType, baseSymbol string, amount float64) float64
	CreateMarketOrder(marketType, baseSymbol, side string, amount float64) (*exchange.MarketOrderResult, error)
	CreateMarketOrderWithParams(baseSymbol, side string, amount float64, params map[string]any) (*exchange.MarketOrderResult, error)
	CreateLimitOrder(marketType, baseSymbol, side string, amount, price float64) (string, error)
	CancelOrder(marketType, baseSymbol, orderID string) error
	FetchOrderStatus(marketType, baseSymbol, orderID string) (*exchange.OrderStatus, error)
	FetchLevelPrice(marketType, baseSymbol, side string, level int) (float64, error)
	IsOutOfLevel(marketType, baseSymbol, side string, level int, myPrice float64) (bool, error)
}

// EngineConfig 引擎整体配置（对应参考图的「执行控制」区）。
type EngineConfig struct {
	MaxNetExposure float64       // float64 最大净敞口（币数量），0=不限；超出自动停止告警
	MaxRetry       int           // int 下单/查询失败的最大重试次数
	PollInterval   time.Duration // time.Duration 订单状态轮询间隔（低频工具 1 秒足够）
}

// LegConfig 单条腿的执行配置。
// 当前两腿共用一套配置（需求是“参考成熟方案”，保持简单）；结构上预留了将来双腿独立配置。
type LegConfig struct {
	OrderMethod  string // string 下单方式：maker / taker
	Level        int    // int 盘口档位：Maker 挂单落在前 N 档，掉出则追价
	MaxChase     int    // int 最大追价次数：超过后按 ChaseToTaker 处理
	ChaseToTaker bool   // bool 追价超限后是否转 Taker（市价单保证成交）
}

// Fill 一笔实际成交明细（写 trade_fill 表 + 聚合腿均价用）。
type Fill struct {
	Exchange    string    // string 交易所
	MarketType  string    // string spot / swap
	Side        string    // string buy / sell
	Price       float64   // float64 成交均价
	Amount      float64   // float64 成交币数量
	CostUSDT    float64   // float64 名义价值
	Fee         float64   // float64 手续费
	FeeCurrency string    // string 手续费币种
	OrderID     string    // string 订单号
	Maker       bool      // bool true=Maker 成交 / false=Taker 成交
	Time        time.Time // time.Time 成交时间
}

// LegOutcome 单条腿的执行结果（成交汇总 + 明细）。
type LegOutcome struct {
	Fills       []Fill  // []Fill 全部成交明细
	Amount      float64 // float64 总成交币数量
	AvgPrice    float64 // float64 加权均价
	CostUSDT    float64 // float64 总名义价值
	Fee         float64 // float64 手续费合计
	FeeCurrency string  // string 手续费币种
}

// Engine 双腿对冲执行引擎：现货=优先腿，合约=对冲腿。
// spot/swap 用上面的 Exchange【接口】类型：既可以传真实的 *exchange.Exchange，
// 也可以传单测的 fake，引擎本身不关心具体实现。
type Engine struct {
	spot Exchange     // Exchange 优先腿（现货）
	swap Exchange     // Exchange 对冲腿（合约）
	cfg  EngineConfig // EngineConfig 整体配置
	leg  LegConfig    // LegConfig 单腿配置
	// logf 日志回调：引擎不直接写库，由 trade.Service 注入（依赖反转，见 trade.go）。
	// 字段类型是【函数类型】：func(level, action, symbol, msg string)，
	// 存的就是一个函数，调用 logf(...) 就是在调用注入的那个函数。
	logf func(level, action, symbol, msg string)
}

// NewEngine 创建引擎。
func NewEngine(spot, swap Exchange, cfg EngineConfig, leg LegConfig,
	logf func(level, action, symbol, msg string)) *Engine {
	return &Engine{spot: spot, swap: swap, cfg: cfg, leg: leg, logf: logf}
}

// HedgeOnce 执行一轮双腿对冲（优先腿先执行，对冲腿实时跟进）。
// 参数：symbol 内部币对；spotSide/swapSide 两腿方向；amount 本轮计划量；
//
//	reduceOnly 平仓时合约腿附加 reduceOnly（只减仓不开新仓）。
//
// 返回两腿成交结果；若量差超最大净敞口，返回错误（需人工处理）。
// 注意函数签名是【命名返回值】：spotOut, swapOut *LegOutcome, err 在头里声明了名字。
func (e *Engine) HedgeOnce(symbol, spotSide, swapSide string, amount float64, reduceOnly bool) (spotOut, swapOut *LegOutcome, err error) {
	// ---------- 第 1 步：优先腿（现货） ----------
	// reduceOnly=false：现货腿开多/平多都不存在“反向开仓”风险，不需要保护。
	spotOut, err = e.execWithRetry(e.spot, "spot", symbol, spotSide, amount, false)
	if err != nil {
		return spotOut, nil, fmt.Errorf("优先腿（现货 %s）失败: %w", spotSide, err)
	}
	if spotOut.Amount <= 0 { // 防呆：现货一单没成交
		return spotOut, nil, fmt.Errorf("优先腿未成交任何数量") // 终止，别用 0 去下合约单
	}

	// ---------- 第 2 步：对冲腿（合约）实时跟进 ----------
	// “实时跟进”的关键：传给合约腿的量是 spotOut.Amount（【实际】成交），
	// 不是传入的 amount（计划量）。计划 10 可能只成交 9.8，合约就对冲 9.8。
	swapOut, err = e.execWithRetry(e.swap, "swap", symbol, swapSide, spotOut.Amount, reduceOnly)
	if err != nil {
		// 对冲腿失败 = 只持有一条腿 = 裸敞口，这是套利最危险的状态：价格一动就亏。
		// 所以 error 级日志 + 终止执行，提示人工处理（绝不静默跳过）。
		e.logf("error", "hedge", symbol,
			fmt.Sprintf("对冲腿（合约 %s %.6f）失败: %v —— 存在净敞口 %.6f 币，请人工处理！",
				swapSide, spotOut.Amount, err, spotOut.Amount))
		return spotOut, nil, fmt.Errorf("对冲腿（合约 %s）失败，净敞口 %.6f: %w", swapSide, spotOut.Amount, err)
	}

	// ---------- 第 3 步：净敞口检查 ----------
	// 两腿都完成，比较量差（部分成交/精度截断可能造成微小差异）。
	exposure := math.Abs(spotOut.Amount - swapOut.Amount)            // 绝对值 = 量差
	if e.cfg.MaxNetExposure > 0 && exposure > e.cfg.MaxNetExposure { // 超上限（且开启了限制）
		// 敞口超限 = 两腿没对齐 = 风险裸露，必须停，让人来对齐。
		e.logf("error", "exposure", symbol,
			fmt.Sprintf("净敞口 %.6f 超过上限 %.6f，自动停止，请人工平仓对齐！", exposure, e.cfg.MaxNetExposure))
		return spotOut, swapOut, fmt.Errorf("净敞口 %.6f 超过上限 %.6f，执行已停止", exposure, e.cfg.MaxNetExposure)
	}
	if exposure > 0 { // 有微小差异（精度截断），不阻断，记 warn
		e.logf("warn", "exposure", symbol, fmt.Sprintf("存在微小净敞口 %.8f（精度差），继续执行", exposure))
	}
	return spotOut, swapOut, nil // 成功返回两腿结果
}

// execWithRetry 单腿执行入口：失败按 MaxRetry 重试。
// 为什么重试？—— 网络偶发失败（超时/限流/断线）是常态，一次失败就放弃会把
// 能成的交易搞砸。但也不无限重试，超上限报错交给人处理。
// ⚠️ 关键：只对【可重试】的错误重试（网络/限流/临时故障）；不可重试的错误
// （余额不足、参数非法、交易所拒绝）重试 N 次只会浪费时间和可能越下越错，
// 所以【失败即停】直接返回——这叫"错误分类重试"。
func (e *Engine) execWithRetry(ex Exchange, marketType, symbol, side string, amount float64, reduceOnly bool) (*LegOutcome, error) {
	var err error              // 声明 error 变量（零值 nil）
	var out *LegOutcome        // 声明结果指针
	maxRetry := e.cfg.MaxRetry // 取配置的重试次数
	if maxRetry < 1 {
		maxRetry = 1 // 至少尝试一次
	}
	for attempt := 1; attempt <= maxRetry; attempt++ { // 经典 for 三段式：初始化;条件;步进
		out, err = e.execLeg(ex, marketType, symbol, side, amount, reduceOnly) // 执行单腿
		if err == nil {                                                        // 成功（err 为 nil）
			return out, nil // 立即返回结果
		}
		if !isRetryable(err) { // 不可重试的错误（余额不足/参数非法等）
			e.logf("error", "fatal", symbol, fmt.Sprintf("%s 腿不可重试错误，停止: %v", marketType, err))
			return nil, err // 失败即停，不浪费重试
		}
		e.logf("warn", "retry", symbol, fmt.Sprintf("%s 腿第 %d/%d 次执行失败（可重试）: %v", marketType, attempt, maxRetry, err))
		time.Sleep(e.cfg.PollInterval) // 重试前等 1 秒，别把交易所打得更狠
	}
	return nil, err // 全部失败，返回最后一次错误
}

// isRetryable 判断错误是否值得重试：只看错误信息里是否出现"网络/限流/临时故障"特征。
// 这是一种务实的启发式判断（ccxt 的错误类型因版本而异，直接分类不稳定）；
// 命中"余额不足/参数非法/订单不存在"这类确定性错误则返回 false（不重试）。
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// 可重试特征：网络抖动、超时、断连、交易所限流、临时不可用。
	retryableHints := []string{
		"timeout", "timed out", "econnreset", "connection refused",
		"connection reset", "rate limit", "too many requests", "429",
		"slow down", "temporary", "busy", "503", "520", "unreachable",
	}
	for _, hint := range retryableHints {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}

// execLeg 单腿执行调度：按配置走 Taker（市价）或 Maker（限价追价）。
func (e *Engine) execLeg(ex Exchange, marketType, symbol, side string, amount float64, reduceOnly bool) (*LegOutcome, error) {
	if e.leg.OrderMethod == "taker" { // 配置成市价
		return e.execTaker(ex, marketType, symbol, side, amount, reduceOnly)
	}
	// 默认（及显式 "maker"）走 Maker。为什么默认 Maker？—— 见文件头：
	// 吃挂单费优惠 + 价格优势。
	return e.execMaker(ex, marketType, symbol, side, amount, reduceOnly)
}

// execTaker 市价单执行：一次下单直接成交。
// 为什么保留它？—— 行情剧烈波动时限价可能一直挂不到，用户可选市价保成交
// （代价是付 taker 手续费、没价格优势）。
func (e *Engine) execTaker(ex Exchange, marketType, symbol, side string, amount float64, reduceOnly bool) (*LegOutcome, error) {
	amount = ex.AmountToPrecision(marketType, symbol, amount) // 按精度截断数量
	if amount <= 0 {
		return nil, fmt.Errorf("数量精度换算后为 0")
	}
	var order *exchange.MarketOrderResult // 声明结果
	var err error
	if reduceOnly { // 平仓：加 reduceOnly（只减仓）保护
		// reduceOnly 确保这笔单【绝不会开新仓】，防止数量算错意外把空头开大。
		order, err = ex.CreateMarketOrderWithParams(symbol, side, amount, map[string]any{"reduceOnly": true})
	} else { // 建仓：普通市价单
		order, err = ex.CreateMarketOrder(marketType, symbol, side, amount)
	}
	if err != nil {
		return nil, err
	}
	out := &LegOutcome{Amount: order.Filled, AvgPrice: order.AvgPrice, CostUSDT: order.Cost} // 汇总
	out.Fills = append(out.Fills, Fill{                                                      // 记录一笔成交明细
		Exchange: ex.ID(), MarketType: marketType, Side: side,
		Price: order.AvgPrice, Amount: order.Filled, CostUSDT: order.Cost,
		OrderID: order.OrderID, Maker: false, Time: time.Now(), // Maker=false：市价是 taker 成交
	})
	return out, nil
}

// execMaker Maker 核心循环：挂单 → 轮询 → 掉档追价 → 超限转 Taker。
// 挂第 N 档而不是第 1 档？—— 第 1 档是最优价，挂那里大概率立即成交（变 taker 收费）；
// 挂第 N 档排在队列后面等别人的单来成交，才能拿到 maker 费率优惠。代价是成交慢、
// 可能被甩开——所以才有后面的追价机制兜底。
func (e *Engine) execMaker(ex Exchange, marketType, symbol, side string, amount float64, reduceOnly bool) (*LegOutcome, error) {
	out := &LegOutcome{FeeCurrency: "USDT"} // 结果对象，默认手续费币种 USDT
	remaining := amount                     // 剩余待成交数量
	chase := 0                              // 已追价次数

	for remaining > 0 { // 还有剩余就继续（for 条件循环）
		// 按交易所精度截断；为 0 说明剩余是粉尘（太小挂不了），直接结束。
		amountToPlace := ex.AmountToPrecision(marketType, symbol, remaining)
		if amountToPlace <= 0 {
			break // break = 跳出整个循环
		}

		// 第 1 步：取盘口第 N 档价格挂单（买单看 bids，卖单看 asks）。
		price, err := ex.FetchLevelPrice(marketType, symbol, side, e.leg.Level)
		if err != nil {
			return out, fmt.Errorf("读取盘口失败: %w", err)
		}
		orderID, err := ex.CreateLimitOrder(marketType, symbol, side, amountToPlace, price) // 挂限价单
		if err != nil {
			return out, fmt.Errorf("挂限价单失败: %w", err)
		}
		e.logf("info", "order_placed", symbol,
			fmt.Sprintf("%s 腿 Maker 挂单 %s %.6f @ %.8f（前%d档）", marketType, side, amountToPlace, price, e.leg.Level))

		// 第 2 步：轮询订单，直到成交 / 掉档追价。
		filledThisOrder := false // 布尔标志：这单搞定了没？
		for !filledThisOrder {   // 【布尔标志驱动循环】：条件为 false 就继续转
			time.Sleep(e.cfg.PollInterval) // 低频工具：1 秒看一次单子

			status, err := ex.FetchOrderStatus(marketType, symbol, orderID) // 查单
			if err != nil {
				// 查单失败不致命（可能订单刚撤），跳出内层循环重新挂单。
				e.logf("warn", "order_query", symbol, fmt.Sprintf("查询订单 %s 失败（将重新挂单）: %v", orderID, err))
				break
			}

			// 全量成交 → 这单结束。
			// 为什么两个条件（Filled ≥ 下单量 或 Status==closed）？—— 有的交易所
			// status 永远是 open，只能靠 Filled 判断；有的 Filled 有延迟，靠 status 兜底。
			if status.Filled >= amountToPlace || status.Status == "closed" {
				e.recordFill(out, ex, marketType, side, status.AvgPrice, status.Filled, status.Fee, status.FeeCurrency, orderID, true)
				remaining -= status.Filled // 扣掉已成交部分
				filledThisOrder = true     // 标志置 true，退出内层循环
				continue                   // continue：跳过下面的检查，直接回循环头
			}

			// 未全量成交 → 检查是否掉出前 N 档（需要追价）。
			outOfLevel, err := ex.IsOutOfLevel(marketType, symbol, side, e.leg.Level, price)
			if err != nil {
				continue // 盘口读取失败，下一轮再看（宽容处理，别因一次失败误撤单）
			}
			if !outOfLevel { // 价格仍在前 N 档
				continue // 继续等成交
			}

			// 掉档：撤单，统计已成交部分，追价次数 +1。
			// 为什么撤单？—— 行情已走远，我的挂单价格不再有优势，继续挂要么不成交
			// 要么成交也吃亏。撤掉重挂更优的价。
			_ = ex.CancelOrder(marketType, symbol, orderID) // 撤单（忽略错误）
			// 撤单 ≠ 没成交：排队期间可能已成交一部分，撤完再查一次把已成交部分记上。
			if final, err := ex.FetchOrderStatus(marketType, symbol, orderID); err == nil && final.Filled > 0 {
				e.recordFill(out, ex, marketType, side, final.AvgPrice, final.Filled, final.Fee, final.FeeCurrency, orderID, true)
				remaining -= final.Filled
			}
			chase++ // 追价次数 +1
			e.logf("info", "chase", symbol,
				fmt.Sprintf("%s 腿订单 @ %.8f 掉出前%d档，已撤单追价（第 %d/%d 次）", marketType, price, e.leg.Level, chase, e.leg.MaxChase))

			// 追价超限：按配置转 Taker 或报错。
			if chase > e.leg.MaxChase { // 追价次数超过上限
				if !e.leg.ChaseToTaker { // 没开“超限转 Taker”
					// 用户宁可不成交也不付 taker 费，报错停下。
					return out, fmt.Errorf("追价超过 %d 次仍未成交，且未开启超限转 Taker", e.leg.MaxChase)
				}
				// 开了：一直挂不到太耗时，果断市价吃掉剩余。
				// 这是“限价省费 vs 市价保成交”的权衡——挂太久了，价格风险已经超过手续费收益。
				e.logf("warn", "chase_to_taker", symbol, fmt.Sprintf("%s 腿追价超限，剩余 %.6f 转 Taker 市价成交", marketType, remaining))
				takerOut, err := e.execTaker(ex, marketType, symbol, side, remaining, reduceOnly) // 市价吃掉剩余
				if err != nil {
					return out, err
				}
				out.Fills = append(out.Fills, takerOut.Fills...) // 合并市价成交明细
				e.recompute(out)                                 // 重算汇总
				remaining = 0                                    // 剩余清零，外层循环结束
			}
			break // 退出内层轮询，回到外层循环按最新盘口重新挂单
		}
	}

	e.recompute(out) // 最终重算一遍汇总
	if out.Amount <= 0 {
		return out, fmt.Errorf("Maker 执行结束但无任何成交")
	}
	return out, nil
}

// recordFill 把一笔成交追加到腿结果中，并实时重算汇总。
func (e *Engine) recordFill(out *LegOutcome, ex Exchange, marketType, side string,
	price, amount, fee float64, feeCurrency, orderID string, maker bool) {
	if amount <= 0 { // 成交量为 0 不记录（比如撤单时查到没成交）
		return
	}
	if price <= 0 {
		price = 0 // 均价缺失时保留 0，recompute 时用 cost/amount 反推
	}
	out.Fills = append(out.Fills, Fill{ // 追加一笔明细
		Exchange: ex.ID(), MarketType: marketType, Side: side,
		Price: price, Amount: amount, CostUSDT: price * amount, // 名义价值 = 价×量
		Fee: fee, FeeCurrency: feeCurrency,
		OrderID: orderID, Maker: maker, Time: time.Now(),
	})
	e.recompute(out) // 实时重算汇总
}

// recompute 由成交明细重算腿的汇总值（总量/加权均价/名义价值/手续费）。
// 为什么每次追加都重算，而不是维护累加变量？—— 累加变量容易在“部分撤单、追加合并”
// 等路径里算错；每次从明细重算虽然 O(n)，但一定正确。个人工具性能够，“宁可多算一次，不要算错”。
func (e *Engine) recompute(out *LegOutcome) {
	var amount, cost, fee float64 // 三个累加器（声明时全为 0）
	for _, f := range out.Fills { // 遍历所有明细
		amount += f.Amount // 累加数量
		cost += f.CostUSDT // 累加成本
		fee += f.Fee       // 累加手续费
		if f.FeeCurrency != "" {
			out.FeeCurrency = f.FeeCurrency // 取最后一个非空币种
		}
	}
	out.Amount = amount // 写回总数量
	out.CostUSDT = cost // 写回总成本
	out.Fee = fee       // 写回总手续费
	if amount > 0 {
		out.AvgPrice = cost / amount // 加权均价 = 总成本 / 总数量
	}
}
