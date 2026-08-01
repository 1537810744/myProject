// 【阅读顺序 14】模块 6：自动交易模块（全系统的“大脑”）。
// 常驻循环，每 N 秒一轮：数据维护 → 预警检查 → 卖出阶段 → 买入阶段 → 邮件汇报。
// 为什么它是“大脑”而不是“苦力”？—— runOnce() 自己不做任何拉行情/下单/算账，
// 全是调用 market/trade/account/alert 的方法，只决定【什么时候做什么】。
// 这是【编排者模式】：改策略（比如换卖出阈值逻辑）只改这一个文件，不影响其它模块。
// 语法点预览：for{} 无限循环、time.Sleep、无条件 switch、map 当集合、continue/break、
// math.Floor、sync.Mutex、defer+recover（panic 恢复）、命名返回、%w 错误包装。
package autotrade

// import 导入用到的包。
import (
	"errors"  // 构造错误（panic 恢复里用）
	"fmt"     // 格式化
	"math"    // 数学
	"strings" // 字符串
	"sync"    // 并发同步
	"time"    // 时间

	"deltacrypto/internal/model"            // 数据结构
	"deltacrypto/internal/notify"           // 邮件 + webhook 通知
	"deltacrypto/internal/service/account"  // 账户模块
	"deltacrypto/internal/service/alert"    // 预警模块
	"deltacrypto/internal/service/market"   // 行情模块
	"deltacrypto/internal/service/settings" // 设置模块
	"deltacrypto/internal/service/trade"    // 交易模块
)

// Service 自动交易模块服务。
type Service struct {
	settings *settings.Service // 参数中心
	market   *market.Service   // 行情模块
	trade    *trade.Service    // 交易模块
	account  *account.Service  // 账户模块
	alert    *alert.Service    // 预警模块

	breaker *Breaker // 熔断器：连续失败自动停机（见 breaker.go）

	mu          sync.Mutex // 保护下面三个状态字段：后台循环在写，前端 HTTP 在读
	lastRunAt   time.Time  // time.Time 最近一轮执行时间
	lastSummary string     // string 最近一轮摘要（前端展示）
	roundCount  int        // int 累计执行轮数
}

// New 创建自动交易模块。
func New(settings *settings.Service, marketSvc *market.Service, tradeSvc *trade.Service,
	accountSvc *account.Service, alertSvc *alert.Service) *Service {
	return &Service{
		settings: settings, market: marketSvc, trade: tradeSvc,
		account: accountSvc, alert: alertSvc,
		breaker:     NewBreaker(5), // 初始阈值 5，每轮会从设置动态刷新
		lastSummary: "尚未执行",        // 初始摘要
	}
}

// Status 自动交易运行状态（前端展示）。
type Status struct {
	Enabled     bool      `json:"enabled"`      // bool 总开关（设置页配置）
	LastRunAt   time.Time `json:"last_run_at"`  // time.Time 最近一轮时间
	RoundCount  int       `json:"round_count"`  // int 累计轮数
	LastSummary string    `json:"last_summary"` // string 最近一轮摘要
	Halted      bool      `json:"halted"`       // bool 是否停机（熔断/手动）
}

// GetStatus 读取运行状态。加锁因为后台 goroutine 可能在写。
func (s *Service) GetStatus() Status {
	s.mu.Lock()         // 加锁（读也加锁：防止读到一半被写、数据错乱）
	defer s.mu.Unlock() // 解锁（defer 保证）
	return Status{      // 组装返回
		Enabled:     s.settings.GetInt(settings.KeyAutoTradeEnabled) == 1, // 总开关
		LastRunAt:   s.lastRunAt,
		RoundCount:  s.roundCount,
		LastSummary: s.lastSummary,
		Halted:      s.settings.IsHalted(),
	}
}

// Halt 手动停机（"杀开关"）：停止自动交易，发通知提醒。
// 用途：发现异常时立即停手，不再开新仓/自动平仓，先人工处理。
func (s *Service) Halt(reason string) error {
	if err := s.settings.SetHalted(true); err != nil { // 停机状态写进设置（持久化，重启也不丢）
		return err
	}
	s.trade.LogExternal("autotrade", "warn", "halt", "", "手动停机: "+reason)
	_ = notify.Notify(s.settings.GetMailConfig(), "自动交易已手动停机", reason)
	return nil
}

// Resume 恢复交易：清除熔断状态，解除停机。
func (s *Service) Resume() error {
	s.breaker.Reset() // 熔断器复位
	if err := s.settings.SetHalted(false); err != nil {
		return err
	}
	s.trade.LogExternal("autotrade", "info", "resume", "", "自动交易已恢复")
	return nil
}

// RunLoop 自动交易主循环（main 中以 goroutine 常驻运行）。
func (s *Service) RunLoop() {
	s.trade.LogExternal("autotrade", "info", "loop", "", "自动交易循环已启动（等待开关开启）")
	for { // 【无限循环】：程序生命周期内一直转。Go 里 for 后面什么都不写 = 死循环
		interval := s.settings.GetInt(settings.KeyLoopIntervalSec) // 间隔每轮现读
		if interval <= 0 {
			interval = 15 // 兜底
		}
		if s.settings.GetInt(settings.KeyAutoTradeEnabled) != 1 { // 总开关没开
			time.Sleep(3 * time.Second) // 3 秒查一次开关
			// 为什么关着也用 3 秒轮询？—— 用户刚在设置页打开开关，希望尽快生效；
			// 3 秒意味着“最多等 3 秒就开跑”，而开关关闭时不干活，这点开销可忽略。
			continue // 回到 for 循环开头（再查一遍）
		}
		s.safeRunOnce() // 执行一轮（safe 版：内部有 panic 恢复）
		// 间隔每轮现读：用户改了间隔，下一轮就按新间隔来，不用重启。
		// time.Duration(interval)：把 int 类型的 interval（秒数）转成 time.Duration。
		// time.Duration 本质就是一个 int64 纳秒数；time.Second = 10亿纳秒的常量。
		// 两者相乘 = “interval 秒的纳秒数”，time.Sleep 拿它当休眠时长。
		time.Sleep(time.Duration(interval) * time.Second) // 睡到下一轮
	}
}

// RunOnceManual 供前端“立即执行一轮”按钮手动触发。
func (s *Service) RunOnceManual() {
	s.safeRunOnce()
}

// safeRunOnce 给 runOnce 套一层 panic 恢复：
// 自动交易跑在后台 goroutine，任何 panic 都不该把整个进程带崩（goroutine panic
// 默认会让程序崩溃）。这里 recover 住，记日志并当成一次失败计入熔断。
func (s *Service) safeRunOnce() {
	defer func() {
		if r := recover(); r != nil { // recover 只能在 defer 函数里生效
			s.trade.LogExternal("autotrade", "error", "panic", "", fmt.Sprintf("panic 已恢复: %v", r))
			s.recordRoundResult(errors.New("panic: " + fmt.Sprint(r)))
		}
	}()
	s.runOnce()
}

// runOnce 执行一轮（卖出阶段 + 买入阶段），并记录成功/失败给熔断器。
func (s *Service) runOnce() {
	if s.settings.IsHalted() { // 停机状态（熔断触发 或 手动停机）
		s.trade.LogExternal("autotrade", "info", "halt", "", "自动交易处于停机状态，本轮跳过")
		return
	}
	err := s.runRound()      // 真正的单轮逻辑
	s.recordRoundResult(err) // 记录本轮成功/失败，交给熔断器判断
}

// recordRoundResult 把一轮的结果喂给熔断器；触发跳闸时停机 + 告警。
func (s *Service) recordRoundResult(err error) {
	s.breaker.SetMaxFail(s.settings.GetInt(settings.KeyBreakerMaxFail)) // 阈值从设置动态读
	if err == nil {
		s.breaker.RecordSuccess() // 成功：清零连续失败
		return
	}
	if s.breaker.RecordFailure() { // 连续失败达到阈值，刚刚跳闸
		_ = s.settings.SetHalted(true) // 停机（持久化）
		s.trade.LogExternal("autotrade", "error", "halt", "",
			fmt.Sprintf("连续失败超过阈值，自动停机（熔断）。最近错误: %v", err))
		_ = notify.Notify(s.settings.GetMailConfig(), "自动交易已熔断停机",
			fmt.Sprintf("连续失败达到阈值，自动交易已自动停机。\n最近错误: %v", err))
	}
}

// runRound 执行一轮完整的 卖出阶段 + 买入阶段。
// 返回 error：致命错误（读持仓/查购买力/拉行情失败）返回错误触发熔断；
// 正常决策（跳过买卖）返回 nil。
func (s *Service) runRound() error {
	roundStart := time.Now() // 本轮开始时间（记录状态、邮件正文用）
	var actions []string     // 本轮发生的动作（邮件正文 + 摘要）

	// ========== 第 0 步：数据维护（资金费流水入库 + 收益快照）==========
	// 为什么每轮都干？—— 详情页的“资金费流水”“收益曲线”靠每轮同步/打点刷新，
	// 跟着自动交易循环走，不需要单独的定时任务。
	s.account.SyncFundingPayments() // 同步资金费流水
	s.account.SnapshotProfit()      // 记收益快照

	// ========== 第 1 步：预警检查（拿到 fast sell 信号）==========
	firedAlerts := s.alert.CheckAll()    // 检查一轮预警
	fastSellSet := make(map[string]bool) // 需要 fast sell 的币对集合
	for _, r := range firedAlerts {      // 遍历触发的预警
		// 费率转负 或 ADL 预警，且带了币对 → 需要 fast sell
		if (r.Type == "funding_negative" || r.Type == "adl") && r.Symbol != "" {
			fastSellSet[r.Symbol] = true // 记录该币要 fast sell
		}
	}
	// 为什么先收集成集合再处理？—— 预警循环负责“收信号”，卖出循环负责“做决策”，
	// 用 map 做中间传递，卖出循环只需 O(1) 判断，关注点也分开了。

	// ========== 阶段 1：卖出阶段 ==========
	positions, err := s.trade.OpenPositions() // 读当前持仓
	if err != nil {
		s.trade.LogExternal("autotrade", "error", "sell_phase", "", "读取持仓失败: "+err.Error())
		return fmt.Errorf("读取持仓失败: %w", err) // 致命：返回错误触发熔断
	}
	soldAny := false                                                       // 是否有卖出动作
	holdThreshold := s.settings.GetFloat(settings.KeyHoldSellThresholdPct) // 持有阈值 0.01%

	for _, p := range positions { // 遍历每个持仓
		// fast sell：费率转负 / ADL 预警 → 一股脑卖出，不看基差。
		// 为什么不看基差？—— 风险优先：费率转负/ADL 是“马上亏钱/被强减”，
		// 这时候管不了基差是否有利，先撤了再说。风控 > 收益。
		if fastSellSet[p.Symbol] { // 这个币在 fast sell 集合里
			msg := s.closePosition(p, "fast_sell", "费率转负/ADL 风险，立即全部平仓")
			actions = append(actions, msg) // 记录动作
			soldAny = true                 // 标记有卖出
			continue                       // 处理下一个持仓
		}

		// 拿当前费率，决定 skip / slow sell。
		rate, err := s.market.CurrentFundingRate(p.Symbol) // 当前费率
		if err != nil {
			s.trade.LogExternal("autotrade", "warn", "sell_phase", p.Symbol, "费率获取失败，本轮跳过: "+err.Error())
			continue // 拿不到费率就不瞎卖，留到下一轮再判断（不致命）
		}

		// —— 卖出三态判断。注意这里 switch 后面【没有判断对象】——
		// switch { case 条件: } 是【无条件 switch】：纯粹按条件分发，比 if-else if 链更清晰。
		switch {
		case rate > holdThreshold: // 费率高于持有阈值
			// skip sell：持有有利可图，不卖。费率高于 0.01% = 持有一天还在收钱，卖了反而断收益。
			s.trade.LogExternal("autotrade", "info", "skip_sell", p.Symbol,
				// fmt.Sprintf 格式符：%.4f=保留4位小数的浮点数，%%=输出字面百分号（% 是格式符开头，必须双写）。
				fmt.Sprintf("当前费率 %.4f%% > 阈值 %.4f%%，持有有利可图，不卖", rate, holdThreshold))
		case rate < 0: // 费率转负
			// 费率转负但预警没覆盖（双保险）：正常情况费率转负会触发预警从而 fast sell，
			// 这里兜底——万一预警因去重被跳过，卖出循环自己也要接住。
			msg := s.closePosition(p, "fast_sell", fmt.Sprintf("费率 %.4f%% 转负，立即全部平仓", rate))
			actions = append(actions, msg)
			soldAny = true
		default: // 0 ≤ 费率 ≤ 阈值 → slow sell：看基差
			// 费率不高不低（还有收益但不丰厚），该考虑“基差是否在收敛”——
			// 收敛就平仓，把基差差价也落袋。
			currentBasis, err := s.market.CurrentBasisPct(p.Symbol) // 当前基差
			if err != nil {
				s.trade.LogExternal("autotrade", "warn", "slow_sell", p.Symbol, "基差获取失败，本轮跳过: "+err.Error())
				continue
			}
			if currentBasis < p.EntryBasisPct { // 当前基差 < 入场基差 = 价差在收敛
				// 建仓时赚的是高价差，现在价差变小，平掉等于把缩小的部分变成利润（顺带基差套利）。
				msg := s.closePosition(p, "slow_sell",
					fmt.Sprintf("费率 %.4f%% 偏低且基差 %.4f%% < 买入基差 %.4f%%，平仓顺带基差套利",
						rate, currentBasis, p.EntryBasisPct))
				actions = append(actions, msg)
				soldAny = true
			} else { // 基差没收敛
				s.trade.LogExternal("autotrade", "info", "slow_sell_skip", p.Symbol,
					fmt.Sprintf("费率 %.4f%% 偏低，但基差 %.4f%% >= 买入基差 %.4f%%，继续观望",
						rate, currentBasis, p.EntryBasisPct))
			}
		}
	}

	if soldAny { // 有卖出动作
		s.trade.LogExternal("autotrade", "info", "sell_phase", "", "卖出阶段完成，重新拉取账户与行情")
	}

	// ========== 阶段 2：买入阶段 ==========
	power, err := s.account.PurchasingPower() // 购买力 = min(合约×杠杆, 现货)
	if err != nil {
		s.trade.LogExternal("autotrade", "error", "buy_phase", "", "购买力查询失败: "+err.Error())
		s.finishRound(roundStart, actions) // 收尾（即使失败也要更新状态）
		return fmt.Errorf("购买力查询失败: %w", err)
	}
	groupSize := s.settings.GetFloat(settings.KeyGroupSizeUSDT) // 50U
	maxPairs := s.settings.GetInt(settings.KeyMaxBuyPairs)      // 3

	// skip buy：现金不足一组。
	// 为什么“不够一组就留着现金”？—— 一组 50U 是策略最小操作单位。花 30U 强买，
	// 拆单后数量太小可能达不到交易所最小下单量，手续费占比也上升。宁缺毋滥。
	if power < groupSize {
		s.trade.LogExternal("autotrade", "info", "skip_buy", "",
			fmt.Sprintf("购买力 %.2fU 不足一组 %.2fU，留着现金", power, groupSize))
		s.finishRound(roundStart, actions)
		return nil // 正常决策，不算失败
	}

	// 行情模块拿到待买入列表（已按费率降序）。
	candidates, err := s.market.Candidates()
	if err != nil {
		s.trade.LogExternal("autotrade", "error", "buy_phase", "", "行情获取失败: "+err.Error())
		s.finishRound(roundStart, actions)
		return fmt.Errorf("行情获取失败: %w", err)
	}
	// 排除已持仓的币对（不重复建仓）。为什么？—— 一个币只持有一组，保持
	// “一组=一个持仓”的对应关系，也避免自己在同一币上叠加仓位。
	heldSet := make(map[string]bool) // 已持仓集合
	for _, p := range positions {
		heldSet[p.Symbol] = true // 标记
	}
	var buyable []model.MarketCandidate // 可买的币
	for _, c := range candidates {      // 遍历行情列表
		if !heldSet[c.Symbol] { // 没持仓
			buyable = append(buyable, c) // 加入可买列表
		}
	}
	// skip buy：没有值得买的。
	if len(buyable) == 0 {
		s.trade.LogExternal("autotrade", "info", "skip_buy", "", "行情列表为空，没有值得买的，留着现金")
		s.finishRound(roundStart, actions)
		return nil
	}

	// —— 总持仓名义金额上限（风险控制）——
	// 设置 max_total_exposure_usdt>0 时：已持有的总名义金额达到上限就不再开新仓，
	// 防止仓位无限膨胀、保证金爆掉。0 = 不限（默认）。
	maxExposure := s.settings.GetFloat(settings.KeyMaxTotalExposureUSDT)
	if maxExposure > 0 {
		var currentNotional float64
		for _, p := range positions {
			currentNotional += p.SpotAmount * p.SpotEntryPrice // 每个持仓的名义金额累加
		}
		if currentNotional >= maxExposure {
			s.trade.LogExternal("autotrade", "info", "skip_buy", "",
				fmt.Sprintf("总持仓名义金额 %.2fU 已达上限 %.2fU，停止开新仓", currentNotional, maxExposure))
			s.finishRound(roundStart, actions)
			return nil
		}
	}

	// scatter buy：分散买入前 n 个（n = min(最大对数, 候选数, 现金够买的组数)）。
	// 为什么“分散”？—— 鸡蛋不放一个篮子：单押一个币，它费率突然反转就整组亏。
	// int(math.Floor(...))：math.Floor 返回 float64（向下取整后的浮点数，比如 3.7→3.0），
	// int(...) 再转回整数——因为后面要用它当循环次数（int 类型）。这就是“先取整再转类型”。
	affordable := int(math.Floor(power / groupSize)) // 现金够买几组（向下取整）
	n := minInt(maxPairs, len(buyable))              // 先取 最大对数 和 候选数 的较小
	if affordable < n {                              // 现金不够买那么多
		n = affordable // 少买几个
	}
	if n <= 0 {
		n = 1 // 不足一组时尝试买 1 组（下面的余额判断会兜底）
	}
	remainingPower := power  // 剩余购买力（每买一组扣掉）
	for i := 0; i < n; i++ { // 经典 for 循环：买 n 组
		if remainingPower <= 0 { // 钱花完了
			s.trade.LogExternal("autotrade", "info", "scatter_buy", buyable[i].Symbol, "购买力已耗尽，跳过")
			break // 跳出循环
		}
		// 本组下单量：正常 50U；余额不足 50U 时全花掉（上取整语义）。
		buyUSDT := groupSize // 默认一组
		if remainingPower < groupSize {
			buyUSDT = remainingPower // 全花掉
		}
		c := buyable[i] // 取第 i 个候选
		// 调交易模块建仓。传 RequestID 启用幂等（同一轮同一币不会重复建仓）。
		result, err := s.trade.Open(model.TradeRequest{
			Symbol:    c.Symbol,
			Action:    "open",
			TotalUSDT: buyUSDT,
			RequestID: fmt.Sprintf("auto-%d-%s", time.Now().Unix(), c.Symbol), // 幂等键
		})
		if err != nil {
			s.trade.LogExternal("autotrade", "error", "scatter_buy", c.Symbol, "建仓失败: "+err.Error())
			continue // 单个币建仓失败不影响买下一个
		}
		actions = append(actions, fmt.Sprintf("【买入】%s %.2fU（基差 %.4f%%，费率 %.4f%%）: %s",
			c.Symbol, buyUSDT, c.BasisPct, c.FundingRate, result.Message))
		remainingPower -= buyUSDT // 扣购买力
	}

	s.finishRound(roundStart, actions)
	return nil // 本轮正常结束
}

// closePosition 卖出统一入口：按持仓实际数量全部平掉（一个持仓即一组）。
// 交易模块内部已做原子拆单与粉尘处理。
func (s *Service) closePosition(p model.HedgePosition, action, reason string) string {
	// 用现货腿“数量×成本”估算名义价值作为卖出总量（估值用，交易模块按数量精度执行）。
	totalUSDT := p.SpotAmount * p.SpotEntryPrice
	if totalUSDT <= 0 { // 估不出来
		totalUSDT = s.settings.GetFloat(settings.KeyGroupSizeUSDT) // 兜底：按一组 50U
	}
	result, err := s.trade.Close(model.TradeRequest{ // 调交易模块平仓
		Symbol:    p.Symbol,
		Action:    "close",
		TotalUSDT: totalUSDT,
	}, p.ID) // 传持仓 id，让交易模块把持仓置 closed
	if err != nil {
		msg := fmt.Sprintf("【%s失败】%s: %v", action, p.Symbol, err)
		s.trade.LogExternal("autotrade", "error", action, p.Symbol, msg)
		return msg
	}
	msg := fmt.Sprintf("【%s】%s: %s（%s）", action, p.Symbol, result.Message, reason)
	s.trade.LogExternal("autotrade", "info", action, p.Symbol, msg)
	return msg
}

// finishRound 收尾：记录状态；有动作时发邮件。
func (s *Service) finishRound(start time.Time, actions []string) {
	summary := "本轮无买卖操作"  // 默认摘要
	if len(actions) > 0 { // 有动作
		summary = strings.Join(actions, "\n") // 把所有动作拼成多行文本
		// 有动作才发通知——没动作时 15 秒一封 = 通知轰炸。只在真买卖了才打扰用户。
		// start.Format("2006-01-02 15:04:05")：把时间转成“年-月-日 时:分:秒”字符串。
		// ⚠️ "2006-01-02 15:04:05" 是 Go 的固定参考时间模板（2006年1月2日15点04分05秒），
		// 数字的位置决定输出年月日时分秒——这是 Go 特有的写法，不是随手填的数字。
		body := fmt.Sprintf("时间: %s\n\n%s", start.Format("2006-01-02 15:04:05"), summary)
		// notify.Notify：邮件 + webhook 双通道（webhook 是第二通道，邮件失败还有它）。
		if err := notify.Notify(s.settings.GetMailConfig(), "套利工具执行通知", body); err != nil {
			s.trade.LogExternal("autotrade", "warn", "mail", "", "执行通知发送失败: "+err.Error())
		}
	}
	// 加锁更新状态（后台循环写、前端 HTTP 读）。
	s.mu.Lock()
	s.lastRunAt = start     // 记录本轮时间
	s.roundCount++          // 轮数 +1
	s.lastSummary = summary // 记录摘要
	s.mu.Unlock()
}

// minInt 两个 int 取较小。
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
