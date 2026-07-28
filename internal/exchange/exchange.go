// 【阅读顺序 05】交易所连接抽象层（全文最长，但结构简单）。
//
// 本文件职责：把 ccxt 的调用收敛为项目内统一的 Exchange 类型 + Hub 管理器。
// 阅读目的：明白两件事——
//
//	1) 为什么要有这一层（需求：“和交易所的连接要抽象化，方便更换交易所”）：
//	   上层模块（行情/交易/账户/预警）只依赖本包，不直接碰 ccxt；
//	   换交易所只需改 newCcxtClient 的一个 case。
//	2) 四条连接的分工：
//	   Spot/Swap（带凭证，可交易，账户/交易/预警用）
//	   PublicSpot/PublicSwap（无凭证只读，行情模块用——没配 API 也能看行情）。
//
// 阅读提示：文件分六段——接口定义 / 构造 / 公共行情 / 账户 / 交易（含引擎用的
// 限价单/盘口/撤单）/ Hub 管理器。指针解引用的小工具函数在末尾。
package exchange

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	ccxt "github.com/ccxt/ccxt/go/v4"

	"deltacrypto/internal/database"
	"deltacrypto/internal/model"
)

// ccxtClient 是 *ccxt.Binance / *ccxt.Gate 的公共方法集。
// ccxt 的 Go 版本由统一模板生成，各交易所结构体方法签名一致，
// 因此可以用接口统一持有，便于替换交易所。
type ccxtClient interface {
	LoadMarkets(params ...any) (map[string]ccxt.MarketInterface, error)
	FetchTime(params ...any) (int64, error)
	FetchTicker(symbol string, options ...ccxt.FetchTickerOptions) (ccxt.Ticker, error)
	FetchTickers(options ...ccxt.FetchTickersOptions) (ccxt.Tickers, error)
	FetchFundingRate(symbol string, options ...ccxt.FetchFundingRateOptions) (ccxt.FundingRate, error)
	FetchFundingRates(options ...ccxt.FetchFundingRatesOptions) (ccxt.FundingRates, error)
	FetchFundingRateHistory(options ...ccxt.FetchFundingRateHistoryOptions) ([]ccxt.FundingRateHistory, error)
	FetchBalance(params ...any) (ccxt.Balances, error)
	FetchPositions(options ...ccxt.FetchPositionsOptions) ([]ccxt.Position, error)
	SetLeverage(leverage int64, options ...ccxt.SetLeverageOptions) (map[string]any, error)
	CreateOrder(symbol string, typeVar string, side string, amount float64, options ...ccxt.CreateOrderOptions) (ccxt.Order, error)
	// 以下为交易引擎与持仓详情所需（盘口/限价单/撤单/查单/资金费流水）
	FetchOrderBook(symbol string, options ...ccxt.FetchOrderBookOptions) (ccxt.OrderBook, error)
	FetchOrder(id string, options ...ccxt.FetchOrderOptions) (ccxt.Order, error)
	CancelOrder(id string, options ...ccxt.CancelOrderOptions) (ccxt.Order, error)
	FetchFundingHistory(options ...ccxt.FetchFundingHistoryOptions) ([]ccxt.FundingHistory, error)
}

// Exchange 项目内统一的交易所连接对象
type Exchange struct {
	id     string     // 交易所标识：binance / gate
	role   string     // 承担角色：spot（现货腿）/ swap（合约腿）
	client ccxtClient // ccxt 统一接口
	raw    any        // 原始 ccxt 对象，保留用于交易所特有功能（如币安 ADL 排名）
}

// newCcxtClient 按交易所 ID 构造 ccxt 客户端。
// 新增交易所时在此添加 case 即可（抽象层的扩展点）。
func newCcxtClient(id, role, apiKey, secret string) (ccxtClient, any, error) {
	cfg := map[string]any{
		"apiKey":          apiKey,
		"secret":          secret,
		"enableRateLimit": true, // 开启内置限速，避免触发交易所频控
	}
	switch id {
	case "binance":
		// 合约腿：默认市场类型设为 USDT 永续（swap）
		if role == "swap" {
			cfg["options"] = map[string]any{"defaultType": "swap"}
		}
		ex := ccxt.NewBinance(cfg)
		return ex, ex, nil
	case "gate":
		// 现货腿：默认市场类型 spot
		cfg["options"] = map[string]any{"defaultType": "spot"}
		ex := ccxt.NewGate(cfg)
		return ex, ex, nil
	default:
		return nil, nil, fmt.Errorf("暂不支持的交易所: %s", id)
	}
}

// New 创建并初始化一个交易所连接（加载市场信息）
func New(id, role, apiKey, secret string) (*Exchange, error) {
	client, raw, err := newCcxtClient(id, role, apiKey, secret)
	if err != nil {
		return nil, err
	}
	e := &Exchange{id: id, role: role, client: client, raw: raw}
	// 预加载市场表，后续 symbol 校验/精度换算都依赖它
	if _, err := client.LoadMarkets(); err != nil {
		return nil, fmt.Errorf("%s 加载市场信息失败: %w", id, err)
	}
	return e, nil
}

// ID 返回交易所标识
func (e *Exchange) ID() string { return e.id }

// Role 返回承担角色（spot/swap）
func (e *Exchange) Role() string { return e.role }

// ---------- 连通性 / 权限测试（模块 1 使用） ----------

// TestPublic 测试公共接口连通性（拉服务器时间）
func (e *Exchange) TestPublic() error {
	_, err := e.client.FetchTime()
	return err
}

// TestPrivate 测试私有接口权限（读取余额）
func (e *Exchange) TestPrivate() error {
	_, err := e.client.FetchBalance()
	return err
}

// ---------- 行情相关（模块 2 使用） ----------

// TickerData 项目内简化的行情数据（价格单位为 USDT）
type TickerData struct {
	Symbol      string  // ccxt 标准符号（现货 BTC/USDT，合约 BTC/USDT:USDT）
	Last        float64 // 最新价
	QuoteVolume float64 // 24H 成交额（USDT）
}

// SwapSymbols 返回该交易所全部“USDT 永续合约”的内部币对（形如 BTC/USDT，已去掉 :USDT 后缀）
func (e *Exchange) SwapSymbols() ([]string, error) {
	markets, err := e.client.LoadMarkets()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, mkt := range markets {
		// 注意：ccxt 的 MarketInterface 虽名为 Interface，实为结构体，直接用字段
		if str(mkt.Type) == "swap" && str(mkt.QuoteCurrency) == "USDT" && boolv(mkt.Active) {
			out = append(out, SwapToBase(str(mkt.Symbol)))
		}
	}
	return out, nil
}

// SpotSymbols 返回该交易所全部“USDT 现货”的内部币对（形如 BTC/USDT）
func (e *Exchange) SpotSymbols() ([]string, error) {
	markets, err := e.client.LoadMarkets()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, mkt := range markets {
		if str(mkt.Type) == "spot" && str(mkt.QuoteCurrency) == "USDT" && boolv(mkt.Active) {
			out = append(out, str(mkt.Symbol))
		}
	}
	return out, nil
}

// FetchTickers 批量拉取行情；spot 传 ["BTC/USDT"]，swap 传 ["BTC/USDT:USDT"]
func (e *Exchange) FetchTickers(symbols []string) (map[string]TickerData, error) {
	res := make(map[string]TickerData, len(symbols))
	if len(symbols) == 0 {
		return res, nil
	}
	tickers, err := e.client.FetchTickers(ccxt.WithFetchTickersSymbols(symbols))
	if err != nil {
		return nil, err
	}
	for sym, t := range tickers.Tickers {
		res[sym] = TickerData{
			Symbol:      sym,
			Last:        f64(t.Last),
			QuoteVolume: f64(t.QuoteVolume),
		}
	}
	return res, nil
}

// FetchFundingRates 批量拉取当前资金费率，返回 map[内部币对]费率百分比（%）
// 注意：ccxt 返回的是小数（0.0001），这里统一乘 100 转成百分数（0.01），
// 上层所有模块都用百分数思考，与需求文档口径一致（0.05%、0.0100%）。
func (e *Exchange) FetchFundingRates(baseSymbols []string) (map[string]float64, error) {
	res := make(map[string]float64, len(baseSymbols))
	if len(baseSymbols) == 0 {
		return res, nil
	}
	ccxtSymbols := make([]string, len(baseSymbols))
	for i, s := range baseSymbols {
		ccxtSymbols[i] = BaseToSwap(s)
	}
	rates, err := e.client.FetchFundingRates(ccxt.WithFetchFundingRatesSymbols(ccxtSymbols))
	if err != nil {
		return nil, err
	}
	for sym, r := range rates.FundingRates {
		res[SwapToBase(sym)] = f64(r.FundingRate) * 100 // 小数 -> 百分数
	}
	return res, nil
}

// FetchFundingHistory 拉取某币对最近 limit 次资金费率（百分数，旧->新排序）
func (e *Exchange) FetchFundingHistory(baseSymbol string, limit int64) ([]float64, error) {
	hist, err := e.client.FetchFundingRateHistory(
		ccxt.WithFetchFundingRateHistorySymbol(BaseToSwap(baseSymbol)),
		ccxt.WithFetchFundingRateHistoryLimit(limit),
	)
	if err != nil {
		return nil, err
	}
	out := make([]float64, 0, len(hist))
	for _, h := range hist {
		out = append(out, f64(h.FundingRate)*100) // 小数 -> 百分数
	}
	return out, nil
}

// ---------- 账户相关（模块 4 使用） ----------

// FetchUSDTBalance 读取 USDT 余额（free/used/total）
func (e *Exchange) FetchUSDTBalance() (free, used, total float64, err error) {
	bal, err := e.client.FetchBalance()
	if err != nil {
		return 0, 0, 0, err
	}
	if v, ok := bal.Free["USDT"]; ok {
		free = f64(v)
	}
	if v, ok := bal.Used["USDT"]; ok {
		used = f64(v)
	}
	if v, ok := bal.Total["USDT"]; ok {
		total = f64(v)
	}
	return free, used, total, nil
}

// FetchCurrencyBalance 读取单个币种余额（持仓详情页现货腿要查“币”的数量）
func (e *Exchange) FetchCurrencyBalance(currency string) (free, used, total float64, err error) {
	bal, err := e.client.FetchBalance()
	if err != nil {
		return 0, 0, 0, err
	}
	if v, ok := bal.Free[currency]; ok {
		free = f64(v)
	}
	if v, ok := bal.Used[currency]; ok {
		used = f64(v)
	}
	if v, ok := bal.Total[currency]; ok {
		total = f64(v)
	}
	return free, used, total, nil
}

// FetchSwapPositions 拉取全部合约持仓（仅合约腿交易所使用）
func (e *Exchange) FetchSwapPositions() ([]model.SwapPositionInfo, error) {
	positions, err := e.client.FetchPositions()
	if err != nil {
		return nil, err
	}
	out := make([]model.SwapPositionInfo, 0) // 空数组而非 nil，JSON 返回 []
	for _, p := range positions {
		contracts := f64(p.Contracts)
		if contracts == 0 {
			continue // 过滤空仓
		}
		out = append(out, model.SwapPositionInfo{
			Exchange:         e.id,
			Symbol:           SwapToBase(str(p.Symbol)),
			Side:             str(p.Side),
			Contracts:        contracts,
			EntryPrice:       f64(p.EntryPrice),
			MarkPrice:        f64(p.MarkPrice),
			UnrealizedPnl:    f64(p.UnrealizedPnl),
			Leverage:         f64(p.Leverage),
			LiquidationPrice: f64(p.LiquidationPrice),
		})
	}
	return out, nil
}

// FetchADLRank 拉取某合约的 ADL（自动减仓）排名，1~5，数值越大越危险。
// 该功能为币安特有，通过类型断言调用；其它交易所返回错误。
func (e *Exchange) FetchADLRank(baseSymbol string) (int, error) {
	b, ok := e.raw.(*ccxt.Binance)
	if !ok {
		return 0, fmt.Errorf("%s 不支持 ADL 查询", e.id)
	}
	adls, err := b.FetchPositionsADLRank(ccxt.WithFetchPositionsADLRankParams(map[string]any{}))
	if err != nil {
		return 0, err
	}
	target := BaseToSwap(baseSymbol)
	for _, a := range adls {
		if str(a.Symbol) == target {
			return int(i64(a.Rank)), nil
		}
	}
	return 0, fmt.Errorf("未找到 %s 的 ADL 信息", baseSymbol)
}

// ---------- 交易相关（模块 3 使用） ----------

// SetLeverage 设置合约杠杆倍数（建仓前调用，失败不致命）
func (e *Exchange) SetLeverage(baseSymbol string, leverage int64) error {
	_, err := e.client.SetLeverage(leverage, ccxt.WithSetLeverageSymbol(BaseToSwap(baseSymbol)))
	return err
}

// MarketOrderResult 市价单成交结果
type MarketOrderResult struct {
	OrderID  string
	Filled   float64 // 成交币数量
	AvgPrice float64 // 成交均价
	Cost     float64 // 名义价值（USDT）
}

// CreateMarketOrder 下市价单。
// marketType: spot（内部币对 BTC/USDT）/ swap（自动转换为 BTC/USDT:USDT）。
// 个人低频套利工具，市价单简单可靠，秒级延迟可接受（需求文档明确不追求性能）。
func (e *Exchange) CreateMarketOrder(marketType, baseSymbol, side string, amount float64) (*MarketOrderResult, error) {
	return e.createMarketOrder(marketType, baseSymbol, side, amount, nil)
}

// CreateMarketOrderWithParams 下附带额外参数的市价单（合约端符号自动转换）。
// 典型用途：币安平空仓时传 {"reduceOnly": true}，确保只减仓不开新仓。
func (e *Exchange) CreateMarketOrderWithParams(baseSymbol, side string, amount float64, params map[string]any) (*MarketOrderResult, error) {
	return e.createMarketOrder("swap", baseSymbol, side, amount, params)
}

// createMarketOrder 市价单统一实现
func (e *Exchange) createMarketOrder(marketType, baseSymbol, side string, amount float64, params map[string]any) (*MarketOrderResult, error) {
	if amount <= 0 {
		return nil, errors.New("下单数量必须大于 0")
	}
	symbol := baseSymbol
	if marketType == "swap" {
		symbol = BaseToSwap(baseSymbol)
	}
	var order ccxt.Order
	var err error
	if params != nil {
		order, err = e.client.CreateOrder(symbol, "market", side, amount, ccxt.WithCreateOrderParams(params))
	} else {
		order, err = e.client.CreateOrder(symbol, "market", side, amount)
	}
	if err != nil {
		return nil, err
	}
	avg := f64(order.Average)
	if avg == 0 {
		avg = f64(order.Price) // 部分交易所市价单不返回均价，用价格字段兜底
	}
	filled := f64(order.Filled)
	if filled == 0 {
		filled = amount // 个别交易所不返回 filled，按请求数量处理
	}
	cost := f64(order.Cost)
	if cost == 0 {
		cost = filled * avg
	}
	return &MarketOrderResult{
		OrderID:  str(order.Id),
		Filled:   filled,
		AvgPrice: avg,
		Cost:     cost,
	}, nil
}

// AmountToPrecision 按交易所市场规则截断下单数量精度（避免被交易所拒单）。
// ccxt 的 AmountToPrecision 定义在 BaseExchange 上，通过嵌入提升到各交易所结构体。
func (e *Exchange) AmountToPrecision(marketType, baseSymbol string, amount float64) float64 {
	symbol := baseSymbol
	if marketType == "swap" {
		symbol = BaseToSwap(baseSymbol)
	}
	// 用匿名接口适配，避免强依赖具体交易所类型
	type precisioner interface {
		AmountToPrecision(symbol any, amount any) any
	}
	p, ok := e.raw.(precisioner)
	if !ok {
		return amount // 不支持则原样返回
	}
	switch v := p.AmountToPrecision(symbol, amount).(type) {
	case string:
		var f float64
		fmt.Sscanf(v, "%f", &f)
		return f
	case float64:
		return v
	case float32:
		return float64(v)
	}
	return amount
}

// ---------- 交易引擎与持仓详情所需（限价单 / 盘口 / 撤单 / 查单 / 资金费流水） ----------

// CreateLimitOrder 挂限价单（交易引擎的 Maker 模式使用）
func (e *Exchange) CreateLimitOrder(marketType, baseSymbol, side string, amount, price float64) (string, error) {
	if amount <= 0 || price <= 0 {
		return "", errors.New("限价单数量与价格必须大于 0")
	}
	symbol := baseSymbol
	if marketType == "swap" {
		symbol = BaseToSwap(baseSymbol)
	}
	order, err := e.client.CreateOrder(symbol, "limit", side, amount, ccxt.WithCreateOrderPrice(price))
	if err != nil {
		return "", err
	}
	return str(order.Id), nil
}

// CancelOrder 撤销指定订单（追价前先撤掉旧挂单）
func (e *Exchange) CancelOrder(marketType, baseSymbol, orderID string) error {
	symbol := baseSymbol
	if marketType == "swap" {
		symbol = BaseToSwap(baseSymbol)
	}
	_, err := e.client.CancelOrder(orderID, ccxt.WithCancelOrderSymbol(symbol))
	return err
}

// OrderStatus 订单状态查询结果（引擎轮询成交进度用）
type OrderStatus struct {
	Status      string  // open / closed / canceled
	Filled      float64 // 已成交币数量
	AvgPrice    float64 // 成交均价
	Fee         float64 // 手续费数值
	FeeCurrency string  // 手续费币种
}

// FetchOrderStatus 查询订单成交进度
func (e *Exchange) FetchOrderStatus(marketType, baseSymbol, orderID string) (*OrderStatus, error) {
	symbol := baseSymbol
	if marketType == "swap" {
		symbol = BaseToSwap(baseSymbol)
	}
	order, err := e.client.FetchOrder(orderID, ccxt.WithFetchOrderSymbol(symbol))
	if err != nil {
		return nil, err
	}
	// ccxt 的 Fee 结构只有 Rate/Cost 没有币种字段，尝试从原始返回 Info 里挖
	feeCurrency := ""
	if feeMap, ok := order.Info["fee"].(map[string]any); ok {
		if c, ok := feeMap["currency"].(string); ok {
			feeCurrency = c
		}
	}
	return &OrderStatus{
		Status:      str(order.Status),
		Filled:      f64(order.Filled),
		AvgPrice:    f64(order.Average),
		Fee:         f64(order.Fee.Cost),
		FeeCurrency: feeCurrency,
	}, nil
}

// FetchLevelPrice 取盘口某侧第 level 档价格（引擎 Maker 挂单/掉档判断用）。
// side=buy 看买盘 bids（前高后低），side=sell 看卖盘 asks（前低后高）；
// level 超出盘口深度时取最后一档。
func (e *Exchange) FetchLevelPrice(marketType, baseSymbol, side string, level int) (float64, error) {
	symbol := baseSymbol
	if marketType == "swap" {
		symbol = BaseToSwap(baseSymbol)
	}
	book, err := e.client.FetchOrderBook(symbol)
	if err != nil {
		return 0, err
	}
	levels := book.Asks
	if side == "buy" {
		levels = book.Bids
	}
	if len(levels) == 0 {
		return 0, errors.New("盘口为空")
	}
	if level < 1 {
		level = 1
	}
	if level > len(levels) {
		level = len(levels)
	}
	// 盘口档位结构：[价格, 数量]
	if len(levels[level-1]) == 0 {
		return 0, errors.New("盘口档位数据异常")
	}
	return levels[level-1][0], nil
}

// IsOutOfLevel 判断我的挂单价是否已“掉出前 N 档”（引擎追价条件）。
// 买单：挂单价 < 当前第 N 档买价 => 掉出；卖单：挂单价 > 当前第 N 档卖价 => 掉出。
func (e *Exchange) IsOutOfLevel(marketType, baseSymbol, side string, level int, myPrice float64) (bool, error) {
	levelPrice, err := e.FetchLevelPrice(marketType, baseSymbol, side, level)
	if err != nil {
		return false, err
	}
	if side == "buy" {
		return myPrice < levelPrice, nil
	}
	return myPrice > levelPrice, nil
}

// FundingPayment 一笔资金费结算（持仓详情页“资金费率流水”页签的数据）
type FundingPayment struct {
	ID       string    // 交易所侧流水号（去重入库用）
	Symbol   string    // 内部币对
	Amount   float64   // 收入为正 / 支出为负（USDT）
	Time     time.Time // 结算时间
}

// FetchFundingPayments 拉取账户资金费流水（仅合约腿交易所；需要凭证）
func (e *Exchange) FetchFundingPayments(baseSymbol string, limit int64) ([]FundingPayment, error) {
	opts := []ccxt.FetchFundingHistoryOptions{}
	if baseSymbol != "" {
		opts = append(opts, ccxt.WithFetchFundingHistorySymbol(BaseToSwap(baseSymbol)))
	}
	if limit > 0 {
		opts = append(opts, ccxt.WithFetchFundingHistoryLimit(limit))
	}
	hist, err := e.client.FetchFundingHistory(opts...)
	if err != nil {
		return nil, err
	}
	out := make([]FundingPayment, 0, len(hist))
	for _, h := range hist {
		out = append(out, FundingPayment{
			ID:     str(h.Id),
			Symbol: SwapToBase(str(h.Symbol)),
			Amount: f64(h.Amount),
			Time:   time.UnixMilli(i64(h.Timestamp)),
		})
	}
	return out, nil
}

// FetchLastPrice 获取单个币对最新价（spot 传 BTC/USDT，swap 传 BTC/USDT:USDT）
func (e *Exchange) FetchLastPrice(marketType, baseSymbol string) (float64, error) {
	symbol := baseSymbol
	if marketType == "swap" {
		symbol = BaseToSwap(baseSymbol)
	}
	t, err := e.client.FetchTicker(symbol)
	if err != nil {
		return 0, err
	}
	return f64(t.Last), nil
}

// ---------- 符号换算辅助 ----------

// BaseToSwap 内部币对 -> 币安 USDT 永续符号：BTC/USDT -> BTC/USDT:USDT
func BaseToSwap(base string) string {
	if strings.Contains(base, ":") {
		return base
	}
	return base + ":USDT"
}

// SwapToBase 合约符号 -> 内部币对：BTC/USDT:USDT -> BTC/USDT
func SwapToBase(swap string) string {
	if i := strings.Index(swap, ":"); i > 0 {
		return swap[:i]
	}
	return swap
}

// ---------- ccxt 指针字段安全解引用 ----------

func f64(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func i64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func boolv(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

// ---------- Hub：交易所连接管理器 ----------

// Hub 持有“现货腿 + 合约腿”两条连接，从数据库读取最新 API 凭证构建。
// 模块 1 保存新凭证后调用 Reload 即可热更新连接，无需重启程序。
//
// 另外持有两条【公开连接】（publicSpot/publicSwap）：
// 行情、资金费率等都是交易所公共接口，不需要 API 凭证。
// 需求文档第 3 条更新：即使没有配置账户 API，行情模块也必须可用（只是观看）。
type Hub struct {
	db   *database.DB
	mu   sync.RWMutex
	spot *Exchange // 现货腿交易所（默认 gate，带凭证，可交易）
	swap *Exchange // 合约腿交易所（默认 binance，带凭证，可交易）
	// 公开连接（无凭证，只读公共行情；构建失败不影响启动，调用时返回错误）
	publicSpot *Exchange
	publicSwap *Exchange
	// 角色 -> 交易所 ID 的映射，集中写死在一处，更换交易所只改这里
	spotID string
	swapID string
}

// NewHub 创建管理器（spotID/swapID 留空则默认 gate/binance），并立即构建公开连接
func NewHub(db *database.DB, spotID, swapID string) *Hub {
	if spotID == "" {
		spotID = "gate"
	}
	if swapID == "" {
		swapID = "binance"
	}
	h := &Hub{db: db, spotID: spotID, swapID: swapID}
	h.initPublic()
	return h
}

// initPublic 构建无凭证的公开连接（只用于公共行情接口）。
// 构建失败（如无网络）不致命：publicXxx 保持 nil，调用方会收到明确错误。
func (h *Hub) initPublic() {
	// 空凭证创建：ccxt 公共接口（行情/费率/盘口）不需要 Key
	if ex, err := New(h.spotID, "spot", "", ""); err == nil {
		h.publicSpot = ex
	}
	if ex, err := New(h.swapID, "swap", "", ""); err == nil {
		h.publicSwap = ex
	}
}

// PublicSpot 现货腿公开连接（行情模块使用，无需配置 API）
func (h *Hub) PublicSpot() (*Exchange, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.publicSpot == nil {
		return nil, errors.New("现货公开行情连接不可用（请检查网络）")
	}
	return h.publicSpot, nil
}

// PublicSwap 合约腿公开连接（行情模块使用，无需配置 API）
func (h *Hub) PublicSwap() (*Exchange, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.publicSwap == nil {
		return nil, errors.New("合约公开行情连接不可用（请检查网络）")
	}
	return h.publicSwap, nil
}

// Reload 从数据库读取 API 凭证，重建两条连接（凭证变更后调用）
func (h *Hub) Reload() error {
	spotKey, spotSecret, err := h.loadCredential(h.spotID)
	if err != nil {
		return fmt.Errorf("现货腿交易所(%s)凭证缺失: %w", h.spotID, err)
	}
	swapKey, swapSecret, err := h.loadCredential(h.swapID)
	if err != nil {
		return fmt.Errorf("合约腿交易所(%s)凭证缺失: %w", h.swapID, err)
	}

	spotEx, err := New(h.spotID, "spot", spotKey, spotSecret)
	if err != nil {
		return err
	}
	swapEx, err := New(h.swapID, "swap", swapKey, swapSecret)
	if err != nil {
		return err
	}

	h.mu.Lock()
	h.spot = spotEx
	h.swap = swapEx
	h.mu.Unlock()
	return nil
}

// loadCredential 从数据库读取某交易所最新一条凭证
func (h *Hub) loadCredential(exchangeID string) (key, secret string, err error) {
	row := h.db.QueryRow(
		`SELECT api_key, api_secret FROM exchange_api WHERE exchange = ? ORDER BY id DESC LIMIT 1`,
		exchangeID,
	)
	if err = row.Scan(&key, &secret); err != nil {
		return "", "", err
	}
	return key, secret, nil
}

// Spot 获取现货腿交易所连接
func (h *Hub) Spot() (*Exchange, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.spot == nil {
		return nil, errors.New("现货腿交易所未配置，请先在“API配置”页保存凭证")
	}
	return h.spot, nil
}

// Swap 获取合约腿交易所连接
func (h *Hub) Swap() (*Exchange, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.swap == nil {
		return nil, errors.New("合约腿交易所未配置，请先在“API配置”页保存凭证")
	}
	return h.swap, nil
}

// Ready 两条连接是否都已就绪
func (h *Hub) Ready() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.spot != nil && h.swap != nil
}
