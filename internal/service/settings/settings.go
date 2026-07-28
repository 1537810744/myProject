// 【阅读顺序 06】设置模块（所有模块的“参数中心”）。
//
// 本文件职责：声明全部可调参数（key/默认值/中文说明），读写 settings 表。
// 阅读目的：项目有哪些旋钮可以调，全在 AllParams 列表里——
// 加新参数只需在列表追加一行，前端设置页自动渲染出来（需求：参数不隐藏）。
// 上下游：被几乎所有模块依赖（它们每次用参数时实时 Get，改完即时生效）。
package settings

import (
	"strconv"

	"deltacrypto/internal/database"
)

// ParamMeta 参数元信息（key、默认值、中文说明），前端设置页据此渲染
type ParamMeta struct {
	Key         string `json:"key"`
	Default     string `json:"default"`
	Description string `json:"description"`
}

// AllParams 全部可调参数（新增参数只需在此追加一行，前端自动可见）
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
	// —— 交易引擎（模块 3 的下单引擎参数，参考成熟方案）——
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
	// —— 邮件通知（模块 5）——
	{KeySMTPHost, "", "SMTP 服务器地址，如 smtp.qq.com"},
	{KeySMTPPort, "465", "SMTP 端口（SSL 一般 465）"},
	{KeySMTPUser, "", "SMTP 登录用户名（通常即发件邮箱）"},
	{KeySMTPPass, "", "SMTP 授权码/密码"},
	{KeyMailFrom, "", "发件邮箱"},
	{KeyMailTo, "", "收件邮箱（预警接收方）"},
}

// 参数 key 常量（避免散落在代码里的魔法字符串）
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
)

// Service 设置模块服务
type Service struct {
	db *database.DB
}

// New 创建设置模块，并把默认参数写入数据库（不覆盖用户已设置的值）
func New(db *database.DB) *Service {
	s := &Service{db: db}
	s.ensureDefaults()
	return s
}

// ensureDefaults 启动时把缺失的参数写入默认值（INSERT OR IGNORE，安全幂等）
func (s *Service) ensureDefaults() {
	for _, p := range AllParams {
		s.db.Exec(`INSERT OR IGNORE INTO settings(key, value) VALUES(?, ?)`, p.Key, p.Default)
	}
}

// Get 读取字符串参数（不存在时返回该参数的默认值）
func (s *Service) Get(key string) string {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err != nil {
		return s.defaultOf(key)
	}
	return v
}

// GetFloat 读取浮点参数
func (s *Service) GetFloat(key string) float64 {
	f, _ := strconv.ParseFloat(s.Get(key), 64)
	return f
}

// GetInt 读取整型参数
func (s *Service) GetInt(key string) int {
	n, _ := strconv.Atoi(s.Get(key))
	return n
}

// Set 更新参数
func (s *Service) Set(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// SetBatch 批量更新（前端设置页整体保存）
func (s *Service) SetBatch(kv map[string]string) error {
	for k, v := range kv {
		if err := s.Set(k, v); err != nil {
			return err
		}
	}
	return nil
}

// All 返回全部参数的当前值 + 元信息（前端渲染用，参数全部可见）
func (s *Service) All() []map[string]string {
	out := make([]map[string]string, 0, len(AllParams))
	for _, p := range AllParams {
		out = append(out, map[string]string{
			"key":         p.Key,
			"value":       s.Get(p.Key),
			"default":     p.Default,
			"description": p.Description,
		})
	}
	return out
}

// defaultOf 返回某参数的默认值
func (s *Service) defaultOf(key string) string {
	for _, p := range AllParams {
		if p.Key == key {
			return p.Default
		}
	}
	return ""
}

// MailConfig 邮件配置汇总（预警模块使用）
type MailConfig struct {
	Host string
	Port int
	User string
	Pass string
	From string
	To   string
}

// Enabled 邮件配置是否完整可用
func (m MailConfig) Enabled() bool {
	return m.Host != "" && m.User != "" && m.Pass != "" && m.To != ""
}

// GetMailConfig 读取邮件配置
func (s *Service) GetMailConfig() MailConfig {
	return MailConfig{
		Host: s.Get(KeySMTPHost),
		Port: s.GetInt(KeySMTPPort),
		User: s.Get(KeySMTPUser),
		Pass: s.Get(KeySMTPPass),
		From: s.Get(KeyMailFrom),
		To:   s.Get(KeyMailTo),
	}
}
