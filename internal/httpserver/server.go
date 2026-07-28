// 【阅读顺序 16】HTTP 服务层（后端唯一对外的门）。
//
// 本文件职责：把 7 个模块的方法包装成 RESTful API 给前端调用 + 托管前端静态文件。
// 阅读目的：看 Run() 里的路由表即可——每个 URL 对应哪个模块的哪个方法，一目了然；
// 处理器只做“解析参数 → 调模块 → 包 JSON”，不含业务逻辑。
// 注意（需求约定）：HTTP 只服务前端；后端模块之间不走 HTTP，直接包内函数调用。
// 无 JWT 鉴权，仅监听 localhost（个人本机工具）。
package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"deltacrypto/internal/config"
	"deltacrypto/internal/model"
	"deltacrypto/internal/service/account"
	"deltacrypto/internal/service/alert"
	"deltacrypto/internal/service/apiconfig"
	"deltacrypto/internal/service/autotrade"
	"deltacrypto/internal/service/market"
	"deltacrypto/internal/service/settings"
	"deltacrypto/internal/service/trade"
)

// Server 聚合全部模块，注册路由
type Server struct {
	cfg       *config.Config
	apiconfig *apiconfig.Service
	market    *market.Service
	trade     *trade.Service
	account   *account.Service
	alert     *alert.Service
	settings  *settings.Service
	autotrade *autotrade.Service
}

// New 创建 HTTP 服务（依赖注入：所有模块在此装配）
func New(cfg *config.Config,
	apiconfigSvc *apiconfig.Service, marketSvc *market.Service, tradeSvc *trade.Service,
	accountSvc *account.Service, alertSvc *alert.Service, settingsSvc *settings.Service,
	autotradeSvc *autotrade.Service) *Server {
	return &Server{
		cfg: cfg, apiconfig: apiconfigSvc, market: marketSvc, trade: tradeSvc,
		account: accountSvc, alert: alertSvc, settings: settingsSvc, autotrade: autotradeSvc,
	}
}

// Run 启动 HTTP 服务（阻塞）
func (s *Server) Run() error {
	mux := http.NewServeMux()

	// —— 模块 1：API 配置 ——
	mux.HandleFunc("/api/config/apis", s.handleAPIs)       // GET 列表 / POST 保存
	mux.HandleFunc("/api/config/apis/", s.handleAPIDelete) // DELETE /{exchange}
	mux.HandleFunc("/api/config/test", s.handleAPITest)    // POST 测试连通性

	// —— 模块 2：行情 ——
	mux.HandleFunc("/api/market/candidates", s.handleCandidates) // GET 待买入列表

	// —— 模块 3：交易 ——
	mux.HandleFunc("/api/trade/open", s.handleTradeOpen)   // POST 手动建仓
	mux.HandleFunc("/api/trade/close", s.handleTradeClose) // POST 手动平仓
	mux.HandleFunc("/api/trade/logs", s.handleTradeLogs)   // GET 日志

	// —— 模块 4：账户 ——
	mux.HandleFunc("/api/account/overview", s.handleOverview)     // GET 账户总览
	mux.HandleFunc("/api/position/detail", s.handlePositionDetail) // GET 持仓详情（统计+双腿+敞口）
	mux.HandleFunc("/api/position/fills", s.handlePositionFills)   // GET 成交记录
	mux.HandleFunc("/api/position/funding", s.handlePositionFunding) // GET 资金费流水
	mux.HandleFunc("/api/position/profit", s.handlePositionProfit)   // GET 收益曲线

	// —— 模块 5：预警 ——
	mux.HandleFunc("/api/alert/records", s.handleAlertRecords) // GET 预警记录
	mux.HandleFunc("/api/alert/check", s.handleAlertCheck)     // POST 立即检查一轮

	// —— 模块 6：自动交易 ——
	mux.HandleFunc("/api/autotrade/status", s.handleAutoStatus) // GET 状态
	mux.HandleFunc("/api/autotrade/run", s.handleAutoRun)       // POST 立即执行一轮

	// —— 模块 7：设置 ——
	mux.HandleFunc("/api/settings", s.handleSettings) // GET 全部 / POST 批量保存

	// —— 前端静态文件（同源部署，无跨域）——
	mux.Handle("/", http.FileServer(http.Dir("./web/static")))

	return http.ListenAndServe(s.cfg.ListenAddr, mux)
}

// ---------- 模块 1 处理器 ----------

func (s *Server) handleAPIs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.apiconfig.List()
		respond(w, list, err)
	case http.MethodPost:
		var req struct {
			Exchange  string `json:"exchange"`
			Label     string `json:"label"`
			APIKey    string `json:"api_key"`
			APISecret string `json:"api_secret"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondErr(w, err)
			return
		}
		respond(w, map[string]string{"status": "ok"}, s.apiconfig.Save(req.Exchange, req.Label, req.APIKey, req.APISecret))
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAPIDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	exchangeID := strings.TrimPrefix(r.URL.Path, "/api/config/apis/")
	respond(w, map[string]string{"status": "ok"}, s.apiconfig.Delete(exchangeID))
}

func (s *Server) handleAPITest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Exchange  string `json:"exchange"`
		Role      string `json:"role"` // spot / swap
		APIKey    string `json:"api_key"`
		APISecret string `json:"api_secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErr(w, err)
		return
	}
	respond(w, s.apiconfig.Test(req.Exchange, req.Role, req.APIKey, req.APISecret), nil)
}

// ---------- 模块 2 处理器 ----------

func (s *Server) handleCandidates(w http.ResponseWriter, r *http.Request) {
	list, err := s.market.Candidates()
	respond(w, list, err)
}

// ---------- 模块 3 处理器 ----------

func (s *Server) handleTradeOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Symbol    string  `json:"symbol"`
		TotalUSDT float64 `json:"total_usdt"`
		AtomUSDT  float64 `json:"atom_usdt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErr(w, err)
		return
	}
	result, err := s.trade.Open(tradeRequestOf(req.Symbol, "open", req.TotalUSDT, req.AtomUSDT))
	respond(w, result, err)
}

func (s *Server) handleTradeClose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Symbol     string  `json:"symbol"`
		PositionID int64   `json:"position_id"`
		TotalUSDT  float64 `json:"total_usdt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErr(w, err)
		return
	}
	result, err := s.trade.Close(tradeRequestOf(req.Symbol, "close", req.TotalUSDT, 0), req.PositionID)
	respond(w, result, err)
}

func (s *Server) handleTradeLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := s.trade.Logs(200)
	respond(w, logs, err)
}

// ---------- 模块 4 处理器 ----------

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	overview, err := s.account.Overview()
	respond(w, overview, err)
}

// handlePositionDetail 持仓详情（统计 + 双腿 + 敞口），?symbol=BTC/USDT
func (s *Server) handlePositionDetail(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	if symbol == "" {
		respondErr(w, errSymbolRequired)
		return
	}
	// 打开详情页时顺带同步一次资金费流水（有凭证才生效，无凭证静默跳过）
	s.account.SyncFundingPayments()
	detail, err := s.account.PositionDetail(symbol)
	respond(w, detail, err)
}

// handlePositionFills 成交记录，?symbol=
func (s *Server) handlePositionFills(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	if symbol == "" {
		respondErr(w, errSymbolRequired)
		return
	}
	fills, err := s.trade.Fills(symbol, 200)
	respond(w, fills, err)
}

// handlePositionFunding 资金费流水，?symbol=
func (s *Server) handlePositionFunding(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	if symbol == "" {
		respondErr(w, errSymbolRequired)
		return
	}
	records, err := s.account.FundingRecords(symbol, 100)
	respond(w, records, err)
}

// handlePositionProfit 收益曲线，?symbol=
func (s *Server) handlePositionProfit(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol")
	if symbol == "" {
		respondErr(w, errSymbolRequired)
		return
	}
	points, err := s.account.ProfitHistory(symbol, 500)
	respond(w, points, err)
}

// ---------- 模块 5 处理器 ----------

func (s *Server) handleAlertRecords(w http.ResponseWriter, r *http.Request) {
	records, err := s.alert.Records(100)
	respond(w, records, err)
}

func (s *Server) handleAlertCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	fired := s.alert.CheckAll()
	respond(w, fired, nil)
}

// ---------- 模块 6 处理器 ----------

func (s *Server) handleAutoStatus(w http.ResponseWriter, r *http.Request) {
	respond(w, s.autotrade.GetStatus(), nil)
}

func (s *Server) handleAutoRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	go s.autotrade.RunOnceManual() // 异步执行，前端轮询状态
	respond(w, map[string]string{"status": "started"}, nil)
}

// ---------- 模块 7 处理器 ----------

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		respond(w, s.settings.All(), nil)
	case http.MethodPost:
		var kv map[string]string
		if err := json.NewDecoder(r.Body).Decode(&kv); err != nil {
			respondErr(w, err)
			return
		}
		respond(w, map[string]string{"status": "ok"}, s.settings.SetBatch(kv))
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// tradeRequestOf 构造交易请求（TotalUSDT/AtomUSDT 为 0 时交易模块自动读取设置）
func tradeRequestOf(symbol, action string, totalUSDT, atomUSDT float64) model.TradeRequest {
	return model.TradeRequest{Symbol: symbol, Action: action, TotalUSDT: totalUSDT, AtomUSDT: atomUSDT}
}

// errSymbolRequired symbol 参数缺失错误（持仓详情接口共用）
var errSymbolRequired = fmt.Errorf("缺少 symbol 参数")

// ---------- 工具函数 ----------

// respond 统一 JSON 响应：err 为 nil 返回数据，否则返回错误信息
func respond(w http.ResponseWriter, data any, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func respondErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
