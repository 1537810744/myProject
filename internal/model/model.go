// Package model 定义项目内所有模块共用的数据结构。
// 说明：个人工具不追求严格的 DDD 分层，模型集中放在一个包里，
// 各模块直接引用，避免循环依赖，简单直观。
package model

import "time"

// ---------- 交易所 API 配置（模块 1） ----------

// ExchangeAPI 保存在数据库中的某个交易所的 API 凭证
type ExchangeAPI struct {
	ID        int64     `json:"id"`
	Exchange  string    `json:"exchange"`   // 交易所标识：binance / gate
	Label     string    `json:"label"`      // 用户自定义标签，如“币安主账户”
	APIKey    string    `json:"api_key"`    // API Key
	APISecret string    `json:"api_secret"` // API Secret
	CreatedAt time.Time `json:"created_at"`
}

// APITestResult API 连通性/权限测试结果，返回给前端展示
type APITestResult struct {
	Exchange   string `json:"exchange"`
	Connected  bool   `json:"connected"`  // 连通性（能否拿到服务器时间/公共行情）
	Permission bool   `json:"permission"` // 权限（能否读取私有余额）
	Message    string `json:"message"`    // 附加说明（错误信息等）
}

// ---------- 行情（模块 2） ----------

// MarketCandidate 通过全部过滤条件的“优质对冲标的”，展示在待买入列表中
type MarketCandidate struct {
	Symbol         string    `json:"symbol"`           // 统一币对，如 BTC/USDT（合约端为 BTC/USDT:USDT）
	SwapExchange   string    `json:"swap_exchange"`    // 合约所在交易所（做空腿）
	SpotExchange   string    `json:"spot_exchange"`    // 现货所在交易所（做多腿）
	SwapPrice      float64   `json:"swap_price"`       // 合约最新价
	SpotPrice      float64   `json:"spot_price"`       // 现货最新价
	BasisPct       float64   `json:"basis_pct"`        // 基差百分比 = (合约价-现货价)/现货价*100
	FundingRate    float64   `json:"funding_rate"`     // 当前资金费率（%）
	FundingAvgPct  float64   `json:"funding_avg_pct"`  // 最近 N 次资金费率均值（%）
	FundingRates   []float64 `json:"funding_rates"`    // 最近 N 次资金费率明细（%，旧->新）
	AnnualizedPct  float64   `json:"annualized_pct"`   // 年化 = 当前费率% * 3 * 365（8小时一次）
	QuoteVolume24h float64   `json:"quote_volume_24h"` // 合约 24H 成交额（USDT）
	Direction      string    `json:"direction"`        // 推荐方向说明
	UpdatedAt      time.Time `json:"updated_at"`
}

// ---------- 交易（模块 3） ----------

// TradeRequest 一次对冲下单请求（建仓/平仓共用）
type TradeRequest struct {
	Symbol    string  `json:"symbol"`     // 币对，如 BTC/USDT
	Action    string  `json:"action"`     // open=建仓(买现货+空合约) / close=平仓(卖现货+平空)
	TotalUSDT float64 `json:"total_usdt"` // 组容量：本次计划成交的总名义价值（U）
	AtomUSDT  float64 `json:"atom_usdt"`  // 原子单位：每次下单的名义价值（U）
	DustUSDT  float64 `json:"dust_usdt"`  // 粉尘阈值：剩余低于该值则一并带走
}

// LegResult 单条腿的执行结果
type LegResult struct {
	Exchange   string   `json:"exchange"`
	MarketType string   `json:"market_type"` // spot / swap
	Side       string   `json:"side"`        // buy / sell
	Amount     float64  `json:"amount"`      // 成交的币数量
	AvgPrice   float64  `json:"avg_price"`   // 成交均价
	CostUSDT   float64  `json:"cost_usdt"`   // 名义价值（U）
	OrderIDs   []string `json:"order_ids"`   // 所有子订单号
}

// TradeResult 一次对冲交易的完整结果
type TradeResult struct {
	Success   bool       `json:"success"`
	Message   string     `json:"message"`
	SpotLeg   *LegResult `json:"spot_leg,omitempty"` // 现货腿
	SwapLeg   *LegResult `json:"swap_leg,omitempty"` // 合约腿
	Timestamp time.Time  `json:"timestamp"`
}

// ---------- 对冲持仓（账户模块 & 自动交易模块共用） ----------

// HedgePosition 一个正在持有（或已关闭）的对冲交易对
// 建仓时记录入场基差与均价，供 slow sell 判断“当前基差是否小于买入基差”
type HedgePosition struct {
	ID             int64      `json:"id"`
	Symbol         string     `json:"symbol"`
	SpotExchange   string     `json:"spot_exchange"`
	SwapExchange   string     `json:"swap_exchange"`
	SpotAmount     float64    `json:"spot_amount"`      // 现货腿持币数量
	SwapAmount     float64    `json:"swap_amount"`      // 合约腿张数对应的币数量
	SpotEntryPrice float64    `json:"spot_entry_price"` // 现货买入均价
	SwapEntryPrice float64    `json:"swap_entry_price"` // 合约开仓均价
	EntryBasisPct  float64    `json:"entry_basis_pct"`  // 买入时基差（%）
	Status         string     `json:"status"`           // open / closed
	OpenedAt       time.Time  `json:"opened_at"`
	ClosedAt       *time.Time `json:"closed_at,omitempty"`
}

// ---------- 账户信息（模块 4） ----------

// ExchangeBalance 单个交易所的资金概况
type ExchangeBalance struct {
	Exchange   string  `json:"exchange"`
	MarketType string  `json:"market_type"` // spot / swap
	USDTFree   float64 `json:"usdt_free"`
	USDTUsed   float64 `json:"usdt_used"`
	USDTTotal  float64 `json:"usdt_total"`
}

// SwapPositionInfo 合约实时持仓（从交易所拉取）
type SwapPositionInfo struct {
	Exchange         string  `json:"exchange"`
	Symbol           string  `json:"symbol"`
	Side             string  `json:"side"`      // long / short
	Contracts        float64 `json:"contracts"` // 持仓数量（币）
	EntryPrice       float64 `json:"entry_price"`
	MarkPrice        float64 `json:"mark_price"`
	UnrealizedPnl    float64 `json:"unrealized_pnl"`
	Leverage         float64 `json:"leverage"`
	LiquidationPrice float64 `json:"liquidation_price"` // 强平价（预警模块用）
}

// AccountOverview 聚合账户总览（账户模块输出）
type AccountOverview struct {
	Balances        []ExchangeBalance  `json:"balances"`         // 各交易所资金
	TotalUSDT       float64            `json:"total_usdt"`       // 聚合总资金
	PurchasingPower float64            `json:"purchasing_power"` // 购买力 = min(合约账户*杠杆, 现货账户)
	Hedges          []HedgePosition    `json:"hedges"`           // 当前持有的对冲对（来自数据库）
	SwapPositions   []SwapPositionInfo `json:"swap_positions"`   // 合约实时持仓（来自交易所）
	RunningSince    time.Time          `json:"running_since"`    // 程序本次启动时间（运行时长）
}

// ---------- 预警（模块 5） ----------

// AlertRecord 一条预警记录（同时写入数据库与发送邮件）
type AlertRecord struct {
	ID       int64     `json:"id"`
	Time     time.Time `json:"time"`
	Type     string    `json:"type"`   // funding_negative / adl / liquidation / balance
	Symbol   string    `json:"symbol"` // 相关币对（资金平衡预警时为空）
	Level    string    `json:"level"`  // info / warning / critical
	Message  string    `json:"message"`
	MailSent bool      `json:"mail_sent"`
}

// ---------- 日志（交易模块 & 自动交易模块共用） ----------

// TradeLog 操作日志，前端“日志”页签展示
type TradeLog struct {
	ID      int64     `json:"id"`
	Time    time.Time `json:"time"`
	Module  string    `json:"module"` // 来源模块：trade / autotrade / alert
	Level   string    `json:"level"`  // info / warn / error
	Action  string    `json:"action"` // 动作：open / close / skip_sell / slow_sell / fast_sell / scatter_buy ...
	Symbol  string    `json:"symbol"`
	Message string    `json:"message"`
}
