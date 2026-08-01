// 【阅读顺序 05】交易所连接抽象层（全文最长但结构最简单）。
// 职责：把 ccxt 库的所有调用收敛成本项目统一的 Exchange 类型 + Hub 管理器，
// 上层模块只依赖本包，不直接碰 ccxt。这是【门面模式】：底层再复杂，只给上层简单入口。
// 连接分四类：现货腿/合约腿（带凭证可交易）、公开现货/公开合约（无凭证只读，行情模块用）。
// 语法点预览：interface、鸭子类型、类型断言、switch、map[string]any、any、方法接收者、
// 命名返回值、可变参数、指针安全解引用、sync.RWMutex。
package exchange

// import 导入用到的包。
import (
	"errors"  // 构造错误：errors.New
	"fmt"     // 格式化：fmt.Errorf / fmt.Sscanf
	"strings" // 字符串操作：strings.Contains / strings.Index / strings.Join
	"sync"    // 并发同步：sync.RWMutex（读写锁）
	"time"    // 时间：time.Time / time.UnixMilli

	ccxt "github.com/ccxt/ccxt/go/v4" // ccxt 交易所 SDK（第三方库，import 路径带版本）

	"deltacrypto/internal/crypto"   // 凭证加密
	"deltacrypto/internal/database" // 数据库
	"deltacrypto/internal/model"    // 数据结构
)

// ccxtClient 是 ccxt 各交易所（Binance/Gate）的公共方法集。
//
// 【什么是 interface（接口）？】
//
//	接口 = 一组【方法签名】的集合，不写实现。只要一个类型实现了这些方法，
//	它就【自动】满足这个接口（Go 叫鸭子类型，不需要写 implements）。
//
// 为什么需要接口？—— *ccxt.Binance 和 *ccxt.Gate 都由 ccxt 模板生成、方法签名一致，
// 用接口统一持有后，业务代码只依赖“方法集合”，换交易所不用改调用方。
// 这就是“面向接口编程”：依赖抽象，不依赖具体实现。
type ccxtClient interface {
	LoadMarkets(params ...any) (map[string]ccxt.MarketInterface, error)                                                             // 加载市场元信息
	FetchTime(params ...any) (int64, error)                                                                                         // 服务器时间
	FetchTicker(symbol string, options ...ccxt.FetchTickerOptions) (ccxt.Ticker, error)                                             // 单个行情
	FetchTickers(options ...ccxt.FetchTickersOptions) (ccxt.Tickers, error)                                                         // 批量行情
	FetchFundingRate(symbol string, options ...ccxt.FetchFundingRateOptions) (ccxt.FundingRate, error)                              // 单个费率
	FetchFundingRates(options ...ccxt.FetchFundingRatesOptions) (ccxt.FundingRates, error)                                          // 批量费率
	FetchFundingRateHistory(options ...ccxt.FetchFundingRateHistoryOptions) ([]ccxt.FundingRateHistory, error)                      // 历史费率
	FetchBalance(params ...any) (ccxt.Balances, error)                                                                              // 余额
	FetchPositions(options ...ccxt.FetchPositionsOptions) ([]ccxt.Position, error)                                                  // 持仓
	SetLeverage(leverage int64, options ...ccxt.SetLeverageOptions) (map[string]any, error)                                         // 设杠杆
	CreateOrder(symbol string, typeVar string, side string, amount float64, options ...ccxt.CreateOrderOptions) (ccxt.Order, error) // 下单
	// —— 以下为交易引擎与持仓详情所需（盘口/限价单/撤单/查单/资金费流水）——
	FetchOrderBook(symbol string, options ...ccxt.FetchOrderBookOptions) (ccxt.OrderBook, error)   // 盘口
	FetchOrder(id string, options ...ccxt.FetchOrderOptions) (ccxt.Order, error)                   // 查单
	CancelOrder(id string, options ...ccxt.CancelOrderOptions) (ccxt.Order, error)                 // 撤单
	FetchFundingHistory(options ...ccxt.FetchFundingHistoryOptions) ([]ccxt.FundingHistory, error) // 资金费流水
	// 注意函数签名里“options ...xxx”也是可变参数，ccxt 用一堆 WithXxx() 选项函数组装参数。
}

// Exchange 项目内统一的交易所连接对象。
// 为什么同时留 client 和 raw 两个字段？——
//
//	client：统一接口，99% 的功能靠它；
//	raw：个别交易所的独有功能（如币安 ADL 排名、精度换算）接口里没有，
//	     必须拿到具体类型才能调，所以用 any 存原对象、需要时【类型断言】。
type Exchange struct {
	id     string     // string 交易所标识：binance / gate
	role   string     // string 承担角色：spot（现货腿）/ swap（合约腿）
	client ccxtClient // ccxtClient 统一接口
	raw    any        // any（= interface{}）：原始 ccxt 对象，可以是任何类型
}

// proxyURL 进程级代理设置（main 启动时注入）。
// 为什么用【包级变量】而不是 Exchange 的字段？—— 代理对“所有连接”一视同仁，
// 放包里让所有实例共享最省事；只在启动时写一次、之后只读，所以不用加锁。
var proxyURL string // var 声明变量；string 类型；没初始化 = 空串 ""（string 零值）

// SetProxy 设置全局代理（如 http://127.0.0.1:7890），空串表示直连。
// 函数签名：入参 url（string），无返回值。
func SetProxy(url string) { // 注意“func 名(参数) 返回值”
	proxyURL = url // = 赋值：把 url 的值写入全局变量（和 := 的区别：= 不声明新变量）
}

// newCcxtClient 按交易所 ID 构造 ccxt 客户端。新增交易所就在这加一个 case。
// 函数签名：入参 (id, role, apiKey, secret 四个 string)，返回三个值：(接口, 原对象, 错误)。
func newCcxtClient(id, role, apiKey, secret string) (ccxtClient, any, error) {
	// map[string]any{...} 复合字面量：初始化一个 map（键是 string，值可以是任何类型）。
	// 为什么用 map[string]any？—— 交易所配置各项类型不同（string/布尔/嵌套 map），
	// 只有“值可以是任何类型”的 map 装得下。
	cfg := map[string]any{
		"apiKey":          apiKey, // 键 "apiKey" 对应值 apiKey（变量）
		"secret":          secret, // 键 "secret" 对应值 secret
		"enableRateLimit": true,   // ccxt 内置限速：请求按交易所要求的最小间隔排队，防封禁
	}
	// 有代理就注入（ccxt 的 httpsProxy 配置项，对所有 HTTPS 请求生效）。
	if proxyURL != "" { // 判断全局变量非空
		cfg["httpsProxy"] = proxyURL // 往 map 里加一项：键 "httpsProxy"，值 proxyURL
	}
	switch id { // 【switch 分支选择】：按 id 的值分发到不同 case
	case "binance": // 如果 id == "binance"
		// 合约腿：限定只加载 USDT 永续市场。
		// 为什么限定？—— ① 避免请求被墙最严重的主站 api.binance.com（现货接口），
		// 合约走 fapi.binance.com 连通性更好；② 少加载几千个无用市场，启动更快。
		if role == "swap" { // 只有合约腿才加这些选项；现货腿（公开连接）不需要
			cfg["options"] = map[string]any{"defaultType": "swap", "fetchMarkets": []any{"swap"}}
		}
		ex := ccxt.NewBinance(cfg) // 用配置创建币安对象
		return ex, ex, nil         // 返回 (接口, 原对象, nil)。nil 表示没有错误
		// ⚠️ 注意 Go 的 switch 每个 case 执行完【自动跳出】，不用写 break（不像 C/Java）。
	case "gate": // 如果 id == "gate"
		// 现货腿：同理限定只加载现货市场，顺带规避 gate 其它市场类型的解析问题。
		cfg["options"] = map[string]any{"defaultType": "spot", "fetchMarkets": []any{"spot"}}
		ex := ccxt.NewGate(cfg)
		return ex, ex, nil
	default: // 其它任何值
		// fmt.Errorf：构造带格式的错误。%s 是字符串占位符。
		return nil, nil, fmt.Errorf("暂不支持的交易所: %s", id)
	}
}

// New 创建并初始化一个交易所连接。
func New(id, role, apiKey, secret string) (*Exchange, error) {
	// 先构造 ccxt 客户端；“:=” 声明 client 和 raw 两个新变量。
	client, raw, err := newCcxtClient(id, role, apiKey, secret)
	if err != nil { // 构造失败
		return nil, err // 返回 (nil, 错误)——调用方检查 err
	}
	// “&Exchange{...}” 复合字面量：分配 Exchange 并初始化字段。
	e := &Exchange{id: id, role: role, client: client, raw: raw}
	// 预加载市场表：价格精度/数量精度/最小下单量等元信息，后续 AmountToPrecision
	// 等都要靠它。ccxt 会缓存这份数据，所以这里拉一次就够了。
	if _, err := client.LoadMarkets(); err != nil { // “if err := ...; err != nil” 带初始化
		return nil, fmt.Errorf("%s 加载市场信息失败: %w", id, err) // %w 包装错误
	}
	return e, nil // 成功：返回 (连接对象, nil)
}

// ID 返回交易所标识。
// “(e *Exchange)” 是【方法接收者】：把函数挂到结构体上变成方法（e.ID() 这样调）。
// *Exchange 是指针接收者——操作原对象；如果是 (e Exchange) 值接收者则操作副本。
// 为什么这里用指针接收者？—— 项目里所有方法统一用指针，保持风格一致（混用是坏味道）。
func (e *Exchange) ID() string { return e.id }

// Role 返回承担角色（spot/swap）。
func (e *Exchange) Role() string { return e.role }

// ---------- 连通性 / 权限测试（模块 1 使用） ----------

// TestPublic 测试公共接口连通性：拉服务器时间，能拉到说明网络通。
// 方法签名：(e *Exchange) 接收者，无入参，返回 error。
func (e *Exchange) TestPublic() error {
	_, err := e.client.FetchTime() // “_” 丢弃第一个返回值（时间戳），只关心 err
	return err                     // 把 err 原样返回，调用方判断 nil / 非 nil
}

// TestPrivate 测试私有接口权限：读余额，能读到说明 Key 有效且给了读取权限。
func (e *Exchange) TestPrivate() error {
	// FetchBalance() 返回两个值：(余额数据, error)。
	// 这里“_”把第一个值（余额数据）丢弃——测试权限只需要知道“有没有报错”，
	// 不需要真的看余额内容。第二个值 err 是我们关心的。
	_, err := e.client.FetchBalance()
	return err // 把 err 原样返回给调用方：调用方判断 nil（成功）/ 非 nil（失败）
}

// ---------- 行情相关（模块 2 使用） ----------

// TickerData 项目内简化后的行情数据。
// 为什么自己再定义一层而不是直接用 ccxt.Ticker？—— ccxt 的 Ticker 字段太多（几十个），
// 本项目只关心 3 个。定义精简结构体 = 把外部世界的噪音过滤掉（这叫【防腐层】）。
type TickerData struct {
	Symbol      string  // string ccxt 标准符号（现货 BTC/USDT，合约 BTC/USDT:USDT）
	Last        float64 // float64 最新价
	QuoteVolume float64 // float64 24H 成交额（USDT）
}

// SwapSymbols 返回该交易所全部“USDT 永续合约”的内部币对（BTC/USDT，已去掉 :USDT 后缀）。
// 返回 []string（字符串切片）——返回列表时用切片。
func (e *Exchange) SwapSymbols() ([]string, error) {
	markets, err := e.client.LoadMarkets() // LoadMarkets 有缓存，不会真的重新发请求
	if err != nil {
		return nil, err
	}
	var out []string              // var 声明一个空切片（此时是 nil 切片，append 会自动分配底层数组）
	for _, mkt := range markets { // 遍历市场 map：mkt 是每个市场对象
		// ⚠️ mkt 的类型叫 ccxt.MarketInterface——名字带 Interface，但它其实是一个
		// 【结构体】，直接取字段就行（mkt.Type、mkt.QuoteCurrency...）。这是 ccxt
		// Go 版本的历史命名，容易误导新手，知道就行。
		// str()/boolv() 是文件末尾的“指针安全解引用”小工具：ccxt 字段是 *string/*bool，
		// nil 时直接解引用会 panic，这些函数把 nil 检查收敛到一处。
		if str(mkt.Type) == "swap" && str(mkt.QuoteCurrency) == "USDT" && boolv(mkt.Active) {
			// 三个条件同时成立（&& 表示“并且”）：是永续合约 && 结算货币是 USDT && 处于激活状态
			out = append(out, SwapToBase(str(mkt.Symbol))) // append 往切片末尾加元素
			// ⚠️ 必须把 append 的返回值赋回去（out = append(...)），因为 append 可能扩容
			// 返回新的底层数组。SwapToBase：合约符号 BTC/USDT:USDT → 内部 BTC/USDT。
		}
	}
	return out, nil // 返回 (切片, nil)
}

// SpotSymbols 返回该交易所全部“USDT 现货”的内部币对（BTC/USDT）。
func (e *Exchange) SpotSymbols() ([]string, error) {
	markets, err := e.client.LoadMarkets()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, mkt := range markets { // 逻辑和 SwapSymbols 一样，只是过滤条件是“现货”
		if str(mkt.Type) == "spot" && str(mkt.QuoteCurrency) == "USDT" && boolv(mkt.Active) {
			out = append(out, str(mkt.Symbol)) // 现货符号不需要转换（本来就没有冒号）
		}
	}
	return out, nil
}

// FetchTickers 批量拉行情。spot 传 ["BTC/USDT"]，swap 传 ["BTC/USDT:USDT"]。
func (e *Exchange) FetchTickers(symbols []string) (map[string]TickerData, error) {
	// make + 预分配容量：先初始化空 map 再给容量提示。
	// ⚠️ 为什么不能直接 var res map[...]... 再往里写？—— 那样 res 是 nil，
	// 往 nil map 写会 panic。map 必须 make 或字面量初始化才能写。
	res := make(map[string]TickerData, len(symbols))
	if len(symbols) == 0 { // len() 取切片长度
		return res, nil // 空输入返回空 map，调用方 for range 不会崩
	}
	// ccxt.WithFetchTickersSymbols(symbols)：ccxt 的【选项模式】写法。
	// ccxt 里很多方法接受“...选项”参数，但它不用传一个配置结构体，而是用一堆
	// 带 With 前缀的小函数（WithXxx）逐个指定选项，再展开传进去。这句的意思是
	// “本次要查的币对是 symbols”。后面见到的 WithFetchFundingRateHistorySymbol、
	// WithCreateOrderPrice 等都是同一套路：用函数把单个参数包成一个选项。
	tickers, err := e.client.FetchTickers(ccxt.WithFetchTickersSymbols(symbols))
	if err != nil {
		return nil, err
	}
	for sym, t := range tickers.Tickers { // 遍历行情 map：sym=符号，t=行情对象
		res[sym] = TickerData{ // 往结果 map 里塞：用项目自己的精简结构体
			Symbol:      sym,         // 符号
			Last:        f64(t.Last), // t.Last 是 *float64（指针），f64() 安全取值
			QuoteVolume: f64(t.QuoteVolume),
		}
	}
	return res, nil
}

// FetchFundingRates 批量拉当前资金费率，返回 map[内部币对]费率百分比（%）。
// ⚠️ 单位换算：ccxt 返回小数（0.0001），这里 ×100 统一成百分数（0.01），
// 上层所有模块、设置页阈值、需求文档都用百分数思考——口径统一是防 bug 的关键。
func (e *Exchange) FetchFundingRates(baseSymbols []string) (map[string]float64, error) {
	res := make(map[string]float64, len(baseSymbols)) // 初始化空 map
	if len(baseSymbols) == 0 {
		return res, nil // 空输入返回空 map
	}
	ccxtSymbols := make([]string, len(baseSymbols)) // 分配同长度的切片
	for i, s := range baseSymbols {                 // 遍历：i=下标，s=元素
		ccxtSymbols[i] = BaseToSwap(s) // 内部币对 → 合约符号再请求（写回下标 i 位置）
	}
	rates, err := e.client.FetchFundingRates(ccxt.WithFetchFundingRatesSymbols(ccxtSymbols))
	if err != nil {
		return nil, err
	}
	for sym, r := range rates.FundingRates { // 遍历费率 map
		res[SwapToBase(sym)] = f64(r.FundingRate) * 100 // 键转回内部币对，值 ×100 变百分数
	}
	return res, nil
}

// FetchFundingHistory 拉某币对最近 limit 次历史费率（百分数，旧->新排序）。
// 历史费率是 market 模块判断“趋势是否上升”的数据源。
func (e *Exchange) FetchFundingHistory(baseSymbol string, limit int64) ([]float64, error) {
	hist, err := e.client.FetchFundingRateHistory(
		ccxt.WithFetchFundingRateHistorySymbol(BaseToSwap(baseSymbol)), // 币对（转合约符号）
		ccxt.WithFetchFundingRateHistoryLimit(limit),                   // 条数限制
	)
	if err != nil {
		return nil, err
	}
	out := make([]float64, 0, len(hist)) // 空切片，但预分配容量 len(hist)（性能优化）
	for _, h := range hist {             // 遍历历史费率
		out = append(out, f64(h.FundingRate)*100) // 每个都 ×100 转百分数
	}
	return out, nil
}

// ---------- 账户相关（模块 4 使用） ----------

// FetchUSDTBalance 读取 USDT 余额（free=可用 / used=冻结 / total=合计）。
// 注意签名是【命名返回值】：free, used, total float64, err 都在函数头声明了名字，
// 函数体里可以直接给它们赋值，最后“裸 return”会按名字返回这些值。
func (e *Exchange) FetchUSDTBalance() (free, used, total float64, err error) {
	bal, err := e.client.FetchBalance() // 拉余额
	if err != nil {
		return 0, 0, 0, err // 出错：返回 4 个零值/错误
	}
	// “v, ok := bal.Free["USDT"]” —— map 取值的 ok 惯用法：ok 表示该键是否存在。
	// 余额里没有 USDT（说明一分钱没有）时 ok=false，v 是零值 0，跳过赋值。
	if v, ok := bal.Free["USDT"]; ok {
		free = f64(v) // 给命名返回值 free 赋值
	}
	if v, ok := bal.Used["USDT"]; ok {
		used = f64(v) // f64() 是文件末尾的“安全取字段”工具：v 是 *float64 可能为 nil，它负责 nil 检查
	}
	if v, ok := bal.Total["USDT"]; ok {
		total = f64(v) // 同上：安全取 total 字段（nil → 0）
	}
	return free, used, total, nil // 成功返回
}

// FetchCurrencyBalance 读取单个币种余额（持仓详情页现货腿要查“币”的数量）。
func (e *Exchange) FetchCurrencyBalance(currency string) (free, used, total float64, err error) {
	bal, err := e.client.FetchBalance()
	if err != nil {
		return 0, 0, 0, err
	}
	// 又是 map 取值的 ok 惯用法：“v, ok := m[key]” 一次拿到值和“是否存在”两个信息。
	// 这里按传入的币种名（比如 "BTC"）去余额里找；币种不存在时 ok=false，跳过赋值。
	if v, ok := bal.Free[currency]; ok { // 可用余额里有这个币
		free = f64(v) // 取出来赋给命名返回值 free
	}
	if v, ok := bal.Used[currency]; ok {
		used = f64(v) // f64() 是文件末尾的“安全取字段”工具：v 是 *float64 可能为 nil，它负责 nil 检查
	}
	if v, ok := bal.Total[currency]; ok {
		total = f64(v) // 同上：安全取 total 字段（nil → 0）
	}
	return free, used, total, nil
}

// FetchSwapPositions 拉全部合约持仓（仅合约腿交易所使用）。
func (e *Exchange) FetchSwapPositions() ([]model.SwapPositionInfo, error) {
	positions, err := e.client.FetchPositions()
	if err != nil {
		return nil, err
	}
	// make([]T, 0) 而非 nil：空切片序列化成 JSON 是 []，nil 是 null。
	// ⚠️ Go 后端经典坑——前端拿 null 去遍历会报错，全项目统一用空数组规避。
	out := make([]model.SwapPositionInfo, 0)
	for _, p := range positions { // 遍历持仓
		contracts := f64(p.Contracts) // 先取出持仓数量
		if contracts == 0 {           // 持仓数为 0
			continue // 跳过：交易所会返回“0 持仓”的行，没意义（continue=进入下一轮循环）
		}
		out = append(out, model.SwapPositionInfo{ // 转成项目自己的结构体
			Exchange:         e.id,
			Symbol:           SwapToBase(str(p.Symbol)), // 合约符号转内部币对
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

// FetchADLRank 拉某合约的 ADL（自动减仓）排名，1~5，越大越危险（币安特有功能）。
func (e *Exchange) FetchADLRank(baseSymbol string) (int, error) {
	// 【类型断言】e.raw 存的是 any，要还原成具体类型才能调独有方法：
	// “b, ok := e.raw.(*ccxt.Binance)” —— ok=true 说明确实是 Binance，b 是可用的
	// Binance 对象；ok=false（比如 raw 是 Gate）则返回“不支持”。
	// ⚠️ 必须检查 ok！直接 e.raw.(*ccxt.Binance) 断言失败会 panic。
	b, ok := e.raw.(*ccxt.Binance)
	if !ok { // 断言失败
		return 0, fmt.Errorf("%s 不支持 ADL 查询", e.id)
	}
	// 这是 Binance 独有的方法，必须拿到具体类型才能调——这就是保留 raw 字段的原因。
	adls, err := b.FetchPositionsADLRank(ccxt.WithFetchPositionsADLRankParams(map[string]any{}))
	if err != nil {
		return 0, err
	}
	target := BaseToSwap(baseSymbol) // 要查的币对（合约符号）
	for _, a := range adls {         // 遍历 ADL 排名列表
		if str(a.Symbol) == target { // 找到目标币对
			return int(i64(a.Rank)), nil // 返回排名（int(i64(...)) 是类型转换：int64→int）
		}
	}
	return 0, fmt.Errorf("未找到 %s 的 ADL 信息", baseSymbol)
}

// ---------- 交易相关（模块 3 使用） ----------

// SetLeverage 设置合约杠杆倍数（建仓前调用，失败不致命——调用方会降级为警告）。
func (e *Exchange) SetLeverage(baseSymbol string, leverage int64) error {
	_, err := e.client.SetLeverage(leverage, ccxt.WithSetLeverageSymbol(BaseToSwap(baseSymbol)))
	return err
}

// MarketOrderResult 市价单成交结果（项目自定义结构体）。
type MarketOrderResult struct {
	OrderID  string  // string 订单号
	Filled   float64 // float64 实际成交币数量
	AvgPrice float64 // float64 成交均价
	Cost     float64 // float64 名义价值（USDT）
}

// CreateMarketOrder 下市价单。marketType: spot（BTC/USDT）/ swap（自动转 BTC/USDT:USDT）。
// 为什么保留市价单？—— 低频工具对毫秒级延迟无所谓，市价单简单可靠，作为
// 引擎的 Taker 兜底（见 trade/engine.go）。
func (e *Exchange) CreateMarketOrder(marketType, baseSymbol, side string, amount float64) (*MarketOrderResult, error) {
	return e.createMarketOrder(marketType, baseSymbol, side, amount, nil) // 转发给统一实现，params 传 nil
}

// CreateMarketOrderWithParams 下带额外参数的市价单（合约端符号自动转换）。
// 典型用途：平空仓时传 {"reduceOnly": true}，确保只减仓、绝不开新仓。
func (e *Exchange) CreateMarketOrderWithParams(baseSymbol, side string, amount float64, params map[string]any) (*MarketOrderResult, error) {
	return e.createMarketOrder("swap", baseSymbol, side, amount, params) // 固定 marketType=swap
}

// createMarketOrder 市价单统一实现。两个公开方法都转到这里，少一份重复代码。
func (e *Exchange) createMarketOrder(marketType, baseSymbol, side string, amount float64, params map[string]any) (*MarketOrderResult, error) {
	if amount <= 0 { // 前置校验：数量不合法直接拒绝（“早失败”模式）
		return nil, errors.New("下单数量必须大于 0") // errors.New：构造一个固定错误
	}
	// 变量 symbol 存的是“要发给交易所的币对符号”。
	// 默认用内部币对 BTC/USDT（现货格式）；但如果这条腿是合约（swap），
	// 必须转成合约符号 BTC/USDT:USDT 才能被交易所识别。
	// 为什么内部统一用 BTC/USDT 记账？—— 现货和合约两边直接可比（取交集的前提），
	// 只在发请求前临时转成合约格式。这就是 BaseToSwap/SwapToBase 这套换算的存在意义。
	// 还是“符号转换”这个惯用法（本文件已见过多次，每次都值得再讲一遍）：
	// symbol 变量存的是“要发给交易所的币对符号”。默认用内部币对 BTC/USDT（现货格式）；
	// 但如果这条腿是合约（swap），必须转成合约符号 BTC/USDT:USDT 才能被交易所识别。
	// 为什么内部统一用 BTC/USDT？—— 现货/合约两边直接可比，只在发请求前临时转成合约格式。
	symbol := baseSymbol
	if marketType == "swap" {
		symbol = BaseToSwap(baseSymbol)
	}
	var order ccxt.Order // var 声明一个 ccxt.Order 零值变量（后面赋值）
	var err error        // 声明 error 零值（nil）
	if params != nil {   // 有额外参数（如 reduceOnly）
		order, err = e.client.CreateOrder(symbol, "market", side, amount, ccxt.WithCreateOrderParams(params))
	} else { // 无参数
		order, err = e.client.CreateOrder(symbol, "market", side, amount)
	}
	if err != nil {
		return nil, err
	}
	// —— 下面三个“兜底”是真实项目常态：不同交易所返回字段完整度不一样 ——
	avg := f64(order.Average) // 取成交均价
	if avg == 0 {
		avg = f64(order.Price) // 有的交易所市价单不返回均价，用价格字段顶上
	}
	filled := f64(order.Filled) // 安全取“已成交量”（*float64，可能 nil）
	if filled == 0 {
		filled = amount // 有的不返回已成交量，按请求数量算
	}
	cost := f64(order.Cost) // 安全取“名义价值”（*float64，可能 nil）
	if cost == 0 {
		cost = filled * avg // 用数量×均价估算
	}
	// 原则：对外部世界的不确定性做【防御式兜底】——缺字段用合理近似顶上，
	// 而不是直接报错。套利是真实资金操作，宁可近似也要给出可用的结果。
	return &MarketOrderResult{ // 返回指针：结构体不大，但 Go 惯例构造函数返回指针
		OrderID:  str(order.Id),
		Filled:   filled,
		AvgPrice: avg,
		Cost:     cost,
	}, nil
}

// AmountToPrecision 按交易所市场规则截断下单数量精度（避免被交易所拒单）。
// 比如交易所要求数量最多 5 位小数，传 1.234567 会被拒，这里截成 1.23456。
func (e *Exchange) AmountToPrecision(marketType, baseSymbol string, amount float64) float64 {
	// 还是“符号转换”这个惯用法（本文件已见过多次，每次都值得再讲一遍）：
	// symbol 变量存的是“要发给交易所的币对符号”。默认用内部币对 BTC/USDT（现货格式）；
	// 但如果这条腿是合约（swap），必须转成合约符号 BTC/USDT:USDT 才能被交易所识别。
	// 为什么内部统一用 BTC/USDT？—— 现货/合约两边直接可比，只在发请求前临时转成合约格式。
	symbol := baseSymbol
	if marketType == "swap" {
		symbol = BaseToSwap(baseSymbol)
	}
	// 【匿名接口】在函数内临时定义接口，只声明“需要有这个方法”。
	// 任何实现了 AmountToPrecision(symbol any, amount any) any 的类型都满足——
	// Binance 和 Gate 都满足，一个分支搞定，不用分别断言具体类型。
	type precisioner interface {
		AmountToPrecision(symbol any, amount any) any
	}
	p, ok := e.raw.(precisioner) // 类型断言：raw 是不是满足这个接口
	if !ok {
		return amount // 不支持则原样返回（宁可不截断也不能报错）
	}
	// 【类型开关】switch v := x.(type)——按实际类型分发。ccxt 返回值在不同版本/交易所
	// 可能是 string、float64 或 float32，逐个处理统一成 float64。
	// ⚠️ 注意：.(type) 这种语法【只能在 switch 里用】。
	switch v := p.AmountToPrecision(symbol, amount).(type) {
	case string: // 如果是 string 类型
		var f float64
		fmt.Sscanf(v, "%f", &f) // 解析字符串成 float64。&f 取 f 的地址（Sscanf 需要指针写入）
		return f
	case float64: // 如果本来就是 float64
		return v
	case float32: // 如果是 float32
		return float64(v) // 转成 float64
	}
	return amount // 其它未知类型，原样返回
}

// ---------- 交易引擎与持仓详情所需（限价单 / 盘口 / 撤单 / 查单 / 资金费流水） ----------

// CreateLimitOrder 挂限价单（交易引擎的 Maker 模式用）。
// 返回 (订单号 string, 错误)。
func (e *Exchange) CreateLimitOrder(marketType, baseSymbol, side string, amount, price float64) (string, error) {
	if amount <= 0 || price <= 0 { // “||” 表示“或者”：数量或价格任一不合法
		return "", errors.New("限价单数量与价格必须大于 0")
	}
	// 还是“符号转换”这个惯用法（本文件已见过多次，每次都值得再讲一遍）：
	// symbol 变量存的是“要发给交易所的币对符号”。默认用内部币对 BTC/USDT（现货格式）；
	// 但如果这条腿是合约（swap），必须转成合约符号 BTC/USDT:USDT 才能被交易所识别。
	// 为什么内部统一用 BTC/USDT？—— 现货/合约两边直接可比，只在发请求前临时转成合约格式。
	symbol := baseSymbol
	if marketType == "swap" {
		symbol = BaseToSwap(baseSymbol)
	}
	// 限价单：指定价格（第 3 个参数 "limit"）+ 价格通过 WithCreateOrderPrice 传。
	order, err := e.client.CreateOrder(symbol, "limit", side, amount, ccxt.WithCreateOrderPrice(price))
	if err != nil {
		return "", err
	}
	return str(order.Id), nil // 返回订单号
}

// CancelOrder 撤销指定订单（追价前先撤掉旧的挂单）。
func (e *Exchange) CancelOrder(marketType, baseSymbol, orderID string) error {
	// 还是“符号转换”这个惯用法（本文件已见过多次，每次都值得再讲一遍）：
	// symbol 变量存的是“要发给交易所的币对符号”。默认用内部币对 BTC/USDT（现货格式）；
	// 但如果这条腿是合约（swap），必须转成合约符号 BTC/USDT:USDT 才能被交易所识别。
	// 为什么内部统一用 BTC/USDT？—— 现货/合约两边直接可比，只在发请求前临时转成合约格式。
	symbol := baseSymbol
	if marketType == "swap" {
		symbol = BaseToSwap(baseSymbol)
	}
	_, err := e.client.CancelOrder(orderID, ccxt.WithCancelOrderSymbol(symbol))
	return err
}

// OrderStatus 订单状态查询结果（引擎轮询成交进度用）。
type OrderStatus struct {
	Status      string  // string open / closed / canceled
	Filled      float64 // float64 已成交币数量
	AvgPrice    float64 // float64 成交均价
	Fee         float64 // float64 手续费数值
	FeeCurrency string  // string 手续费币种
}

// FetchOrderStatus 查询订单成交进度。
func (e *Exchange) FetchOrderStatus(marketType, baseSymbol, orderID string) (*OrderStatus, error) {
	// 还是“符号转换”这个惯用法（本文件已见过多次，每次都值得再讲一遍）：
	// symbol 变量存的是“要发给交易所的币对符号”。默认用内部币对 BTC/USDT（现货格式）；
	// 但如果这条腿是合约（swap），必须转成合约符号 BTC/USDT:USDT 才能被交易所识别。
	// 为什么内部统一用 BTC/USDT？—— 现货/合约两边直接可比，只在发请求前临时转成合约格式。
	symbol := baseSymbol
	if marketType == "swap" {
		symbol = BaseToSwap(baseSymbol)
	}
	order, err := e.client.FetchOrder(orderID, ccxt.WithFetchOrderSymbol(symbol))
	if err != nil {
		return nil, err
	}
	// ccxt 的 Fee 结构只有 Rate/Cost 没有币种字段，所以从原始响应 order.Info 里“挖”。
	// ⚠️ 防御式解析：不同交易所响应结构不同，order.Info["fee"] 可能不是 map，
	// 用两次断言（外层 map、内层 string）保证解析失败也只是留空，不 panic。
	feeCurrency := ""                                         // 先给空串（string 零值）
	if feeMap, ok := order.Info["fee"].(map[string]any); ok { // 外层断言：是 map
		if c, ok := feeMap["currency"].(string); ok { // 内层断言：是 string
			feeCurrency = c // 取到币种
		}
	}
	return &OrderStatus{ // 组装结果返回
		Status:      str(order.Status),
		Filled:      f64(order.Filled),
		AvgPrice:    f64(order.Average),
		Fee:         f64(order.Fee.Cost),
		FeeCurrency: feeCurrency,
	}, nil
}

// FetchLevelPrice 取盘口某侧第 level 档的价格（引擎 Maker 挂单/掉档判断用）。
// 盘口分两侧：买盘 Bids 按价从高到低排（第1档=最高买价），卖盘 Asks 从低到高。
// 所以“买”看 Bids、“卖”看 Asks。
func (e *Exchange) FetchLevelPrice(marketType, baseSymbol, side string, level int) (float64, error) {
	// 还是“符号转换”这个惯用法（本文件已见过多次，每次都值得再讲一遍）：
	// symbol 变量存的是“要发给交易所的币对符号”。默认用内部币对 BTC/USDT（现货格式）；
	// 但如果这条腿是合约（swap），必须转成合约符号 BTC/USDT:USDT 才能被交易所识别。
	// 为什么内部统一用 BTC/USDT？—— 现货/合约两边直接可比，只在发请求前临时转成合约格式。
	symbol := baseSymbol
	if marketType == "swap" {
		symbol = BaseToSwap(baseSymbol)
	}
	book, err := e.client.FetchOrderBook(symbol) // 拉盘口
	if err != nil {
		return 0, err
	}
	levels := book.Asks // 默认看卖盘
	if side == "buy" {
		levels = book.Bids // 买单看买盘
	}
	if len(levels) == 0 { // 盘口为空
		return 0, errors.New("盘口为空")
	}
	// 档位越界就取最后一档：盘口深度可能小于 level，保守取最后一档比报错友好。
	if level < 1 {
		level = 1
	}
	if level > len(levels) {
		level = len(levels)
	}
	// 盘口每档结构是 [价格, 数量] 的切片，第 0 个元素是价格。
	if len(levels[level-1]) == 0 {
		return 0, errors.New("盘口档位数据异常")
	}
	return levels[level-1][0], nil // 返回第 level 档的价格
}

// IsOutOfLevel 判断我的挂单价是否已“掉出前 N 档”（引擎的追价触发条件）。
// 买单：我的买价 < 当前第 N 档买价 → 行情涨上去了，我掉队了；卖单反之。
func (e *Exchange) IsOutOfLevel(marketType, baseSymbol, side string, level int, myPrice float64) (bool, error) {
	levelPrice, err := e.FetchLevelPrice(marketType, baseSymbol, side, level) // 取第 N 档价
	if err != nil {
		return false, err
	}
	if side == "buy" {
		return myPrice < levelPrice, nil // 买单掉出 = 我的价低于第 N 档买价
	}
	return myPrice > levelPrice, nil // 卖单掉出 = 我的价高于第 N 档卖价
}

// FundingPayment 一笔资金费结算（详情页“资金费率流水”页签的数据）。
type FundingPayment struct {
	ID     string    // string 交易所侧流水号（去重入库用）
	Symbol string    // string 内部币对
	Amount float64   // float64 收入为正 / 支出为负（USDT）
	Time   time.Time // time.Time 结算时间
}

// FetchFundingPayments 拉账户资金费流水（仅合约腿；需要凭证）。
func (e *Exchange) FetchFundingPayments(baseSymbol string, limit int64) ([]FundingPayment, error) {
	opts := []ccxt.FetchFundingHistoryOptions{} // 空切片，攒所有可选项
	// 两个条件各自可选：传空 baseSymbol 表示拉全部币对，limit<=0 表示用默认数量。
	if baseSymbol != "" {
		opts = append(opts, ccxt.WithFetchFundingHistorySymbol(BaseToSwap(baseSymbol)))
	}
	if limit > 0 {
		opts = append(opts, ccxt.WithFetchFundingHistoryLimit(limit))
	}
	hist, err := e.client.FetchFundingHistory(opts...) // opts... 是“展开切片”：把切片元素逐个当参数
	if err != nil {
		return nil, err
	}
	// make([]FundingPayment, 0, len(hist))：空切片 + 预分配容量。
	// 空切片保证 JSON 输出 []（不是 null，前端遍历安全）；len(hist) 是容量提示，
	// 让底层数组一开始就够大，避免 append 反复扩容（性能优化）。
	out := make([]FundingPayment, 0, len(hist))
	for _, h := range hist {
		out = append(out, FundingPayment{
			ID:     str(h.Id),
			Symbol: SwapToBase(str(h.Symbol)), // 合约符号转内部币对
			Amount: f64(h.Amount),
			Time:   time.UnixMilli(i64(h.Timestamp)), // 毫秒时间戳 → time.Time（转换函数）
		})
	}
	return out, nil
}

// FetchLastPrice 获取单个币对最新价（spot 传 BTC/USDT，swap 传 BTC/USDT:USDT）。
func (e *Exchange) FetchLastPrice(marketType, baseSymbol string) (float64, error) {
	// 还是“符号转换”这个惯用法（本文件已见过多次，每次都值得再讲一遍）：
	// symbol 变量存的是“要发给交易所的币对符号”。默认用内部币对 BTC/USDT（现货格式）；
	// 但如果这条腿是合约（swap），必须转成合约符号 BTC/USDT:USDT 才能被交易所识别。
	// 为什么内部统一用 BTC/USDT？—— 现货/合约两边直接可比，只在发请求前临时转成合约格式。
	symbol := baseSymbol
	if marketType == "swap" {
		symbol = BaseToSwap(baseSymbol)
	}
	t, err := e.client.FetchTicker(symbol) // 拉单个行情
	if err != nil {
		return 0, err
	}
	return f64(t.Last), nil // 取最新价
}

// ---------- 符号换算辅助 ----------

// BaseToSwap 内部币对 → 合约符号：BTC/USDT -> BTC/USDT:USDT。
// 为什么需要这套换算？—— 现货和合约在交易所里的命名不同：现货 BTC/USDT，
// 合约 BTC/USDT:USDT（冒号后是结算货币）。项目内部统一用“现货式”BTC/USDT 记账
// （方便现货/合约直接比较），只在发请求前临时转成合约符号。
func BaseToSwap(base string) string {
	if strings.Contains(base, ":") { // Contains：判断字符串里是否含子串
		return base // 已经是合约符号，原样返回（幂等）
	}
	return base + ":USDT" // 字符串拼接：+ 号
}

// SwapToBase 合约符号 → 内部币对：BTC/USDT:USDT -> BTC/USDT。
func SwapToBase(swap string) string {
	if i := strings.Index(swap, ":"); i > 0 { // Index：找冒号的下标位置；if 带初始化语句
		return swap[:i] // 【切片语法】swap[:i] = 取下标 0 到 i-1 的部分（冒号前的字符）
	}
	return swap
}

// ---------- ccxt 指针字段安全解引用 ----------

// 为什么需要这 4 个小函数？—— ccxt 用指针（*float64/*string）表达“字段可能没有”。
// 直接 *p 解引用时 p==nil 会 panic。这 4 个函数把 nil 检查收敛到一处：
// nil → 零值（0 / "" / false），非 nil → 真值。看到 f64(...) 就是在“安全取字段”。
func f64(p *float64) float64 { // 入参是 *float64（float64 指针）
	if p == nil { // 判断指针是否为空
		return 0 // 空 → 返回 0
	}
	return *p // 非空 → *p 解引用取真值（“*” 在变量前=解引用）
}

func str(p *string) string {
	if p == nil {
		return "" // 空 → 返回空串
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
		return false // 空 → 返回 false
	}
	return *p
}

// ---------- Hub：交易所连接管理器 ----------

// Hub 持有“现货腿 + 合约腿”两条带凭证连接，以及两条无凭证的公开行情连接。
// 为什么需要这个管理器？——
//
//	a) 凭证存在数据库里，什么时候建连、怎么建？由 Hub 统一负责（Reload 读库建连）；
//	b) 模块按角色取连接只调一个方法：Spot()/Swap()/PublicSpot()/PublicSwap()；
//	c) 用锁保护并发：自动交易循环和 HTTP 请求可能同时来取/换连接。
type Hub struct {
	db   *database.DB
	mu   sync.RWMutex // 【读写锁】：读者可并发（取连接很频繁），写者独占（Reload 换连接罕见）
	spot *Exchange    // *Exchange 现货腿交易所（默认 gate，带凭证，可交易）
	swap *Exchange    // *Exchange 合约腿交易所（默认 binance，带凭证，可交易）
	// —— 公开连接（无凭证，只读公共行情；构建失败不影响启动，调用时返回明确错误）——
	publicSpot *Exchange
	publicSwap *Exchange
	publicErr  error // error 公开连接构建失败的原因（启动日志展示用）
	// —— 角色 → 交易所 ID 的映射，集中写死一处，换交易所只改这里 ——
	spotID  string
	swapID  string
	keyring *crypto.Keyring // 密钥环：读库时解密 API Secret（配合 apiconfig 的加密存储）
}

// NewHub 创建管理器（spotID/swapID 传空串则用默认 gate/binance），并立即构建公开连接。
func NewHub(db *database.DB, spotID, swapID string, keyring *crypto.Keyring) *Hub {
	// Go 没有函数重载（同名不同参数），用“空串 → 默认值”变通实现“可选参数”。
	if spotID == "" {
		spotID = "gate" // 默认现货腿 = gate
	}
	if swapID == "" {
		swapID = "binance" // 默认合约腿 = binance
	}
	h := &Hub{db: db, spotID: spotID, swapID: swapID, keyring: keyring} // 创建 Hub 对象，初始化基础字段
	// 公开连接此时就建：行情页“没配 API 也能看行情”就靠它。
	h.publicErr = h.initPublic() // 失败原因存进字段，main 启动日志用它提示用户
	return h
}

// PublicErr 返回公开连接的构建错误（无错为 nil）。main 启动日志调用。
func (h *Hub) PublicErr() error { return h.publicErr }

// initPublic 构建无凭证的公开连接（只用于公共行情接口）。
func (h *Hub) initPublic() error {
	var errs []string // 收集所有失败原因的切片
	// 空凭证创建：ccxt 的公共接口（行情/费率/盘口）不需要 Key。
	// 两条连接各自独立，一条失败不牵连另一条——错误汇总成一个返回。
	if ex, err := New(h.spotID, "spot", "", ""); err == nil { // “if 带初始化”：建连接
		h.publicSpot = ex // 成功就存进字段
	} else { // 失败
		errs = append(errs, fmt.Sprintf("%s 现货公开连接: %v", h.spotID, err)) // 记下原因
	}
	if ex, err := New(h.swapID, "swap", "", ""); err == nil {
		h.publicSwap = ex
	} else {
		errs = append(errs, fmt.Sprintf("%s 合约公开连接: %v", h.swapID, err))
	}
	if len(errs) > 0 { // 有失败
		// strings.Join：把切片元素用分隔符拼成一个字符串（这里用中文分号）。
		return errors.New(strings.Join(errs, "；"))
	}
	return nil // 全部成功
}

// PublicSpot 现货腿公开连接（行情模块用，无需配置 API）。
func (h *Hub) PublicSpot() (*Exchange, error) {
	// RLock() 是【读锁】：多个读者可以同时持锁（取连接是很频繁的读操作，互不干扰）。
	// RUnlock() 释放读锁。为什么用 defer？—— defer 注册的动作“函数返回前一定执行”，
	// 不管函数是从哪一行 return（成功返回 / 出错提前返回）都会解锁。如果忘了解锁，
	// 其它 goroutine 会永远卡在加锁处等不到锁——死锁。这是 Go 里管理锁的标配写法：
	// 加锁后立刻 defer 解锁，成对出现、不会漏。
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.publicSpot == nil { // 公开连接没建起来（网络问题等）
		return nil, errors.New("现货公开行情连接不可用（请检查网络）")
	}
	return h.publicSpot, nil // 返回连接对象
}

// PublicSwap 合约腿公开连接（行情模块用，无需配置 API）。
func (h *Hub) PublicSwap() (*Exchange, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.publicSwap == nil {
		return nil, errors.New("合约公开行情连接不可用（请检查网络）")
	}
	return h.publicSwap, nil
}

// Reload 从数据库读 API 凭证，重建两条连接（凭证变更后调用——热更新，不用重启）。
func (h *Hub) Reload() error {
	spotKey, spotSecret, err := h.loadCredential(h.spotID) // 读现货腿凭证
	if err != nil {
		return fmt.Errorf("现货腿交易所(%s)凭证缺失: %w", h.spotID, err)
	}
	swapKey, swapSecret, err := h.loadCredential(h.swapID) // 读合约腿凭证
	if err != nil {
		return fmt.Errorf("合约腿交易所(%s)凭证缺失: %w", h.swapID, err)
	}

	spotEx, err := New(h.spotID, "spot", spotKey, spotSecret) // 用新凭证建现货连接
	if err != nil {
		return err
	}
	swapEx, err := New(h.swapID, "swap", swapKey, swapSecret) // 用新凭证建合约连接
	if err != nil {
		return err
	}

	// 先建好【两】条新连接，再一次锁定整体替换：
	// 如果先换现货再建合约，合约建失败时系统就是“一条新一条旧”的错乱状态。
	// “全部准备好再原子切换”——替换共享资源的通用安全写法。
	h.mu.Lock() // Lock 加【写锁】：独占，此时其它读/写必须等
	h.spot = spotEx
	h.swap = swapEx
	h.mu.Unlock() // 释放写锁
	return nil
}

// loadCredential 从数据库读某交易所最新一条凭证。
// ORDER BY id DESC LIMIT 1：按 id 倒序取第 1 条 = 最新一条（Save 时“先删后插”保证只有一条）。
func (h *Hub) loadCredential(exchangeID string) (key, secret string, err error) {
	// QueryRow：查单行。Scan：把这一行的列填进变量（必须传指针 &key 才能写入）。
	row := h.db.QueryRow(
		`SELECT api_key, api_secret FROM exchange_api WHERE exchange = ? ORDER BY id DESC LIMIT 1`,
		exchangeID, // “?” 是 SQL 占位符，这里填 exchangeID——防止 SQL 注入（永远不要字符串拼接 SQL）
	)
	if err = row.Scan(&key, &secret); err != nil {
		return "", "", err // 查不到（没存过凭证）也返回错误
	}
	// 安全：库里的 secret 是加密存储的，建连前必须先解密。
	// DecryptOrRaw：解不开就当明文用（兼容旧版本未加密的数据）。
	if h.keyring != nil {
		secret = h.keyring.DecryptOrRaw(secret)
	}
	return key, secret, nil
}

// Spot 获取现货腿交易所连接（没配置凭证时返回错误，提示用户先填 API）。
func (h *Hub) Spot() (*Exchange, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.spot == nil {
		return nil, errors.New("现货腿交易所未配置，请先在“API配置”页保存凭证")
	}
	return h.spot, nil
}

// Swap 获取合约腿交易所连接。
func (h *Hub) Swap() (*Exchange, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.swap == nil {
		return nil, errors.New("合约腿交易所未配置，请先在“API配置”页保存凭证")
	}
	return h.swap, nil
}

// Ready 两条连接是否都已就绪（比如自动交易开跑前可以先查一下）。
func (h *Hub) Ready() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.spot != nil && h.swap != nil // && 并且：两条都非空才返回 true
}
