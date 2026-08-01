// 【阅读顺序 16】HTTP 服务层（后端唯一对外的门）。
// 职责：把 7 个模块的方法包装成 RESTful API 给前端调用 + 托管前端静态文件。
// 这个文件是【薄控制器层】：只做“解析参数 → 调模块 → 包 JSON”，没有任何策略逻辑。
// 为什么？—— 逻辑层保持纯粹、不绑定 HTTP；这样逻辑层能被任何调用方复用
// （前端 HTTP、自动交易循环都能调同一套代码）。
// HTTP 只服务前端；后端模块之间不走 HTTP（直接包内函数调用，无 HTTP 开销）。
// 无 JWT 鉴权，仅监听 localhost（个人本机工具）。路由用标准库 NewServeMux，
// 20 个端点用不着 gin/echo 这种带中间件生态的框架。
// 语法点预览：http.NewServeMux、HandleFunc、方法分发 switch、匿名结构体、json.Decode、
// Query().Get、go 异步、any 参数、统一响应函数。
package httpserver

// import 导入用到的包。
import (
	"context"       // 优雅停机用的取消上下文
	"encoding/json" // JSON 编解码
	"fmt"           // 格式化
	"log"           // 请求日志
	"net/http"      // HTTP 标准库
	"strings"       // 字符串
	"time"          // 超时/耗时

	"deltacrypto/internal/config"            // 配置
	"deltacrypto/internal/model"             // 数据结构
	"deltacrypto/internal/service/account"   // 账户模块
	"deltacrypto/internal/service/alert"     // 预警模块
	"deltacrypto/internal/service/apiconfig" // API 配置模块
	"deltacrypto/internal/service/autotrade" // 自动交易模块
	"deltacrypto/internal/service/market"    // 行情模块
	"deltacrypto/internal/service/settings"  // 设置模块
	"deltacrypto/internal/service/trade"     // 交易模块
)

// Server 聚合全部模块，注册路由。这是依赖注入的“最后一站”：
// main.go 把所有模块 new 出来全塞进来，Server 负责把模块方法暴露成 URL。
type Server struct {
	cfg       *config.Config     // 配置
	apiconfig *apiconfig.Service // 模块①
	market    *market.Service    // 模块②
	trade     *trade.Service     // 模块③
	account   *account.Service   // 模块④
	alert     *alert.Service     // 模块⑤
	settings  *settings.Service  // 模块⑦
	autotrade *autotrade.Service // 模块⑥

	srv *http.Server // 底层 http.Server：支持超时配置与优雅停机（Shutdown）
}

// New 创建 HTTP 服务。
func New(cfg *config.Config,
	apiconfigSvc *apiconfig.Service, marketSvc *market.Service, tradeSvc *trade.Service,
	accountSvc *account.Service, alertSvc *alert.Service, settingsSvc *settings.Service,
	autotradeSvc *autotrade.Service) *Server {
	return &Server{
		cfg: cfg, apiconfig: apiconfigSvc, market: marketSvc, trade: tradeSvc,
		account: accountSvc, alert: alertSvc, settings: settingsSvc, autotrade: autotradeSvc,
	}
}

// Run 启动 HTTP 服务（阻塞，直到出错才返回——所以 main 让它占住主 goroutine）。
// 路由表就是下面这一串 HandleFunc：URL → 模块方法，一目了然。
func (s *Server) Run() error {
	mux := http.NewServeMux() // 标准库路由器：给路径绑处理函数

	// —— 模块 1：API 配置 ——
	mux.HandleFunc("/api/config/apis", s.handleAPIs)       // GET 列表 / POST 保存
	mux.HandleFunc("/api/config/apis/", s.handleAPIDelete) // DELETE /{exchange}
	// 注意“/api/config/apis/”（结尾带斜杠）是【前缀匹配】——/api/config/apis/binance
	// 会命中它，handleAPIDelete 里再取路径最后一段做参数。这是标准库路由做“路径参数”的手法。
	mux.HandleFunc("/api/config/test", s.handleAPITest) // POST 测试连通性

	// —— 模块 2：行情 ——
	mux.HandleFunc("/api/market/candidates", s.handleCandidates) // GET 待买入列表

	// —— 模块 3：交易 ——
	mux.HandleFunc("/api/trade/open", s.handleTradeOpen)   // POST 手动建仓
	mux.HandleFunc("/api/trade/close", s.handleTradeClose) // POST 手动平仓
	mux.HandleFunc("/api/trade/logs", s.handleTradeLogs)   // GET 日志

	// —— 模块 4：账户 ——
	mux.HandleFunc("/api/account/overview", s.handleOverview)        // GET 账户总览
	mux.HandleFunc("/api/position/detail", s.handlePositionDetail)   // GET 持仓详情
	mux.HandleFunc("/api/position/fills", s.handlePositionFills)     // GET 成交记录
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

	// —— 运维/风险控制端点（本次升级新增）——
	mux.HandleFunc("/api/health", s.handleHealth)                // GET 健康检查（监控探针用）
	mux.HandleFunc("/api/trade/halt", s.handleHalt)              // POST 手动停机（杀开关）
	mux.HandleFunc("/api/trade/resume", s.handleResume)          // POST 恢复交易
	mux.HandleFunc("/api/trade/closeall", s.handleCloseAll)      // POST 紧急全平
	mux.HandleFunc("/api/position/reconcile", s.handleReconcile) // POST 触发一次对账

	// —— 前端静态文件（同源部署，无跨域）——
	// http.FileServer 直接托管一个目录。前面所有 /api/* 路由优先匹配，
	// 剩下的请求（/、/assets/xxx.js）都落这里。这就是“前后端同源”：
	// 浏览器只需访问一个端口，不用配 CORS。
	mux.Handle("/", http.FileServer(http.Dir("./web/static")))

	// —— 中间件链：日志(最外层，能记下所有请求) → 鉴权 → 路由 ——
	var handler http.Handler = mux
	handler = s.authMiddleware(handler)    // 可选 Bearer 鉴权（配置了 AUTH_TOKEN 才生效）
	handler = s.loggingMiddleware(handler) // 请求日志（方法/路径/状态码/耗时）

	// 用带超时和优雅停机的 http.Server，而不是裸 ListenAndServe。
	// 为什么设超时？—— 防止慢速/挂起的连接把服务器拖死（健壮性）。
	s.srv = &http.Server{
		Addr:         s.cfg.ListenAddr,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,  // 读请求体超时
		WriteTimeout: 120 * time.Second, // 交易接口执行较久（一轮双腿可能几十秒），给足
		IdleTimeout:  120 * time.Second, // 长连接空闲回收
	}
	return s.srv.ListenAndServe() // 监听端口、阻塞分发请求
}

// Shutdown 优雅停机：停止接收新请求，等待在途请求处理完（最多等 timeout），然后关闭。
// main 收到 Ctrl+C / SIGTERM 时调用它，保证不丢正在执行的交易请求。
func (s *Server) Shutdown(timeout time.Duration) error {
	if s.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

// ---------- 中间件（运维/安全） ----------

// loggingMiddleware 请求日志：记录每个请求的方法/路径/状态码/耗时，方便排查问题。
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// 标准 http.ResponseWriter 拿不到写出的状态码，所以包一层"能记状态码"的写入器。
		lw := &loggingWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(lw, r)
		log.Printf("[http] %s %s -> %d (%s)", r.Method, r.URL.Path, lw.status, time.Since(start).Round(time.Millisecond))
	})
}

// loggingWriter 包装 ResponseWriter，多记一个状态码。
type loggingWriter struct {
	http.ResponseWriter
	status int
}

// WriteHeader 拦截状态码写入，先记下再转发。
func (w *loggingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// authMiddleware 可选鉴权：cfg.AuthToken 非空时，/api/* 请求必须带
// `Authorization: Bearer <token>` 头，否则 401。
// /api/health 放行（监控探针也要能访问）。
// ⚠️ 默认 AuthToken 为空 = 不鉴权（本机 localhost），启用后前端需配合带 token。
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	token := s.cfg.AuthToken
	if token == "" { // 没配置令牌：直接放行（零行为变化）
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 非 API 路径（静态文件）或健康检查：放行。
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/health" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Authorization") == "Bearer "+token { // 精确匹配令牌
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized) // 401 未授权
		json.NewEncoder(w).Encode(map[string]string{"error": "未授权：缺少或错误的 Authorization 头"})
	})
}

// ---------- 运维/风险控制处理器 ----------

// handleHealth 健康检查：数据库/交易所连接/停机状态/运行时长。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	respond(w, s.account.Health(), nil)
}

// handleHalt 手动停机（杀开关）：立即停止自动交易。
func (s *Server) handleHalt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed) // 405：请求方法不支持
		return
	}
	respond(w, map[string]string{"status": "halted"}, s.autotrade.Halt("前端手动停机"))
}

// handleResume 恢复交易：解除停机并复位熔断器。
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	respond(w, map[string]string{"status": "resumed"}, s.autotrade.Resume())
}

// handleCloseAll 紧急全平：平掉所有持仓（杀开关的动作执行）。
func (s *Server) handleCloseAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	results := s.trade.CloseAllPositions()
	respond(w, map[string]any{"results": results}, nil)
}

// handleReconcile 触发一次对账：对比 DB 持仓与交易所实际持仓。
func (s *Server) handleReconcile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	report, err := s.account.Reconcile()
	respond(w, report, err)
}

// ---------- 模块 1 处理器 ----------

func (s *Server) handleAPIs(w http.ResponseWriter, r *http.Request) {
	// 每个 handler 的标准形态：按 r.Method（HTTP 方法）分发——
	// 同一个 URL 支持 GET（读）和 POST（写）两种操作。
	// w 是响应写入器，r 是请求对象。
	switch r.Method { // 按请求方法分支
	case http.MethodGet: // GET
		list, err := s.apiconfig.List() // 调模块方法
		respond(w, list, err)           // 统一响应
	case http.MethodPost: // POST
		// 用【匿名结构体】接收请求体——为什么不用 model.ExchangeAPI？
		// 请求体字段和数据库模型不完全一样（这里只要 4 个字段，没有 ID/CreatedAt），
		// 而“只在这个函数用一次”的结构用匿名定义最轻量。
		var req struct {
			Exchange  string `json:"exchange"`   // 交易所
			Label     string `json:"label"`      // 标签
			APIKey    string `json:"api_key"`    // Key
			APISecret string `json:"api_secret"` // Secret
		}
		// json.NewDecoder(r.Body).Decode(&req)：把 JSON 请求体填进结构体。
		// &req 传指针——Decode 要往 req 里写，必须传地址。
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil { // 解析请求体 JSON；失败=前端发的不是合法 JSON
			respondErr(w, err) // 参数解析失败 → 400
			return
		}
		respond(w, map[string]string{"status": "ok"}, s.apiconfig.Save(req.Exchange, req.Label, req.APIKey, req.APISecret))
	default: // 其它方法
		w.WriteHeader(http.StatusMethodNotAllowed) // 405：不支持的方法
	}
}

func (s *Server) handleAPIDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete { // 只接受 DELETE
		w.WriteHeader(http.StatusMethodNotAllowed) // 405：请求方法不支持。w 是响应写入器，WriteHeader 设置 HTTP 状态码（http.StatusMethodNotAllowed 是 Go 预定义的常量，就是数字 405）
		return
	}
	// 从 URL 路径取“交易所名”：/api/config/apis/binance -> binance。
	// strings.TrimPrefix 去掉固定前缀，剩下就是参数。
	exchangeID := strings.TrimPrefix(r.URL.Path, "/api/config/apis/")
	respond(w, map[string]string{"status": "ok"}, s.apiconfig.Delete(exchangeID))
}

func (s *Server) handleAPITest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed) // 405：请求方法不支持。w 是响应写入器，WriteHeader 设置 HTTP 状态码（http.StatusMethodNotAllowed 是 Go 预定义的常量，就是数字 405）
		return
	}
	var req struct { // 匿名结构体接收请求体
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
	list, err := s.market.Candidates() // 调行情模块
	respond(w, list, err)
}

// ---------- 模块 3 处理器 ----------

func (s *Server) handleTradeOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed) // 405：请求方法不支持。w 是响应写入器，WriteHeader 设置 HTTP 状态码（http.StatusMethodNotAllowed 是 Go 预定义的常量，就是数字 405）
		return
	}
	var req struct { // 匿名结构体
		Symbol    string  `json:"symbol"`
		TotalUSDT float64 `json:"total_usdt"`
		AtomUSDT  float64 `json:"atom_usdt"`
		// RequestID 幂等键：前端为"同一次点击"生成唯一 id；网络超时后重试同一请求，
		// 交易模块靠它识别出"已处理过"从而绝不重复下单。不传则跳过幂等。
		RequestID string `json:"request_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErr(w, err)
		return
	}
	result, err := s.trade.Open(tradeRequestOf(req.Symbol, "open", req.TotalUSDT, req.AtomUSDT, req.RequestID))
	respond(w, result, err)
}

func (s *Server) handleTradeClose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed) // 405：请求方法不支持。w 是响应写入器，WriteHeader 设置 HTTP 状态码（http.StatusMethodNotAllowed 是 Go 预定义的常量，就是数字 405）
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
	result, err := s.trade.Close(tradeRequestOf(req.Symbol, "close", req.TotalUSDT, 0, ""), req.PositionID)
	respond(w, result, err)
}

func (s *Server) handleTradeLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := s.trade.Logs(200) // 最近 200 条
	respond(w, logs, err)
}

// ---------- 模块 4 处理器 ----------

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	overview, err := s.account.Overview()
	respond(w, overview, err)
}

// handlePositionDetail 持仓详情（统计 + 双腿 + 敞口），?symbol=BTC/USDT。
func (s *Server) handlePositionDetail(w http.ResponseWriter, r *http.Request) {
	symbol := r.URL.Query().Get("symbol") // GET 参数从 URL 查询串取（?symbol=xxx）
	if symbol == "" {                     // 参数缺失
		respondErr(w, errSymbolRequired)
		return
	}
	// 打开详情页时顺带同步一次资金费流水（有凭证才生效）。
	// 为什么在这里同步？—— 用户打开详情页就是要看最新资金费，“按需同步”
	// 比每轮轮询省请求，体验也更即时。
	s.account.SyncFundingPayments()
	detail, err := s.account.PositionDetail(symbol)
	respond(w, detail, err)
}

// handlePositionFills 成交记录，?symbol=
func (s *Server) handlePositionFills(w http.ResponseWriter, r *http.Request) {
	// r.URL.Query()：解析 URL 里的查询参数（?a=b&c=d 那部分），返回一个 map；
	// .Get("symbol")：取 key 为 symbol 的值。所以 ?symbol=BTC/USDT 就能拿到 "BTC/USDT"。
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
	// r.URL.Query()：解析 URL 里的查询参数（?a=b&c=d 那部分），返回一个 map；
	// .Get("symbol")：取 key 为 symbol 的值。所以 ?symbol=BTC/USDT 就能拿到 "BTC/USDT"。
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
	// r.URL.Query()：解析 URL 里的查询参数（?a=b&c=d 那部分），返回一个 map；
	// .Get("symbol")：取 key 为 symbol 的值。所以 ?symbol=BTC/USDT 就能拿到 "BTC/USDT"。
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
		w.WriteHeader(http.StatusMethodNotAllowed) // 405：请求方法不支持。w 是响应写入器，WriteHeader 设置 HTTP 状态码（http.StatusMethodNotAllowed 是 Go 预定义的常量，就是数字 405）
		return
	}
	fired := s.alert.CheckAll() // 立即检查一轮
	respond(w, fired, nil)
}

// ---------- 模块 6 处理器 ----------

func (s *Server) handleAutoStatus(w http.ResponseWriter, r *http.Request) {
	respond(w, s.autotrade.GetStatus(), nil)
}

func (s *Server) handleAutoRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed) // 405：请求方法不支持。w 是响应写入器，WriteHeader 设置 HTTP 状态码（http.StatusMethodNotAllowed 是 Go 预定义的常量，就是数字 405）
		return
	}
	go s.autotrade.RunOnceManual() // 异步执行
	// 为什么用 go 异步？—— 一轮完整交易（卖出+买入）可能耗时几十秒，同步等的话
	// 浏览器请求直接挂到超时。先立刻返回“started”，前端轮询 /api/autotrade/status 看结果。
	respond(w, map[string]string{"status": "started"}, nil)
}

// ---------- 模块 7 处理器 ----------

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		respond(w, s.settings.All(), nil)
	case http.MethodPost:
		// 设置的请求体就是“key -> value”的 map（批量保存）。
		var kv map[string]string
		if err := json.NewDecoder(r.Body).Decode(&kv); err != nil {
			respondErr(w, err)
			return
		}
		respond(w, map[string]string{"status": "ok"}, s.settings.SetBatch(kv))
	default:
		w.WriteHeader(http.StatusMethodNotAllowed) // 405：请求方法不支持。w 是响应写入器，WriteHeader 设置 HTTP 状态码（http.StatusMethodNotAllowed 是 Go 预定义的常量，就是数字 405）
	}
}

// tradeRequestOf 构造交易请求。为什么用辅助函数而不是直接字面量？
// —— 前端只传 3 个字段，TradeRequest 有 5 个；这里把“HTTP 层字段”翻译成
// “业务层结构体”，并隐藏了 Action/DustUSDT 等前端不用关心的细节。
func tradeRequestOf(symbol, action string, totalUSDT, atomUSDT float64, requestID string) model.TradeRequest {
	return model.TradeRequest{Symbol: symbol, Action: action, TotalUSDT: totalUSDT, AtomUSDT: atomUSDT, RequestID: requestID}
}

// errSymbolRequired symbol 参数缺失错误（多个持仓详情接口共用同一个错误对象）。
var errSymbolRequired = fmt.Errorf("缺少 symbol 参数")

// ---------- 工具函数 ----------

// respond 统一 JSON 响应。为什么统一？——
//
//	a) 消除每个 handler 重复的“设 Content-Type → 编码 JSON”样板代码；
//	b) 保证响应格式一致：成功包 {"data": ...}，失败包 {"error": ...}。
//	  前端只要固定取 res.data / res.error，不用为“这个接口返回数组还是对象”操心。
//
// 注意参数类型 any（= interface{}）：任何类型都能传进来当 data。
func respond(w http.ResponseWriter, data any, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8") // 告诉浏览器返回 JSON
	if err != nil {                                                   // 出错
		w.WriteHeader(http.StatusInternalServerError)                      // 500：服务端内部错误
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) // 写错误 JSON
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"data": data}) // 写成功 JSON（包一层 data）
}

// respondErr 请求本身有问题（参数缺失/格式错）时返回 400。
func respondErr(w http.ResponseWriter, err error) {
	// w.Header().Set(...)：设置响应头。这里告诉浏览器“返回的是 JSON 文本”。
	// （响应头是 HTTP 报文的一部分，浏览器靠它知道怎么解析响应体。）
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest) // 400：请求参数错误
	// json.NewEncoder(w)：把后面的数据转成 JSON 文本，写到 w（HTTP 响应体）里。
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
