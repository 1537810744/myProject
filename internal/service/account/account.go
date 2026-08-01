// 【阅读顺序 11】模块 4：账户信息模块（总览部分）。
// 聚合两所资金与持仓，给出账户总览。核心公式：
//
//	购买力 = min(合约账户可用 × 杠杆, 现货账户总额)
//
// 为什么取 min？—— 买现货要花“现货资金”，开合约空单要占“合约保证金”，
// 两条腿必须【同时】有钱。取 min 就是“短板决定论”：钱再多，缺任一条腿的额度就开不齐一组。
// 语法点预览：if err == nil 降级模式、命名返回值、minFloat/absFloat 小工具、time.Now。
package account

// import 导入用到的包。
import (
	"fmt"     // 格式化（对账消息拼接）
	"strings" // 字符串（从币对拆基础币）
	"time"    // 时间

	"deltacrypto/internal/database"         // 数据库
	"deltacrypto/internal/exchange"         // 交易所抽象层
	"deltacrypto/internal/model"            // 数据结构
	"deltacrypto/internal/service/settings" // 设置模块
	"deltacrypto/internal/service/trade"    // 交易模块（读持仓表）
)

// Service 账户信息模块服务。
type Service struct {
	db       *database.DB      // 数据库（资金费流水/收益快照的读写）
	hub      *exchange.Hub     // 交易所连接管理器
	settings *settings.Service // 参数中心
	trade    *trade.Service    // 交易模块（持仓数据来自它落库的 hedge_position 表）
	startAt  time.Time         // time.Time 程序启动时间（算运行时长）
}

// New 创建账户模块。
func New(db *database.DB, hub *exchange.Hub, settings *settings.Service, tradeSvc *trade.Service) *Service {
	return &Service{db: db, hub: hub, settings: settings, trade: tradeSvc, startAt: time.Now()}
}

// Overview 聚合账户总览（前端账户页 + 自动交易买入判断的核心输入）。
func (s *Service) Overview() (*model.AccountOverview, error) {
	// 各切片初始化为空数组，保证 JSON 返回 [] 而非 null（前端遍历安全）。
	out := &model.AccountOverview{
		RunningSince:  s.startAt,                        // 启动时间
		Balances:      make([]model.ExchangeBalance, 0), // 空数组
		Hedges:        make([]model.HedgePosition, 0),
		SwapPositions: make([]model.SwapPositionInfo, 0),
	}

	// 现货腿交易所资金（gate）。
	// 为什么用“if err == nil”嵌套而不是出错就 return？
	// —— 总览要尽力展示“能拿到的一切”：一条腿连不上，另一条照样展示。
	//   这种“部分失败也返回部分成功”的模式，对只读展示页很合适。
	//   （对比：交易下单是写操作，失败必须 return，不能假装成功。）
	if spotEx, err := s.hub.Spot(); err == nil { // 取连接，成功才进 if（if 带初始化）
		free, used, total, err := spotEx.FetchUSDTBalance() // 拉 USDT 余额
		if err == nil {                                     // 拉余额成功
			out.Balances = append(out.Balances, model.ExchangeBalance{ // 追加资金信息
				Exchange: spotEx.ID(), MarketType: "spot", // 现货
				USDTFree: free, USDTUsed: used, USDTTotal: total,
			})
		}
	}
	// 合约腿交易所资金（binance）+ 实时持仓。
	if swapEx, err := s.hub.Swap(); err == nil {
		free, used, total, err := swapEx.FetchUSDTBalance()
		if err == nil {
			out.Balances = append(out.Balances, model.ExchangeBalance{
				Exchange: swapEx.ID(), MarketType: "swap", // 合约
				USDTFree: free, USDTUsed: used, USDTTotal: total,
			})
		}
		// 合约实时持仓（含强平价、未实现盈亏——预警模块要用强平价）。
		if positions, err := swapEx.FetchSwapPositions(); err == nil {
			out.SwapPositions = positions // 直接赋值
		}
	}

	// 聚合总资金（所有交易所 USDT 合计）。
	for _, b := range out.Balances { // 遍历
		out.TotalUSDT += b.USDTTotal // 累加
	}

	// 购买力 = min(现货总额, 合约可用×杠杆)。见文件头解释。
	leverage := s.settings.GetFloat(settings.KeyLeverage) // 杠杆从设置读
	if leverage <= 0 {
		leverage = 4 // 设置读不到合理值时兜底默认 4
	}
	var spotTotal, swapFree float64  // 声明两个累加变量
	for _, b := range out.Balances { // 遍历
		if b.MarketType == "spot" {
			spotTotal = b.USDTTotal // 现货购买力用全部现货资金
		} else {
			swapFree = b.USDTFree // 合约端用【可用】保证金（被占用的保证金不能再用）
		}
	}
	out.PurchasingPower = minFloat(spotTotal, swapFree*leverage) // 公式落地

	// 当前持有的对冲对（来自数据库）。
	if hedges, err := s.trade.OpenPositions(); err == nil && hedges != nil { // 两个条件
		out.Hedges = hedges
	}
	return out, nil
}

// PurchasingPower 单独提供购买力查询（自动交易模块高频调用）。
// 为什么拆出来？—— 自动交易每轮只要“能买多少”，不需要持仓/余额明细。
// 专用方法少拉一半数据，代码意图也更明确。
func (s *Service) PurchasingPower() (float64, error) {
	spotEx, err := s.hub.Spot()
	if err != nil {
		return 0, err
	}
	swapEx, err := s.hub.Swap()
	if err != nil {
		return 0, err
	}
	_, _, spotTotal, err := spotEx.FetchUSDTBalance() // 只要 total，前两个用 _ 丢弃
	if err != nil {
		return 0, err
	}
	swapFree, _, _, err := swapEx.FetchUSDTBalance() // 只要 free
	if err != nil {
		return 0, err
	}
	leverage := s.settings.GetFloat(settings.KeyLeverage)
	if leverage <= 0 {
		leverage = 4
	}
	return minFloat(spotTotal, swapFree*leverage), nil
}

// BalanceRatio 计算现货:合约的实际比例与目标偏差百分比（预警模块用）。
// 目标：合约:现货 = 1:N（N 来自设置 balance_ratio，默认 4，即现货应是合约的 4 倍）。
// 返回偏差% = |实际现货 - 目标现货| / 目标现货 * 100。
func (s *Service) BalanceRatio() (spotTotal, swapTotal, deviationPct float64, err error) {
	spotEx, err := s.hub.Spot()
	if err != nil {
		return 0, 0, 0, err
	}
	swapEx, err := s.hub.Swap()
	if err != nil {
		return 0, 0, 0, err
	}
	_, _, spotTotal, err = spotEx.FetchUSDTBalance() // 注意这里是 = 不是 :=（spotTotal 已声明）
	if err != nil {
		return 0, 0, 0, err
	}
	_, _, swapTotal, err = swapEx.FetchUSDTBalance()
	if err != nil {
		return 0, 0, 0, err
	}
	ratio := s.settings.GetFloat(settings.KeyBalanceRatio) // 目标比例 N
	if ratio <= 0 {
		ratio = 4
	}
	// 为什么监控资金平衡？—— 随着盈亏和交易，两腿资金会逐渐失衡（比如现货一直亏、
	// 合约一直赚，现货钱不够买下一组了）。机器人不自动转账（需求明确），所以算偏差
	// 给预警模块，提醒用户手动调仓。
	targetSpot := swapTotal * ratio // 目标现货资金 = 合约资金 × N
	if targetSpot <= 0 {
		return spotTotal, swapTotal, 0, nil // 合约资金为 0 无法计算，跳过
	}
	deviationPct = absFloat(spotTotal-targetSpot) / targetSpot * 100 // |实际-目标|/目标
	return spotTotal, swapTotal, deviationPct, nil
}

// minFloat 两个浮点数取较小。
func minFloat(a, b float64) float64 {
	if a < b { // 比较
		return a // a 更小
	}
	return b
}

// absFloat 绝对值。
func absFloat(a float64) float64 {
	if a < 0 {
		return -a // 负数取反变正
	}
	return a
}

// ---------- 对账（风险控制） ----------

// reconcileTolerance 对账允许的数量偏差比例（5% 内算一致）。
const reconcileTolerance = 0.05

// Reconcile 把数据库记录的持仓和交易所【实际】持仓/余额做比对，找出不一致。
// 为什么必须做？—— 程序重启、订单部分成交、交易所强平等都可能让"自己记的账"
// 和"交易所的真实仓位"分家。如果不对账，程序会基于错误的前提继续交易。
// 本函数只【检测并报告】不一致，不自动改仓位（自动修正风险太高，先人工处理）。
func (s *Service) Reconcile() (*model.ReconcileReport, error) {
	report := &model.ReconcileReport{
		CheckedAt:    time.Now(),
		Items:        make([]model.ReconcileItem, 0),
		IsConsistent: true,
	}
	positions, err := s.trade.OpenPositions() // 数据库里的 open 持仓
	if err != nil {
		return nil, err
	}
	swapEx, err := s.hub.Swap() // 合约腿连接（对账必须有凭证）
	if err != nil {
		return nil, fmt.Errorf("无法对账（合约连接不可用）: %w", err)
	}
	spotEx, err := s.hub.Spot()
	if err != nil {
		return nil, fmt.Errorf("无法对账（现货连接不可用）: %w", err)
	}
	// 拉一次交易所侧的全部合约持仓，按币对建索引方便查。
	swapPositions, err := swapEx.FetchSwapPositions()
	if err != nil {
		return nil, fmt.Errorf("拉取合约持仓失败: %w", err)
	}
	swapBySymbol := make(map[string]model.SwapPositionInfo)
	for _, p := range swapPositions {
		swapBySymbol[p.Symbol] = p
	}

	for _, pos := range positions {
		item := model.ReconcileItem{
			PositionID:   pos.ID,
			Symbol:       pos.Symbol,
			DBSwapAmount: pos.SwapAmount,
			DBSpotAmount: pos.SpotAmount,
			Status:       "ok",
		}
		// ① 合约腿：交易所有没有这个持仓、数量是否一致。
		if sp, ok := swapBySymbol[pos.Symbol]; ok {
			item.ExSwapAmount = sp.Contracts
			diff := absFloat(sp.Contracts-pos.SwapAmount) / maxFloat(pos.SwapAmount, 1e-9)
			if diff > reconcileTolerance {
				item.Status = "amount_mismatch"
				item.Message = fmt.Sprintf("合约数量不一致：库 %.6f / 所 %.6f", pos.SwapAmount, sp.Contracts)
			}
		} else {
			item.Status = "missing_swap"
			item.Message = "交易所没有该币对的合约持仓"
		}
		// ② 现货腿：库里记的现货数量，交易所余额里有没有。
		base := strings.SplitN(pos.Symbol, "/", 2)[0] // "BTC/USDT" -> "BTC"
		_, _, spotTotal, err := spotEx.FetchCurrencyBalance(base)
		if err != nil {
			item.Status = "spot_query_failed"
			item.Message = "查询现货余额失败: " + err.Error()
		} else {
			item.ExSpotAmount = spotTotal
			if spotTotal < pos.SpotAmount*(1-reconcileTolerance) {
				if item.Status == "ok" {
					item.Status = "missing_spot"
				}
				item.Message = fmt.Sprintf("现货余额不足：库 %.6f / 所 %.6f", pos.SpotAmount, spotTotal)
			}
		}
		// 汇总
		report.Total++
		if item.Status != "ok" {
			report.Mismatches++
			report.IsConsistent = false
		}
		report.Items = append(report.Items, item)
	}
	return report, nil
}

// maxFloat 取两个浮点数较大（对账里避免除 0 用）。
func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// Health 健康检查（/api/health 用）：数据库连通、交易所连接就绪、是否停机、运行时长。
func (s *Service) Health() model.HealthStatus {
	h := model.HealthStatus{Version: model.Version}
	// 数据库：Ping 一下能否连通。
	if err := s.db.Ping(); err != nil {
		h.DB = "error: " + err.Error()
	} else {
		h.DB = "ok"
	}
	// 交易所两条交易连接是否就绪。
	h.HubReady = s.hub.Ready()
	// 是否被熔断/手动停机。
	h.Halted = s.settings.IsHalted()
	// 总状态：数据库或连接有问题 = degraded，否则 ok。
	h.Status = "ok"
	if h.DB != "ok" || !h.HubReady {
		h.Status = "degraded" // 降级但仍可运行（比如没配 API 也能看行情）
	}
	h.UptimeSeconds = int64(time.Since(s.startAt).Seconds())
	return h
}
