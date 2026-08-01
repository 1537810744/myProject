// 【阅读顺序 06】设置模块：全部可调参数的“家”。
// 参数存在 SQLite 的 settings 表里，各模块每次用到时实时 Get()——所以改完设置立即生效、不用重启。
// 加新参数只需在 AllParams 加一行，前端设置页自动渲染出来。
// 语法点预览：const 常量块、var 包级变量、[]struct{} 切片字面量、方法接收者（值/指针）、
// map、strconv 字符串数字互转、UPSERT SQL。
package settings

// import 导入用到的包。
import (
	"strconv" // 字符串与数字互转：strconv.ParseFloat / strconv.Atoi

	"deltacrypto/internal/database" // 数据库
)

// ParamMeta 参数元信息（key、默认值、中文说明）。前端设置页就靠它自动生成输入框。
type ParamMeta struct {
	Key         string `json:"key"`         // string 参数名（存数据库用的键）
	Default     string `json:"default"`     // string 默认值（字符串，因为表里 value 列是 TEXT）
	Description string `json:"description"` // string 中文说明（前端展示）
}

// AllParams 全部可调参数清单。
// var 是【包级变量】：程序启动时初始化，包内所有函数共享。
// []ParamMeta{...} 是切片字面量：把一堆 ParamMeta 结构体列出来。
// 每个 {KeyFundingCount, "5", "..."} 是 ParamMeta 的【位置式复合字面量】——
// 按字段顺序填（Key、Default、Description），不写字段名（3 个字段顺序一目了然时才可读）。
var AllParams = []ParamMeta{
	// —— 行情过滤（模块 2）——
	{KeyFundingCount, "5", "最近 N 次资金费率用于趋势/均值判断"},
	{KeyMinBasisPct, "0.1", "基差约束：合约比现货贵的最小百分比（%）"},
	{KeyMinFundingAvgPct, "0.05", "最近 N 次资金费率均值下限（%）"},
	{KeyMinQuoteVolume24h, "50000", "24H 合约成交额下限（USDT，流通量约束）"},
	// —— 卖出策略（模块 6 阶段 1）——
	{KeyHoldSellThresholdPct, "0.01", "持有阈值：当前费率高于此值（%）不卖，持有有利可图"},
	// —— 交易执行（模块 3/6）——
	{KeyGroupSizeUSDT, "50", "组容量：每次买入/卖出的总名义价值（U）"},
	{KeyAtomSizeUSDT, "5", "原子单位：每笔原子交易的名义价值（U）"},
	{KeyDustUSDT, "5", "粉尘阈值：剩余低于此值（U）一并带走"},
	{KeyMaxBuyPairs, "3", "买入阶段最多分散的交易对数量"},
	// —— 交易引擎（模块 3 的下单引擎参数）——
	{KeyOrderMethod, "maker", "下单方式：maker=限价挂单追价 / taker=市价直接成交"},
	{KeyOrderbookLevel, "3", "盘口档位：Maker 挂单落在前 N 档，掉出自动追价到第 1 档"},
	{KeyMaxChaseCount, "50", "最大追价次数：超过后按下面开关处理"},
	{KeyChaseToTaker, "1", "追价超限转 Taker：1 开（市价保证成交）/ 0 关（报错停止）"},
	{KeyMaxNetExposure, "0", "最大净敞口（币数量）：0 不限，超出自动停止并告警"},
	{KeyMaxRetry, "3", "下单失败最大重试次数"},
	// —— 自动交易（模块 6）——
	{KeyLoopIntervalSec, "15", "自动交易轮询间隔（秒）"},
	{KeyAutoTradeEnabled, "0", "自动交易总开关：1 开 / 0 关"},
	// —— 资金与杠杆（模块 4/5）——
	{KeyLeverage, "4", "合约杠杆倍数"},
	{KeyBalanceRatio, "4", "资金分配比 合约:现货 = 1:N"},
	{KeyBalanceWarnPct, "15", "资金平衡预警阈值：双方误差超过此百分比（%）提醒"},
	// —— 邮件/Webhook 通知（模块 5，邮件为主、webhook 备用）——
	{KeySMTPHost, "", "SMTP 服务器地址，如 smtp.qq.com"},
	{KeySMTPPort, "465", "SMTP 端口（SSL 一般 465）"},
	{KeySMTPUser, "", "SMTP 登录用户名（通常即发件邮箱）"},
	{KeySMTPPass, "", "SMTP 授权码/密码"},
	{KeyMailFrom, "", "发件邮箱"},
	{KeyMailTo, "", "收件邮箱（预警接收方）"},
	{KeyWebhookURL, "", "Webhook 通知地址（可选，如 Server酱/企业微信机器人 URL），邮件之外的第二通道"},
	// —— 模拟盘 / 风险控制（本次升级新增）——
	{KeyDryRun, "0", "模拟盘开关：1=不下真单，模拟成交并记日志（安全测试用）"},
	{KeyBreakerMaxFail, "5", "熔断阈值：自动交易连续失败多少次后自动停机并告警"},
	{KeyMaxTotalExposureUSDT, "0", "总持仓名义金额上限（U）：0=不限，达到后停止开新仓"},
	{KeyBackupIntervalHours, "6", "数据库自动备份间隔（小时）：0=不自动备份"},
}

// 参数 key 常量。
// 为什么抽成常量而不直接在代码里写 "leverage"？—— 写错一个字编译器不会发现，
// 运行时才炸。抽成常量后：拼错常量名编译直接报错，且全局可搜索。
// 这就是【魔法字符串】反模式的解法。
// const 是常量块：值在编译期确定、不可修改（写“给常量赋值”会编译报错）。
const (
	KeyFundingCount         = "funding_count"
	KeyMinBasisPct          = "min_basis_pct"
	KeyMinFundingAvgPct     = "min_funding_avg_pct"
	KeyMinQuoteVolume24h    = "min_quote_volume_24h"
	KeyHoldSellThresholdPct = "hold_sell_threshold_pct"
	KeyGroupSizeUSDT        = "group_size_usdt"
	KeyAtomSizeUSDT         = "atom_size_usdt"
	KeyDustUSDT             = "dust_usdt"
	KeyMaxBuyPairs          = "max_buy_pairs"
	KeyOrderMethod          = "order_method"
	KeyOrderbookLevel       = "orderbook_level"
	KeyMaxChaseCount        = "max_chase_count"
	KeyChaseToTaker         = "chase_to_taker"
	KeyMaxNetExposure       = "max_net_exposure"
	KeyMaxRetry             = "max_retry"
	KeyLoopIntervalSec      = "loop_interval_sec"
	KeyAutoTradeEnabled     = "auto_trade_enabled"
	KeyLeverage             = "leverage"
	KeyBalanceRatio         = "balance_ratio"
	KeyBalanceWarnPct       = "balance_warn_pct"
	KeySMTPHost             = "smtp_host"
	KeySMTPPort             = "smtp_port"
	KeySMTPUser             = "smtp_user"
	KeySMTPPass             = "smtp_pass"
	KeyMailFrom             = "mail_from"
	KeyMailTo               = "mail_to"
	KeyWebhookURL           = "webhook_url"
	KeyDryRun               = "dry_run"
	KeyBreakerMaxFail       = "breaker_max_fail"
	KeyMaxTotalExposureUSDT = "max_total_exposure_usdt"
	KeyBackupIntervalHours  = "backup_interval_hours"
	// trading_halted 是【运行状态】不是用户参数：熔断器或手动接口写它，自动交易读它。
	// 为什么不放进 AllParams？—— 它不该出现在设置页让用户随便改，它是"系统自我保护开关"。
	keyHalted = "trading_halted"
)

// Service 设置模块服务。只持 db 就够了——参数全在数据库里。
type Service struct {
	db *database.DB // 数据库连接
}

// New 创建设置模块，并确保所有默认参数已写入数据库。
// 为什么 New 里就写库？—— 第一次启动时把默认参数都建好，用户之后改的是库里的值。
func New(db *database.DB) *Service {
	s := &Service{db: db} // 创建 Service 对象
	s.ensureDefaults()    // 调自己的方法：确保默认参数存在
	return s
}

// ensureDefaults 把“缺失”的参数写入默认值。
// 为什么必须 IGNORE？—— 程序每次启动都会跑这里，用户改过的值绝不能又被默认值覆盖。
// “只填缺失、不覆盖已有”= 用户设置优先。
func (s *Service) ensureDefaults() {
	for _, p := range AllParams { // 遍历参数清单
		// INSERT OR IGNORE：key 已存在就什么都不做（IGNORE），否则插入。
		// 循环里用“?” 占位符 + 参数，避免 SQL 注入（永远别字符串拼接 SQL）。
		s.db.Exec(`INSERT OR IGNORE INTO settings(key, value) VALUES(?, ?)`, p.Key, p.Default)
	}
}

// Get 读字符串参数；库里没有时返回代码里的默认值（双保险）。
func (s *Service) Get(key string) string {
	var v string                                                                   // var 声明 string 变量（零值 = 空串）
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v) // 查单行并 Scan 进 v
	if err != nil {                                                                // 查不到（或别的错误）
		return s.defaultOf(key) // 回退到代码里的默认值
	}
	return v // 返回库里的值
}

// GetFloat 读浮点参数。settings 表里存的都是字符串，取出来再转。
func (s *Service) GetFloat(key string) float64 {
	f, _ := strconv.ParseFloat(s.Get(key), 64) // 字符串 → float64
	// “_” 丢弃错误。为什么可以忽略？—— 默认值都是我们写的合法数字，只有用户手动
	// 改库成非法值才会解析失败。忽略换取调用方一行写完（个人工具取舍）。
	return f
}

// GetInt 读整型参数。
func (s *Service) GetInt(key string) int {
	// strconv.Atoi：字符串 → int，和上面的 GetFloat 同理。
	// 返回 (数字, 错误) 两个值；这里用“_”把错误丢了。
	// 为什么可以丢？—— 默认值都是我们自己写的合法数字；只有用户手动改库成
	// 非数字才会解析失败。个人工具这里选择忽略，换取调用方一行写完。
	n, _ := strconv.Atoi(s.Get(key))
	return n
}

// Set 更新单个参数。
// UPSERT 语法（ON CONFLICT ... DO UPDATE）：存在则更新、不存在则插入。
// 为什么不用“先 SELECT 判断有没有，再决定 INSERT/UPDATE”？—— 那是两步操作，
// 并发时会判断出同一结论导致冲突；UPSERT 是数据库层面的原子操作。
func (s *Service) Set(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// SetBatch 批量更新（前端设置页点“保存”时一次性提交所有参数）。
func (s *Service) SetBatch(kv map[string]string) error {
	for k, v := range kv { // 遍历 map：k=键，v=值（map 遍历顺序随机，但这里无所谓先后）
		if err := s.Set(k, v); err != nil { // 逐个调用 Set
			return err // 任一失败就返回（前端能看到错误）
		}
	}
	return nil
}

// All 返回全部参数的“当前值 + 元信息”（前端渲染设置页用）。
// 为什么返回 []map[string]string 而不是一个结构体切片？—— 前端每个参数要 4 个字段
// （key/value/default/description），而 ParamMeta 只有 3 个（没有 value）。临时拼 map
// 比定义新结构体轻量。取舍：字段固定且多处复用时才定义结构体，一次性拼接用 map。
func (s *Service) All() []map[string]string {
	out := make([]map[string]string, 0, len(AllParams)) // 空切片，预分配容量
	for _, p := range AllParams {
		out = append(out, map[string]string{ // 每个参数拼成一个 map 加进结果
			"key":         p.Key,         // 参数名
			"value":       s.Get(p.Key),  // 当前值（实时读库）
			"default":     p.Default,     // 默认值
			"description": p.Description, // 中文说明
		})
	}
	return out
}

// defaultOf 返回某参数的默认值（Get 兜底用）。
func (s *Service) defaultOf(key string) string {
	for _, p := range AllParams { // 遍历清单找 key
		if p.Key == key { // 找到
			return p.Default // 返回它的默认值
		}
	}
	return "" // 不是已知参数：返回空串
}

// MailConfig 邮件 + Webhook 通知配置汇总。把通知相关参数“打包”成一个结构体传给
// 通知模块，避免参数列表长得没法读（函数参数太多是坏味道）。
// 名字沿用 MailConfig（历史命名），实际现在承载"邮件 + webhook 双通道"。
type MailConfig struct {
	Host       string // string SMTP 服务器地址
	Port       int    // int 端口（465）
	User       string // string 登录用户名
	Pass       string // string 授权码/密码
	From       string // string 发件邮箱
	To         string // string 收件邮箱
	WebhookURL string // string Webhook 通知地址（可选，第二通道）
}

// Enabled 判断邮件配置是否完整可用。
// 注意这是【值接收者】(m MailConfig)：只读判断、不改字段，结构体又小，用值即可
// （对比 exchange.go 全是指针接收者——那里要访问/可能改状态）。
func (m MailConfig) Enabled() bool {
	// && 并且：4 个关键字段全部非空才返回 true。
	return m.Host != "" && m.User != "" && m.Pass != "" && m.To != ""
}

// GetMailConfig 从设置模块拼出一个 MailConfig（含 webhook）。
func (s *Service) GetMailConfig() MailConfig {
	return MailConfig{ // 复合字面量：从设置里逐个取字段填充
		Host:       s.Get(KeySMTPHost),
		Port:       s.GetInt(KeySMTPPort),
		User:       s.Get(KeySMTPUser),
		Pass:       s.Get(KeySMTPPass),
		From:       s.Get(KeyMailFrom),
		To:         s.Get(KeyMailTo),
		WebhookURL: s.Get(KeyWebhookURL),
	}
}

// IsHalted 自动交易是否处于停机状态（熔断器触发 或 手动停机）。
func (s *Service) IsHalted() bool {
	return s.Get(keyHalted) == "1"
}

// SetHalted 设置停机状态：true=停机（停止自动交易），false=恢复。
func (s *Service) SetHalted(halted bool) error {
	v := "0"
	if halted {
		v = "1"
	}
	return s.Set(keyHalted, v)
}

// DryRun 是否开启模拟盘（不下真单）。
func (s *Service) DryRun() bool {
	return s.GetInt(KeyDryRun) == 1
}
