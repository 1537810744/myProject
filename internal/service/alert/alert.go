// Package alert 模块 5：预警模块。
//
// 需求要点：
//   - 依赖账户信息模块，拿到当前持有的交易对，持续监听：
//     资金费率反转（<0）、ADL、爆仓风险（强平价距离过近）；
//   - 资金平衡提示：合约:现货 = 1:4，双方误差超过阈值（默认 15%）提醒手动平衡；
//     （交易机器人不能自动调仓，因为涉及跨所转账）
//   - 预警写入数据库，并推送邮件。
//
// 调用方：自动交易模块每轮调用；也可由前端手动触发一次检查。
package alert

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"deltacrypto/internal/database"
	"deltacrypto/internal/exchange"
	"deltacrypto/internal/model"
	"deltacrypto/internal/notify"
	"deltacrypto/internal/service/account"
	"deltacrypto/internal/service/settings"
	"deltacrypto/internal/service/trade"
)

// dedupWindow 同类预警的去重窗口：窗口期内同币对同类型只发一次，避免邮件轰炸
const dedupWindow = 4 * time.Hour

// Service 预警模块服务
type Service struct {
	db       *database.DB
	hub      *exchange.Hub
	settings *settings.Service
	account  *account.Service
	trade    *trade.Service

	mu       sync.Mutex
	lastSent map[string]time.Time // 去重表：key = 类型|币对
}

// New 创建预警模块
func New(db *database.DB, hub *exchange.Hub, settings *settings.Service,
	accountSvc *account.Service, tradeSvc *trade.Service) *Service {
	return &Service{
		db: db, hub: hub, settings: settings, account: accountSvc, trade: tradeSvc,
		lastSent: make(map[string]time.Time),
	}
}

// CheckAll 执行一轮全部预警检查（自动交易模块每轮调用）。
// 返回本轮触发的预警（fast sell 需要“费率为负”这一信号）。
func (s *Service) CheckAll() []model.AlertRecord {
	var fired []model.AlertRecord

	// 1. 持仓相关预警：费率反转 / ADL / 爆仓
	positions, err := s.trade.OpenPositions()
	if err == nil {
		for _, p := range positions {
			if r, ok := s.checkFundingNegative(p); ok {
				fired = append(fired, r)
			}
			if r, ok := s.checkADL(p); ok {
				fired = append(fired, r)
			}
		}
	}
	if r, ok := s.checkLiquidation(); ok {
		fired = append(fired, r)
	}

	// 2. 资金平衡预警
	if r, ok := s.checkBalance(); ok {
		fired = append(fired, r)
	}
	return fired
}

// checkFundingNegative 资金费率反转预警：当前费率 < 0（持有中就要亏钱了）
func (s *Service) checkFundingNegative(p model.HedgePosition) (model.AlertRecord, bool) {
	swapEx, err := s.hub.Swap()
	if err != nil {
		return model.AlertRecord{}, false
	}
	rates, err := swapEx.FetchFundingRates([]string{p.Symbol})
	if err != nil {
		return model.AlertRecord{}, false
	}
	if rate := rates[p.Symbol]; rate < 0 {
		return s.fire("funding_negative", p.Symbol, "critical",
			fmt.Sprintf("【费率反转】%s 当前资金费率 %.4f%% 已转负，建议尽快平仓（fast sell）", p.Symbol, rate))
	}
	return model.AlertRecord{}, false
}

// checkADL 预警：币安 ADL 排名 >= 4（被自动减仓风险高）
func (s *Service) checkADL(p model.HedgePosition) (model.AlertRecord, bool) {
	swapEx, err := s.hub.Swap()
	if err != nil {
		return model.AlertRecord{}, false
	}
	rank, err := swapEx.FetchADLRank(p.Symbol)
	if err != nil {
		return model.AlertRecord{}, false // 查询失败跳过（如该所不支持）
	}
	if rank >= 4 {
		return s.fire("adl", p.Symbol, "critical",
			fmt.Sprintf("【ADL 风险】%s 合约 ADL 排名 %d/5，被自动减仓风险高，建议平仓", p.Symbol, rank))
	}
	return model.AlertRecord{}, false
}

// checkLiquidation 爆仓预警：标记价距强平价不足 10%
func (s *Service) checkLiquidation() (model.AlertRecord, bool) {
	overview, err := s.account.Overview()
	if err != nil {
		return model.AlertRecord{}, false
	}
	for _, pos := range overview.SwapPositions {
		if pos.LiquidationPrice <= 0 || pos.MarkPrice <= 0 {
			continue
		}
		// 空仓：价格涨到强平价爆仓；距离 = (强平价-标记价)/标记价
		distance := (pos.LiquidationPrice - pos.MarkPrice) / pos.MarkPrice * 100
		if pos.Side == "long" {
			distance = (pos.MarkPrice - pos.LiquidationPrice) / pos.MarkPrice * 100
		}
		if distance < 10 {
			return s.fire("liquidation", pos.Symbol, "critical",
				fmt.Sprintf("【爆仓风险】%s 距强平价仅剩 %.2f%%（标记 %.4f / 强平 %.4f），请立即处理",
					pos.Symbol, distance, pos.MarkPrice, pos.LiquidationPrice))
		}
	}
	return model.AlertRecord{}, false
}

// checkBalance 资金平衡预警：合约:现货 偏离 1:N 超过阈值（提醒手动平衡，机器人不自动调仓）
func (s *Service) checkBalance() (model.AlertRecord, bool) {
	spotTotal, swapTotal, deviation, err := s.account.BalanceRatio()
	if err != nil {
		return model.AlertRecord{}, false
	}
	threshold := s.settings.GetFloat(settings.KeyBalanceWarnPct)
	if threshold <= 0 {
		threshold = 15
	}
	if deviation > threshold {
		return s.fire("balance", "", "warning",
			fmt.Sprintf("【资金平衡】现货 %.2fU / 合约 %.2fU，偏离目标比例 %.1f%%（阈值 %.1f%%），请手动平衡资金",
				spotTotal, swapTotal, deviation, threshold))
	}
	return model.AlertRecord{}, false
}

// fire 触发一条预警：去重 -> 写库 -> 发邮件
func (s *Service) fire(alertType, symbol, level, message string) (model.AlertRecord, bool) {
	rec := model.AlertRecord{
		Time: time.Now(), Type: alertType, Symbol: symbol, Level: level, Message: message,
	}

	// 去重：窗口期内同类型同币对已发过则跳过
	key := alertType + "|" + symbol
	s.mu.Lock()
	if last, ok := s.lastSent[key]; ok && time.Since(last) < dedupWindow {
		s.mu.Unlock()
		return rec, false
	}
	s.lastSent[key] = rec.Time
	s.mu.Unlock()

	// 发邮件（失败仅记录，不影响预警本身）
	mailErr := notify.SendMail(s.settings.GetMailConfig(), "套利工具预警 - "+alertType, message)
	rec.MailSent = mailErr == nil

	// 写库
	res, err := s.db.Exec(
		`INSERT INTO alert_log(type, symbol, level, message, mail_sent) VALUES(?,?,?,?,?)`,
		alertType, symbol, level, message, boolToInt(rec.MailSent))
	if err == nil {
		rec.ID, _ = res.LastInsertId()
	}

	// 同步写一条操作日志，前端“日志”页也能看到预警
	logMsg := message
	if mailErr != nil {
		logMsg += "（邮件发送失败: " + mailErr.Error() + "）"
	}
	s.trade.LogExternal("alert", level, alertType, symbol, logMsg)
	return rec, true
}

// Records 查询最近的预警记录（前端预警页展示）
func (s *Service) Records(limit int) ([]model.AlertRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT id, time, type, symbol, level, message, mail_sent FROM alert_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.AlertRecord, 0) // 初始化为空数组，JSON 返回 [] 而非 null
	for rows.Next() {
		var r model.AlertRecord
		var mailSent int
		if err := rows.Scan(&r.ID, &r.Time, &r.Type, &r.Symbol, &r.Level, &r.Message, &mailSent); err != nil {
			return nil, err
		}
		r.MailSent = mailSent == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// Summary 给自动交易模块用的简短摘要（邮件正文片段）
func Summary(fired []model.AlertRecord) string {
	if len(fired) == 0 {
		return "本轮无预警"
	}
	var b strings.Builder
	for _, r := range fired {
		b.WriteString("- " + r.Message + "\n")
	}
	return b.String()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
