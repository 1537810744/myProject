// 【阅读顺序 03】全部数据结构（项目的“词汇表”）。
// 为什么所有结构体集中在一个包？—— Go 禁止循环 import。若每个模块自己定义数据结构，
// 模块间互相引用会打成死结。于是开一个【最底层】model 包：它不依赖任何人，所有人都依赖它。
// 每个结构体被三处使用：数据库查询结果 / JSON 序列化给前端 / 模块间传参。
// 语法点预览：type、struct、字段、json tag、指针(*T)、切片([]T)、map、time.Time。
package model

// import 标准库 time：提供时间类型 time.Time。
import "time"

// Version 构建版本号。默认 "dev"；正式构建时用 `-ldflags "-X deltacrypto/internal/model.Version=v1.0.0"` 注入。
// 健康检查接口会返回它，方便确认线上跑的是哪个版本。
var Version = "dev"

// ---------- 交易所 API 配置（模块 1） ----------

// ExchangeAPI 保存在数据库里的某交易所 API 凭证。
// ⚠️ Secret 明文入库：本机个人工具、监听 localhost，明文换实现简单（README 已注明风险）。
type ExchangeAPI struct {
	ID int64 `json:"id"` // int64 数据库自增主键，对齐 SQL 的 INTEGER。
	// 注意“`json:"id"`”叫【struct tag】：告诉 encoding/json 序列化时这个字段在 JSON 里的名字。
	// 前端 JS 习惯小写下划线（api_key），Go 字段名必须大写开头（否则不导出、序列化会漏掉），
	// tag 就是两者之间的“翻译器”。
	Exchange  string    `json:"exchange"`   // string 交易所标识：binance / gate
	Label     string    `json:"label"`      // string 用户自定义标签，如“币安主账户”
	APIKey    string    `json:"api_key"`    // string API Key
	APISecret string    `json:"api_secret"` // string API Secret
	CreatedAt time.Time `json:"created_at"` // time.Time 创建时间（零值=0001-01-01）
}

// APITestResult API 连通性/权限测试结果，返回给前端展示。
type APITestResult struct {
	Exchange   string `json:"exchange"`   // string 交易所标识
	Connected  bool   `json:"connected"`  // bool 连通性（能否拿到服务器时间）——true/false
	Permission bool   `json:"permission"` // bool 私有权限（能否读余额）
	Message    string `json:"message"`    // string 附加说明（错误信息等）
}

// ---------- 行情（模块 2） ----------

// MarketCandidate 通过全部过滤条件的“优质对冲标的”（待买入列表的一行）。
type MarketCandidate struct {
	Symbol         string    `json:"symbol"`           // string 统一币对，如 BTC/USDT
	SwapExchange   string    `json:"swap_exchange"`    // string 合约所在交易所（做空腿）
	SpotExchange   string    `json:"spot_exchange"`    // string 现货所在交易所（做多腿）
	SwapPrice      float64   `json:"swap_price"`       // float64 合约最新价（双精度浮点，存价格够用）
	SpotPrice      float64   `json:"spot_price"`       // float64 现货最新价
	BasisPct       float64   `json:"basis_pct"`        // float64 基差% = (合约价-现货价)/现货价*100 —— 利润来源①
	FundingRate    float64   `json:"funding_rate"`     // float64 当前资金费率（%）—— 利润来源②
	FundingAvgPct  float64   `json:"funding_avg_pct"`  // float64 最近 N 次费率均值（%）—— 趋势判断用
	FundingRates   []float64 `json:"funding_rates"`    // []float64 最近 N 次费率明细（旧->新），切片=动态数组
	AnnualizedPct  float64   `json:"annualized_pct"`   // float64 年化 = 当前费率% * 3 * 365（8小时结算，一天3次）
	QuoteVolume24h float64   `json:"quote_volume_24h"` // float64 合约 24H 成交额（USDT）—— 流通量约束
	Direction      string    `json:"direction"`        // string 推荐方向说明（如“空binance合约/多gate现货”）
	UpdatedAt      time.Time `json:"updated_at"`       // time.Time 更新时间
}

// ---------- 交易（模块 3） ----------

// TradeRequest 一次对冲下单请求。建仓和平仓共用这一个结构体——
// 两者字段几乎一样（币对/金额/原子单位/粉尘），差别只有一个 Action 方向字段。
// 合并能少写一半代码（等将来操作差异大了再拆，现在不过度设计）。
type TradeRequest struct {
	Symbol    string  `json:"symbol"`     // string 币对，如 BTC/USDT
	Action    string  `json:"action"`     // string open=建仓(买现货+空合约) / close=平仓(卖现货+平空)
	TotalUSDT float64 `json:"total_usdt"` // float64 组容量：本次计划成交的总名义价值（U）
	AtomUSDT  float64 `json:"atom_usdt"`  // float64 原子单位：每次下单的名义价值（U）
	DustUSDT  float64 `json:"dust_usdt"`  // float64 粉尘阈值：剩余低于此值则一并带走
	// RequestID 幂等键（防重复下单）：调用方（前端/自动交易）为"同一次业务意图"生成
	// 一个唯一 id；如果网络超时后重发同一个 id，交易模块靠它识别出"这单已经处理过"，
	// 直接返回已处理，绝不再下一次单。空串表示不启用幂等（老调用方不受影响）。
	RequestID string `json:"request_id"`
}

// LegResult 单条腿的执行结果。
// “腿”（leg）是套利术语：一次对冲 = 两条腿——现货腿（gate 买）+ 合约腿（binance 空）。
type LegResult struct {
	Exchange   string   `json:"exchange"`    // string 交易所
	MarketType string   `json:"market_type"` // string spot / swap
	Side       string   `json:"side"`        // string buy / sell
	Amount     float64  `json:"amount"`      // float64 成交的币数量
	AvgPrice   float64  `json:"avg_price"`   // float64 成交均价（多轮成交的加权均价）
	CostUSDT   float64  `json:"cost_usdt"`   // float64 名义价值（U）
	OrderIDs   []string `json:"order_ids"`   // []string 所有子订单号（一个持仓可能拆成多笔单）
}

// TradeResult 一次对冲交易的完整结果。
// SpotLeg/SwapLeg 用【指针 *LegResult】而不是值——因为建仓失败时可能只有一条腿的成交，
// 指针可以表达“这条腿不存在”（nil）。
type TradeResult struct {
	Success bool       `json:"success"`            // bool 是否成功
	Message string     `json:"message"`            // string 给用户看的结果描述
	SpotLeg *LegResult `json:"spot_leg,omitempty"` // *LegResult 现货腿
	// 注意 tag 里多了“,omitempty”：值为 nil 时序列化就省略这个字段，
	// 避免平仓前返回一堆 null。
	SwapLeg   *LegResult `json:"swap_leg,omitempty"` // *LegResult 合约腿
	Timestamp time.Time  `json:"timestamp"`          // time.Time 完成时间
}

// ---------- 对冲持仓（账户模块 & 自动交易模块共用） ----------

// HedgePosition 一个正在持有（或已关闭）的对冲交易对。
// 建仓时记录入场基差与均价——autotrade 的 slow sell 就靠“当前基差 < EntryBasisPct”
// 判断是否该平仓（价差收敛就落袋）。
type HedgePosition struct {
	ID             int64      `json:"id"`               // int64 数据库主键
	Symbol         string     `json:"symbol"`           // string 币对
	SpotExchange   string     `json:"spot_exchange"`    // string 现货所
	SwapExchange   string     `json:"swap_exchange"`    // string 合约所
	SpotAmount     float64    `json:"spot_amount"`      // float64 现货腿持币数量
	SwapAmount     float64    `json:"swap_amount"`      // float64 合约腿对应币数量
	SpotEntryPrice float64    `json:"spot_entry_price"` // float64 现货买入均价
	SwapEntryPrice float64    `json:"swap_entry_price"` // float64 合约开仓均价
	EntryBasisPct  float64    `json:"entry_basis_pct"`  // float64 买入时基差（%）
	Status         string     `json:"status"`           // string open / closed
	OpenedAt       time.Time  `json:"opened_at"`        // time.Time 开仓时间
	ClosedAt       *time.Time `json:"closed_at,omitempty"`
	// 为什么 ClosedAt 用 *time.Time 而不是 time.Time？——
	// 指针能表达“从未平仓”（nil）。如果用 time.Time，它永远有个默认值 0001-01-01，
	// 前端无法区分“还没平”和“0001 年平的”。omitempty：nil 时序列化省略该字段。
}

// ---------- 账户信息（模块 4） ----------

// ExchangeBalance 单个交易所的资金概况。
type ExchangeBalance struct {
	Exchange   string  `json:"exchange"`    // string 交易所
	MarketType string  `json:"market_type"` // string spot / swap
	USDTFree   float64 `json:"usdt_free"`   // float64 可用资金
	USDTUsed   float64 `json:"usdt_used"`   // float64 冻结资金（挂单中）
	USDTTotal  float64 `json:"usdt_total"`  // float64 合计
}

// SwapPositionInfo 合约实时持仓（从交易所接口拉取，不是本地库）。
// 注意和 HedgePosition 的区别：HedgePosition 是我们自己记的账（对冲对）；
// 这个含强平价/未实现盈亏——预警模块要用强平价判断爆仓距离。
type SwapPositionInfo struct {
	Exchange         string  `json:"exchange"`          // string 交易所
	Symbol           string  `json:"symbol"`            // string 币对
	Side             string  `json:"side"`              // string long / short
	Contracts        float64 `json:"contracts"`         // float64 持仓数量（币）
	EntryPrice       float64 `json:"entry_price"`       // float64 开仓均价
	MarkPrice        float64 `json:"mark_price"`        // float64 标记价（算盈亏用的基准价）
	UnrealizedPnl    float64 `json:"unrealized_pnl"`    // float64 未实现盈亏
	Leverage         float64 `json:"leverage"`          // float64 杠杆倍数
	LiquidationPrice float64 `json:"liquidation_price"` // float64 强平价（预警判断爆仓距离用）
}

// AccountOverview 聚合账户总览（前端账户页 + 自动交易买入判断）。
type AccountOverview struct {
	Balances        []ExchangeBalance  `json:"balances"`         // []ExchangeBalance 各交易所资金
	TotalUSDT       float64            `json:"total_usdt"`       // float64 聚合总资金
	PurchasingPower float64            `json:"purchasing_power"` // float64 购买力 = min(合约*杠杆, 现货)
	Hedges          []HedgePosition    `json:"hedges"`           // []HedgePosition 当前对冲对（来自数据库）
	SwapPositions   []SwapPositionInfo `json:"swap_positions"`   // []SwapPositionInfo 合约实时持仓
	RunningSince    time.Time          `json:"running_since"`    // time.Time 程序启动时间（算运行时长）
}

// ---------- 持仓详情（账户模块） ----------

// PositionFill 一笔成交记录（trade_fill 表的行，详情页“成交记录”页签）。
type PositionFill struct {
	ID          int64     `json:"id"`           // int64 主键
	PositionID  int64     `json:"position_id"`  // int64 关联的对冲持仓 id
	Symbol      string    `json:"symbol"`       // string 币对
	Exchange    string    `json:"exchange"`     // string 交易所
	MarketType  string    `json:"market_type"`  // string spot / swap
	Side        string    `json:"side"`         // string buy / sell
	Price       float64   `json:"price"`        // float64 成交价
	Amount      float64   `json:"amount"`       // float64 成交数量
	CostUSDT    float64   `json:"cost_usdt"`    // float64 名义价值
	Fee         float64   `json:"fee"`          // float64 手续费
	FeeCurrency string    `json:"fee_currency"` // string 手续费币种
	OrderID     string    `json:"order_id"`     // string 订单号
	Maker       bool      `json:"maker"`        // bool true=Maker成交(挂单)/false=Taker成交(市价)
	TradedAt    time.Time `json:"traded_at"`    // time.Time 成交时间
}

// FundingPaymentRecord 一笔资金费结算（funding_payment 表，详情页“资金费率流水”页签）。
type FundingPaymentRecord struct {
	ID       int64     `json:"id"`        // int64 主键
	Exchange string    `json:"exchange"`  // string 交易所
	Symbol   string    `json:"symbol"`    // string 币对
	Amount   float64   `json:"amount"`    // float64 收入为正 / 支出为负（USDT）
	IncomeAt time.Time `json:"income_at"` // time.Time 结算时间
}

// PositionDetailStats 持仓详情顶部统计（两排指标卡）。
type PositionDetailStats struct {
	SwapMarginUsed float64 `json:"swap_margin_used"` // float64 合约占用资金（保证金=名义价值/杠杆）
	SpotCostUsed   float64 `json:"spot_cost_used"`   // float64 现货占用资金（买入成本）
	BasisPnl       float64 `json:"basis_pnl"`        // float64 期现收益（合约浮动盈亏+现货浮动盈亏）
	FundingPnl     float64 `json:"funding_pnl"`      // float64 费率收益（资金费流水累计）
	NetProfit      float64 `json:"net_profit"`       // float64 净收益 = 期现 + 费率 - 手续费
	FeeUSDT        float64 `json:"fee_usdt"`         // float64 手续费合计（USDT 部分）
	YieldPct       float64 `json:"yield_pct"`        // float64 收益率 = 净收益 / 总占用
	AnnualizedPct  float64 `json:"annualized_pct"`   // float64 年化收益率
	RunDuration    string  `json:"run_duration"`     // string 运行时长（人性化：3天2小时）
	NetExposure    float64 `json:"net_exposure"`     // float64 敞口 = |现货量 - 合约量|
	NextFundingEst float64 `json:"next_funding_est"` // float64 下次费率预估收益（当前费率×合约名义价值）
}

// PositionLegDetail 单条腿的详情（合约腿/现货腿共用）。
type PositionLegDetail struct {
	Exchange       string  `json:"exchange"`         // string 交易所
	Amount         float64 `json:"amount"`           // float64 持仓数量（币）
	AvgPrice       float64 `json:"avg_price"`        // float64 均价（开仓/成本）
	MarkPrice      float64 `json:"mark_price"`       // float64 标记价/最新价
	ValueUSDT      float64 `json:"value_usdt"`       // float64 持仓价值
	UnrealizedPnl  float64 `json:"unrealized_pnl"`   // float64 未实现盈亏
	RealizedPnl    float64 `json:"realized_pnl"`     // float64 已实现盈亏
	NextFundingPct float64 `json:"next_funding_pct"` // float64 下次费率（%）（仅合约腿）
	NextSettleAt   string  `json:"next_settle_at"`   // string 下次结算时间（仅合约腿）
	LastSyncAt     string  `json:"last_sync_at"`     // string 最后同步时间
}

// PositionDetail 持仓详情完整聚合（详情接口返回：统计 + 双腿 + 敞口分析）。
type PositionDetail struct {
	Symbol   string              `json:"symbol"`   // string 币对
	Status   string              `json:"status"`   // string open / closed
	Stats    PositionDetailStats `json:"stats"`    // PositionDetailStats 顶部统计
	SwapLeg  PositionLegDetail   `json:"swap_leg"` // PositionLegDetail 合约腿
	SpotLeg  PositionLegDetail   `json:"spot_leg"` // PositionLegDetail 现货腿
	Exposure map[string]float64  `json:"exposure"` // map[string]float64 敞口分析（合约/现货/净敞口）
}

// ProfitPoint 收益曲线的一个点（profit_snapshot 表，详情页“收益曲线”页签）。
type ProfitPoint struct {
	Time       time.Time `json:"time"`        // time.Time 时间点
	NetProfit  float64   `json:"net_profit"`  // float64 净收益
	BasisPnl   float64   `json:"basis_pnl"`   // float64 期现收益
	FundingCum float64   `json:"funding_cum"` // float64 费率累计
	FeeCum     float64   `json:"fee_cum"`     // float64 手续费累计
}

// ---------- 健康检查（运维） ----------

// HealthStatus 健康检查结果（/api/health 返回）。
type HealthStatus struct {
	Status        string `json:"status"`         // "ok" / "degraded"
	DB            string `json:"db"`             // "ok" / 错误信息
	HubReady      bool   `json:"hub_ready"`      // 两条交易连接是否就绪
	Halted        bool   `json:"halted"`         // 自动交易是否被熔断/手动停机
	UptimeSeconds int64  `json:"uptime_seconds"` // 程序已运行秒数
	Version       string `json:"version"`        // 构建版本（可用 -ldflags 注入）
}

// ---------- 对账（风险控制） ----------

// ReconcileItem 单个持仓的对账结果。
type ReconcileItem struct {
	PositionID   int64   `json:"position_id"`    // 数据库持仓 id
	Symbol       string  `json:"symbol"`         // 币对
	DBSwapAmount float64 `json:"db_swap_amount"` // 数据库记录的合约数量
	ExSwapAmount float64 `json:"ex_swap_amount"` // 交易所实际合约数量（0=没有该持仓）
	DBSpotAmount float64 `json:"db_spot_amount"` // 数据库记录的现货数量
	ExSpotAmount float64 `json:"ex_spot_amount"` // 交易所实际现货余额
	Status       string  `json:"status"`         // "ok" / "missing_swap" / "amount_mismatch" / "missing_spot"
	Message      string  `json:"message"`        // 给人看的说明
}

// ReconcileReport 一次对账的完整报告。
type ReconcileReport struct {
	CheckedAt    time.Time       `json:"checked_at"`
	Total        int             `json:"total"`         // 检查的持仓数
	Mismatches   int             `json:"mismatches"`    // 有问题的持仓数
	Items        []ReconcileItem `json:"items"`         // 明细
	IsConsistent bool            `json:"is_consistent"` // 是否完全一致
}

// ---------- 预警（模块 5） ----------

// AlertRecord 一条预警记录（同时写库 + 发邮件）。
type AlertRecord struct {
	ID       int64     `json:"id"`        // int64 主键
	Time     time.Time `json:"time"`      // time.Time 触发时间
	Type     string    `json:"type"`      // string funding_negative / adl / liquidation / balance
	Symbol   string    `json:"symbol"`    // string 相关币对（资金平衡预警时为空）
	Level    string    `json:"level"`     // string info / warning / critical
	Message  string    `json:"message"`   // string 给用户看的描述
	MailSent bool      `json:"mail_sent"` // bool 邮件是否发出
}

// ---------- 日志（交易模块 & 自动交易模块共用） ----------

// TradeLog 操作日志，前端“日志”页签展示。
type TradeLog struct {
	ID      int64     `json:"id"`      // int64 主键
	Time    time.Time `json:"time"`    // time.Time 时间
	Module  string    `json:"module"`  // string 来源模块：trade / autotrade / alert
	Level   string    `json:"level"`   // string info / warn / error
	Action  string    `json:"action"`  // string 动作：open / close / skip_sell / slow_sell / fast_sell / scatter_buy ...
	Symbol  string    `json:"symbol"`  // string 币对
	Message string    `json:"message"` // string 日志正文
}
