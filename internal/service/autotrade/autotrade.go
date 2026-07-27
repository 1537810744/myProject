// Package autotrade 模块 6：自动交易模块。
//
// 需求要点（文档原始逻辑，全部实现）：
//   - 每隔 N 秒循环一次（间隔在设置页配置）；
//   - 每轮调用 预警模块 / 账户信息 / 行情模块，拿到最新数据；
//   - 阶段 1 卖出：
//     skip sell : 当前费率 > 0.0100%（阈值可配），持有有利可图，不卖；
//     slow sell : 0 < 费率 <= 阈值，且当前基差 < 买入时基差，则卖（顺带基差套利）；
//     若当前基差仍高于买入基差，跳过继续观望；
//     fast sell : 费率为负 / ADL 预警，一股脑对冲卖出，不再判断基差；
//     （卖出均为 优先腿现货 + 对冲腿合约，原子单位拆单，粉尘一并带走）
//   - 阶段 2 买入：
//     skip buy   : 现金不足，或行情列表为空——宁愿留着现金；
//     scatter buy: 平均分散买入行情列表前 n 个（n <= 3，可配），每个一组 50U；
//     余额不足 50U 则上取整（有多少买多少），没钱则跳过；
//   - 若发生了卖出，重新拉取账户与行情（否则中间只隔几微秒，行情足够新，不重拉）；
//   - 每轮操作写日志入库，有动作时发邮件通知用户；
//   - 麦穗理论：不要贪心，见好就收；牛吃草：不抢一秒，谨慎执行。
//
// 模块间调用：按需求文档，后端模块间不走 HTTP，直接导入包调用。
package autotrade

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"deltacrypto/internal/model"
	"deltacrypto/internal/notify"
	"deltacrypto/internal/service/account"
	"deltacrypto/internal/service/alert"
	"deltacrypto/internal/service/market"
	"deltacrypto/internal/service/settings"
	"deltacrypto/internal/service/trade"
)

// Service 自动交易模块服务
type Service struct {
	settings *settings.Service
	market   *market.Service
	trade    *trade.Service
	account  *account.Service
	alert    *alert.Service

	mu          sync.Mutex
	lastRunAt   time.Time // 最近一轮执行时间
	lastSummary string    // 最近一轮摘要（前端展示）
	roundCount  int       // 累计执行轮数
}

// New 创建自动交易模块
func New(settings *settings.Service, marketSvc *market.Service, tradeSvc *trade.Service,
	accountSvc *account.Service, alertSvc *alert.Service) *Service {
	return &Service{
		settings: settings, market: marketSvc, trade: tradeSvc,
		account: accountSvc, alert: alertSvc,
		lastSummary: "尚未执行",
	}
}

// Status 自动交易运行状态（前端展示）
type Status struct {
	Enabled     bool      `json:"enabled"`      // 总开关（设置页配置）
	LastRunAt   time.Time `json:"last_run_at"`  // 最近一轮时间
	RoundCount  int       `json:"round_count"`  // 累计轮数
	LastSummary string    `json:"last_summary"` // 最近一轮摘要
}

// GetStatus 读取运行状态
func (s *Service) GetStatus() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{
		Enabled:     s.settings.GetInt(settings.KeyAutoTradeEnabled) == 1,
		LastRunAt:   s.lastRunAt,
		RoundCount:  s.roundCount,
		LastSummary: s.lastSummary,
	}
}

// RunLoop 自动交易主循环（在 main 中以 goroutine 常驻运行）。
// 每轮先读设置：开关关闭则休眠等待；间隔动态读取，改设置即时生效。
func (s *Service) RunLoop() {
	s.trade.LogExternal("autotrade", "info", "loop", "", "自动交易循环已启动（等待开关开启）")
	for {
		interval := s.settings.GetInt(settings.KeyLoopIntervalSec)
		if interval <= 0 {
			interval = 15
		}
		if s.settings.GetInt(settings.KeyAutoTradeEnabled) != 1 {
			time.Sleep(3 * time.Second) // 开关关闭：轻量休眠，3 秒检查一次开关
			continue
		}
		s.runOnce()
		time.Sleep(time.Duration(interval) * time.Second)
	}
}

// RunOnceManual 供前端“立即执行一轮”按钮手动触发
func (s *Service) RunOnceManual() {
	s.runOnce()
}

// runOnce 执行一轮完整的 卖出阶段 + 买入阶段
func (s *Service) runOnce() {
	roundStart := time.Now()
	var actions []string // 本轮发生的动作（邮件正文）

	// ========== 第 0 步：预警检查（拿到 fast sell 信号） ==========
	firedAlerts := s.alert.CheckAll()
	fastSellSet := make(map[string]bool) // 需要 fast sell 的币对集合
	for _, r := range firedAlerts {
		if (r.Type == "funding_negative" || r.Type == "adl") && r.Symbol != "" {
			fastSellSet[r.Symbol] = true
		}
	}

	// ========== 阶段 1：卖出阶段 ==========
	positions, err := s.trade.OpenPositions()
	if err != nil {
		s.trade.LogExternal("autotrade", "error", "sell_phase", "", "读取持仓失败: "+err.Error())
		return
	}
	soldAny := false
	holdThreshold := s.settings.GetFloat(settings.KeyHoldSellThresholdPct) // 0.01%

	for _, p := range positions {
		// fast sell：费率为负 / ADL 预警 -> 一股脑卖出，不看基差
		if fastSellSet[p.Symbol] {
			msg := s.closePosition(p, "fast_sell", "费率转负/ADL 风险，立即全部平仓")
			actions = append(actions, msg)
			soldAny = true
			continue
		}

		// 拿当前费率，决定 skip / slow sell
		rate, err := s.market.CurrentFundingRate(p.Symbol)
		if err != nil {
			s.trade.LogExternal("autotrade", "warn", "sell_phase", p.Symbol, "费率获取失败，本轮跳过: "+err.Error())
			continue
		}

		switch {
		case rate > holdThreshold:
			// skip sell：持有有利可图，不卖
			s.trade.LogExternal("autotrade", "info", "skip_sell", p.Symbol,
				fmt.Sprintf("当前费率 %.4f%% > 阈值 %.4f%%，持有有利可图，不卖", rate, holdThreshold))
		case rate < 0:
			// 费率转负但预警未覆盖（极端情况兜底）-> fast sell
			msg := s.closePosition(p, "fast_sell", fmt.Sprintf("费率 %.4f%% 转负，立即全部平仓", rate))
			actions = append(actions, msg)
			soldAny = true
		default:
			// slow sell：0 <= 费率 <= 阈值，看基差
			currentBasis, err := s.market.CurrentBasisPct(p.Symbol)
			if err != nil {
				s.trade.LogExternal("autotrade", "warn", "slow_sell", p.Symbol, "基差获取失败，本轮跳过: "+err.Error())
				continue
			}
			if currentBasis < p.EntryBasisPct {
				msg := s.closePosition(p, "slow_sell",
					fmt.Sprintf("费率 %.4f%% 偏低且基差 %.4f%% < 买入基差 %.4f%%，平仓顺带基差套利",
						rate, currentBasis, p.EntryBasisPct))
				actions = append(actions, msg)
				soldAny = true
			} else {
				s.trade.LogExternal("autotrade", "info", "slow_sell_skip", p.Symbol,
					fmt.Sprintf("费率 %.4f%% 偏低，但基差 %.4f%% >= 买入基差 %.4f%%，继续观望",
						rate, currentBasis, p.EntryBasisPct))
			}
		}
	}

	// 有卖出动作 -> 账户已变化，后续买入阶段重新拉取（购买力查询本身就是实时的）
	if soldAny {
		s.trade.LogExternal("autotrade", "info", "sell_phase", "", "卖出阶段完成，重新拉取账户与行情")
	}

	// ========== 阶段 2：买入阶段 ==========
	power, err := s.account.PurchasingPower() // 购买力 = min(合约×杠杆, 现货)
	if err != nil {
		s.trade.LogExternal("autotrade", "error", "buy_phase", "", "购买力查询失败: "+err.Error())
		s.finishRound(roundStart, actions)
		return
	}
	groupSize := s.settings.GetFloat(settings.KeyGroupSizeUSDT) // 50U
	maxPairs := s.settings.GetInt(settings.KeyMaxBuyPairs)      // 3

	// skip buy：现金不足一组
	if power < groupSize {
		s.trade.LogExternal("autotrade", "info", "skip_buy", "",
			fmt.Sprintf("购买力 %.2fU 不足一组 %.2fU，留着现金", power, groupSize))
		s.finishRound(roundStart, actions)
		return
	}

	// 行情模块拿到待买入列表（已按费率降序）
	candidates, err := s.market.Candidates()
	if err != nil {
		s.trade.LogExternal("autotrade", "error", "buy_phase", "", "行情获取失败: "+err.Error())
		s.finishRound(roundStart, actions)
		return
	}
	// 排除已持仓的币对（不重复建仓）
	heldSet := make(map[string]bool)
	for _, p := range positions {
		heldSet[p.Symbol] = true
	}
	var buyable []model.MarketCandidate
	for _, c := range candidates {
		if !heldSet[c.Symbol] {
			buyable = append(buyable, c)
		}
	}
	// skip buy：没有值得买的
	if len(buyable) == 0 {
		s.trade.LogExternal("autotrade", "info", "skip_buy", "", "行情列表为空，没有值得买的，留着现金")
		s.finishRound(roundStart, actions)
		return
	}

	// scatter buy：平均分散到前 n 个（n = min(最大对数, 候选数, 现金够买的组数)）
	affordable := int(math.Floor(power / groupSize)) // 现金够买几组
	n := minInt(maxPairs, len(buyable))
	if affordable < n {
		n = affordable
	}
	if n <= 0 {
		n = 1 // 不足一组时尝试上取整买 1 组（下面的余额判断会兜底）
	}
	remainingPower := power
	for i := 0; i < n; i++ {
		if remainingPower <= 0 {
			s.trade.LogExternal("autotrade", "info", "scatter_buy", buyable[i].Symbol, "购买力已耗尽，跳过")
			break
		}
		// 本组下单量：正常 50U；余额不足 50U 时上取整（有多少买多少）
		buyUSDT := groupSize
		if remainingPower < groupSize {
			buyUSDT = remainingPower
		}
		c := buyable[i]
		result, err := s.trade.Open(model.TradeRequest{
			Symbol:    c.Symbol,
			Action:    "open",
			TotalUSDT: buyUSDT,
		})
		if err != nil {
			s.trade.LogExternal("autotrade", "error", "scatter_buy", c.Symbol, "建仓失败: "+err.Error())
			continue
		}
		actions = append(actions, fmt.Sprintf("【买入】%s %.2fU（基差 %.4f%%，费率 %.4f%%）: %s",
			c.Symbol, buyUSDT, c.BasisPct, c.FundingRate, result.Message))
		remainingPower -= buyUSDT
	}

	s.finishRound(roundStart, actions)
}

// closePosition 卖出统一入口：按持仓实际数量全部平掉（一个持仓即一组 50U）。
// 交易模块内部已做原子拆单与粉尘处理。
func (s *Service) closePosition(p model.HedgePosition, action, reason string) string {
	// 用现货腿数量与当前价估算名义价值作为卖出总量
	totalUSDT := p.SpotAmount * p.SpotEntryPrice // 估值用，交易模块按数量精度执行
	if totalUSDT <= 0 {
		totalUSDT = s.settings.GetFloat(settings.KeyGroupSizeUSDT)
	}
	result, err := s.trade.Close(model.TradeRequest{
		Symbol:    p.Symbol,
		Action:    "close",
		TotalUSDT: totalUSDT,
	}, p.ID)
	if err != nil {
		msg := fmt.Sprintf("【%s失败】%s: %v", action, p.Symbol, err)
		s.trade.LogExternal("autotrade", "error", action, p.Symbol, msg)
		return msg
	}
	msg := fmt.Sprintf("【%s】%s: %s（%s）", action, p.Symbol, result.Message, reason)
	s.trade.LogExternal("autotrade", "info", action, p.Symbol, msg)
	return msg
}

// finishRound 收尾：记录状态、有动作时发邮件
func (s *Service) finishRound(start time.Time, actions []string) {
	summary := "本轮无买卖操作"
	if len(actions) > 0 {
		summary = strings.Join(actions, "\n")
		// 有动作 -> 发邮件通知用户自己执行了哪些操作
		body := fmt.Sprintf("时间: %s\n\n%s", start.Format("2006-01-02 15:04:05"), summary)
		if err := notify.SendMail(s.settings.GetMailConfig(), "套利工具执行通知", body); err != nil {
			s.trade.LogExternal("autotrade", "warn", "mail", "", "执行通知邮件发送失败: "+err.Error())
		}
	}
	s.mu.Lock()
	s.lastRunAt = start
	s.roundCount++
	s.lastSummary = summary
	s.mu.Unlock()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
