// 【阅读顺序 13】模块 5：预警模块。
// 每轮检查四类风险并通知用户：费率反转、ADL 排名≥4、距强平价<10%（都是 critical）、
// 资金平衡偏离（warning，只提醒不自动调仓）。
// 设计核心：所有预警走同一条 fire() 管线（去重→写库→发邮件→记日志）。
// 为什么统一？—— 四类预警的【发现】不同，但【触发后的处理】完全相同。把公共后处理
// 收敛成一个 fire() 是【模板方法】思想：以后加第五类预警只需写一个 checkXXX() 找到
// 问题、调 fire()，去重/邮件/入库自动接上。
// 语法点预览：const 常量、sync.Mutex、map[string]time.Time 去重表、多返回值 (T, bool)、
// strings.Builder、bool→int。
package alert

// import 导入用到的包。
import (
	"fmt"     // 格式化
	"strings" // 字符串
	"sync"    // 并发同步
	"time"    // 时间

	"deltacrypto/internal/database"         // 数据库
	"deltacrypto/internal/exchange"         // 交易所抽象层
	"deltacrypto/internal/model"            // 数据结构
	"deltacrypto/internal/notify"           // 邮件发送
	"deltacrypto/internal/service/account"  // 账户模块
	"deltacrypto/internal/service/settings" // 设置模块
	"deltacrypto/internal/service/trade"    // 交易模块
)

// dedupWindow 同类预警去重窗口：窗口期内同币对同类型只发一次。
// 为什么必须去重？—— 自动交易 7×24 小时跑，费率一直为负时每 15 秒发现一次，
// 不去重一天就是 5760 封邮件（邮件轰炸）。窗口期内“提醒到了”就够了。
// 4 * time.Hour：time.Hour 是 1 小时（time.Duration 纳秒整数），乘 4 = 4 小时。
const dedupWindow = 4 * time.Hour

// Service 预警模块服务。
type Service struct {
	db       *database.DB      // 数据库
	hub      *exchange.Hub     // 交易所连接管理器
	settings *settings.Service // 参数中心
	account  *account.Service  // 账户模块（爆仓/资金平衡检查用）
	trade    *trade.Service    // 交易模块（读持仓、写日志）

	mu       sync.Mutex           // 【互斥锁】：保护 lastSent，防止并发写
	lastSent map[string]time.Time // map 去重表：key = "类型|币对"，value = 上次发送时间
	// 去重表放内存而非数据库，为什么？—— 去重只看“本次进程运行期间”，重启后重新开始，
	// 没必要持久化。而预警【记录】本身落库（alert_log），让用户翻历史。两者职责不同：
	// 内存表管“现在发不发”，数据库管“曾经发生过什么”。
}

// New 创建预警模块。
func New(db *database.DB, hub *exchange.Hub, settings *settings.Service,
	accountSvc *account.Service, tradeSvc *trade.Service) *Service {
	return &Service{
		db: db, hub: hub, settings: settings, account: accountSvc, trade: tradeSvc,
		lastSent: make(map[string]time.Time), // 初始化空 map（make 必须！否则 nil map 不能写）
	}
}

// CheckAll 执行一轮全部预警检查（自动交易每轮调用）。
// 返回本轮触发的预警——autotrade 的 fast sell 需要“费率转负”这个信号。
func (s *Service) CheckAll() []model.AlertRecord {
	var fired []model.AlertRecord // 收集触发的预警（nil 切片可 append）

	// 1. 持仓相关预警：费率反转 / ADL。
	positions, err := s.trade.OpenPositions() // 读当前持仓
	if err == nil {                           // 读成功才继续（读失败跳过本轮检查）
		for _, p := range positions { // 遍历每个持仓
			if r, ok := s.checkFundingNegative(p); ok { // 检查费率反转
				fired = append(fired, r) // 触发了就收集
			}
			if r, ok := s.checkADL(p); ok { // 检查 ADL
				fired = append(fired, r)
			}
		}
	}
	// 爆仓预警：扫合约实时持仓（不依赖数据库持仓表）。
	if r, ok := s.checkLiquidation(); ok {
		fired = append(fired, r)
	}

	// 2. 资金平衡预警。
	if r, ok := s.checkBalance(); ok {
		fired = append(fired, r)
	}
	return fired
}

// checkFundingNegative 费率反转预警：当前费率 < 0（持有中就要亏钱了）。
// 为什么转负要预警？—— 策略收益主要来自“空头收正费率”，费率转负就变成空头【付】
// 资金费，越持有越亏，必须提醒尽快平仓。
func (s *Service) checkFundingNegative(p model.HedgePosition) (model.AlertRecord, bool) {
	swapEx, err := s.hub.Swap() // 合约腿连接（要私有接口拉费率）
	if err != nil {
		return model.AlertRecord{}, false // 返回 (空记录, false)——false=没触发
	}
	rates, err := swapEx.FetchFundingRates([]string{p.Symbol})
	if err != nil {
		return model.AlertRecord{}, false
	}
	if rate := rates[p.Symbol]; rate < 0 { // 当前费率 < 0（if 带初始化）
		return s.fire("funding_negative", p.Symbol, "critical", // 触发预警
			// fmt.Sprintf 是“格式化字符串”：%s=字符串占位，%.4f=保留4位小数的浮点数，
			// %% = 输出一个【字面百分号】。⚠️ 想要显示“%”必须写 %%（两个），
			// 因为单个 % 会被当成格式符的开头。这句输出如“【费率反转】BTC/USDT 当前资金费率 -0.0231% 已转负...”。
			fmt.Sprintf("【费率反转】%s 当前资金费率 %.4f%% 已转负，建议尽快平仓（fast sell）", p.Symbol, rate))
	}
	return model.AlertRecord{}, false // 没触发
}

// checkADL 预警：币安 ADL 排名 >= 4（被自动减仓风险高）。
func (s *Service) checkADL(p model.HedgePosition) (model.AlertRecord, bool) {
	swapEx, err := s.hub.Swap()
	if err != nil {
		return model.AlertRecord{}, false
	}
	rank, err := swapEx.FetchADLRank(p.Symbol) // 拉 ADL 排名（币安特有）
	if err != nil {
		return model.AlertRecord{}, false // 查询失败跳过（如该所不支持）
	}
	if rank >= 4 { // 排名 ≥ 4（共 5 级）
		return s.fire("adl", p.Symbol, "critical",
			fmt.Sprintf("【ADL 风险】%s 合约 ADL 排名 %d/5，被自动减仓风险高，建议平仓", p.Symbol, rank))
	}
	return model.AlertRecord{}, false
}

// checkLiquidation 爆仓预警：标记价距强平价不足 10%。
func (s *Service) checkLiquidation() (model.AlertRecord, bool) {
	overview, err := s.account.Overview() // 复用账户总览（拿实时合约持仓）
	if err != nil {
		return model.AlertRecord{}, false
	}
	for _, pos := range overview.SwapPositions { // 遍历合约持仓
		if pos.LiquidationPrice <= 0 || pos.MarkPrice <= 0 { // 数据不完整
			continue // 跳过（可能没持仓）
		}
		// 空头：价格【上涨】到强平价爆仓，距离 =（强平价-标记价）/标记价。
		// 多头方向相反（价格下跌爆仓），所以单独分支。
		distance := (pos.LiquidationPrice - pos.MarkPrice) / pos.MarkPrice * 100
		if pos.Side == "long" { // 多头
			distance = (pos.MarkPrice - pos.LiquidationPrice) / pos.MarkPrice * 100
		}
		if distance < 10 { // 距离强平不足 10%
			return s.fire("liquidation", pos.Symbol, "critical",
				fmt.Sprintf("【爆仓风险】%s 距强平价仅剩 %.2f%%（标记 %.4f / 强平 %.4f），请立即处理",
					pos.Symbol, distance, pos.MarkPrice, pos.LiquidationPrice))
		}
	}
	return model.AlertRecord{}, false
}

// checkBalance 资金平衡预警：现货:合约偏离 1:N 超过阈值。
// 只提醒不自动调仓——转账涉及真实资金和交易所限制，机器人不自动动钱。
func (s *Service) checkBalance() (model.AlertRecord, bool) {
	spotTotal, swapTotal, deviation, err := s.account.BalanceRatio() // 算偏差
	if err != nil {
		return model.AlertRecord{}, false
	}
	threshold := s.settings.GetFloat(settings.KeyBalanceWarnPct) // 阈值从设置读
	if threshold <= 0 {
		threshold = 15 // 兜底默认 15%
	}
	if deviation > threshold { // 偏差超阈值
		return s.fire("balance", "", "warning",
			fmt.Sprintf("【资金平衡】现货 %.2fU / 合约 %.2fU，偏离目标比例 %.1f%%（阈值 %.1f%%），请手动平衡资金",
				spotTotal, swapTotal, deviation, threshold))
	}
	return model.AlertRecord{}, false
}

// fire 触发一条预警：去重 → 发邮件 → 写库 → 记日志。所有预警的公共出口。
func (s *Service) fire(alertType, symbol, level, message string) (model.AlertRecord, bool) {
	rec := model.AlertRecord{ // 构造预警记录
		Time: time.Now(), Type: alertType, Symbol: symbol, Level: level, Message: message,
	}

	// 去重：窗口期内同类型同币对已发过则跳过。
	// 注意 key 是“类型|币对”——所以“BTC 费率转负”和“ETH 费率转负”是不同 key，
	// 各自独立去重，不同币需要分别提醒。
	key := alertType + "|" + symbol                                        // 字符串拼接成唯一键
	s.mu.Lock()                                                            // 加锁（CheckAll 可能被 HTTP + 自动交易同时调用，防并发写）
	if last, ok := s.lastSent[key]; ok && time.Since(last) < dedupWindow { // 已发过且没超窗口
		s.mu.Unlock()     // 解锁
		return rec, false // 去重命中：不重复发
	}
	s.lastSent[key] = rec.Time // 记录本次发送时间
	s.mu.Unlock()              // 解锁

	// 发通知（邮件 + webhook 双通道）。失败仅记录，不影响预警本身。
	// 为什么失败不影响？—— 通知只是手段，预警这个“事实”已发生；通知没发出，
	// 记录也已入库，用户翻预警页能看到。多通道里一个失败，另一个可能已送达。
	mailErr := notify.Notify(s.settings.GetMailConfig(), "套利工具预警 - "+alertType, message)
	rec.MailSent = mailErr == nil // 邮件是否成功

	// 写库。
	res, err := s.db.Exec(
		`INSERT INTO alert_log(type, symbol, level, message, mail_sent) VALUES(?,?,?,?,?)`,
		alertType, symbol, level, message, boolToInt(rec.MailSent))
	if err == nil {
		rec.ID, _ = res.LastInsertId() // 拿自增主键
	}

	// 同步写一条操作日志，前端“日志”页也能看到预警。
	logMsg := message
	if mailErr != nil { // 邮件失败
		logMsg += "（邮件发送失败: " + mailErr.Error() + "）" // 在日志里附加原因
	}
	s.trade.LogExternal("alert", level, alertType, symbol, logMsg)
	return rec, true // 返回 (记录, true)——true=触发了
}

// Records 查询最近的预警记录（前端预警页展示）。
func (s *Service) Records(limit int) ([]model.AlertRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT id, time, type, symbol, level, message, mail_sent FROM alert_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	// defer rows.Close()：rows 是数据库查询结果的【游标】，它会一直占着底层数据库连接；
	// 用完必须关闭，否则连接池的连接被占满，后面的查询全卡死。defer 保证函数无论从哪
	// 一行 return 都会执行 Close——这就是 Go 里“打开资源后立刻 defer 关闭”的标准姿势。
	defer rows.Close()
	// make([]model.AlertRecord, 0)：初始化一个【空切片】而不是 nil 切片。
	// 为什么必须在乎这点？—— JSON 序列化时：空切片输出 []，nil 切片输出 null。
	// 前端拿 res.data 去遍历，遇到 null 直接报错；输出 [] 则安全。所以所有返回列表的
	// 函数都统一用 make([]T, 0)。这是 Go 后端最常见的坑之一。
	out := make([]model.AlertRecord, 0)
	for rows.Next() { // rows.Next()：游标有没有下一行？有就返回 true 继续循环，没有返回 false 结束
		var r model.AlertRecord
		var mailSent int // 数据库里是整数
		// rows.Scan(...)：把【当前这一行】的各列按顺序填进变量。
		// 传的都是 &变量（取地址）——Scan 要往变量里写值，必须传指针。
		if err := rows.Scan(&r.ID, &r.Time, &r.Type, &r.Symbol, &r.Level, &r.Message, &mailSent); err != nil {
			return nil, err
		}
		r.MailSent = mailSent == 1 // 转回 bool
		out = append(out, r)       // 把这一行加入结果切片
	}
	// rows.Err()：遍历结束后再查一次“遍历过程中是否出错”。
	// 为什么还要查？—— 只检查循环里的 Scan 错误不够，遍历中途可能因连接断开等原因
	// 出错，这个错误存在 rows 里，必须最后取出来看。这是 SQL 游标模式的收尾动作。
	return out, rows.Err()
}

// Summary 给自动交易模块用的简短摘要（邮件正文片段）。
func Summary(fired []model.AlertRecord) string {
	if len(fired) == 0 { // 没触发
		return "本轮无预警"
	}
	var b strings.Builder // 字符串拼接器。为什么用它而不是“+=”？
	// 因为字符串是不可变的，每用一次“+=”就会新建一个字符串对象（旧的等垃圾回收）；
	// 而 Builder 内部用字节数组累积，最后一次性生成字符串，循环里拼接明显更快。
	// 这是 Go 里“循环拼字符串”的标准工具（mail.go 里拼邮件正文也是它）。
	for _, r := range fired {
		b.WriteString("- " + r.Message + "\n") // 每行一条预警
	}
	return b.String() // 一次性生成字符串
}

// boolToInt bool → 整数（SQLite 没有布尔类型）。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
