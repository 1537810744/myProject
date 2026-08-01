// 【阅读顺序 01】程序入口。
// 本文件：创建全部模块并“接线”（依赖注入根），启动 HTTP + 自动交易两个循环。
// 本次升级新增：启动对账、定时数据库备份、优雅停机（Ctrl+C 也能安全退出）。
// 语法点预览：package、import、func、:=、if err != nil、defer、go 关键字、
// signal.Notify（信号监听）、goroutine 后台任务。
package main

// import 导入要用到的包。路径以 go.mod 里的模块名 deltacrypto 为根。
import (
	"fmt"           // 格式化（备份文件名）
	"log"           // Go 标准库日志：打印启动信息、错误
	"net/http"      // HTTP（判断 ErrServerClosed）
	"os"            // 信号/退出
	"os/signal"     // 监听系统信号（Ctrl+C / SIGTERM）
	"path/filepath" // 路径操作：取 DB 目录生成密钥文件
	"sort"          // 排序（备份文件按时间清理）
	"strings"       // 字符串（备份文件名）
	"syscall"       // 系统信号常量
	"time"          // 时间

	"deltacrypto/internal/config"            // 环境配置
	"deltacrypto/internal/crypto"            // 凭证加密
	"deltacrypto/internal/database"          // SQLite 数据库
	"deltacrypto/internal/exchange"          // 交易所连接抽象层
	"deltacrypto/internal/httpserver"        // HTTP 服务层
	"deltacrypto/internal/notify"            // 通知（邮件+webhook）
	"deltacrypto/internal/service/account"   // 模块④账户
	"deltacrypto/internal/service/alert"     // 模块⑤预警
	"deltacrypto/internal/service/apiconfig" // 模块①API配置
	"deltacrypto/internal/service/autotrade" // 模块⑥自动交易
	"deltacrypto/internal/service/market"    // 模块②行情
	"deltacrypto/internal/service/settings"  // 模块⑦设置
	"deltacrypto/internal/service/trade"     // 模块③交易
)

// main 是程序入口函数：程序从这里开始执行，也在这里结束（main 返回=进程退出）。
// 整个文件只做【装配】——模块自己不搭依赖（数据库/交易所连接），由 main 建好后
// 传给它，这叫【依赖注入】。好处：模块间零耦合，依赖关系在 main 里一眼看全。
func main() {

	// ---------- 第 1 步：最底层基础资源（谁都不依赖它们，它们被所有人依赖） ----------

	// “:=” 短变量声明：声明 cfg 并初始化，类型由右边返回值推断（*config.Config）。
	// cfg 变量在整个 main 函数里可用。
	cfg := config.Load()

	// database.Open() 返回两个值：(数据库对象, error)。
	// Go 惯例：可能失败的函数都返回 error 作为最后一个返回值，调用方必须检查。
	db, err := database.Open(cfg.DBPath)
	// “if err != nil” 是 Go 错误处理的固定套路：err 非空 = 出错了。
	if err != nil {
		// log.Fatalf：打印错误并【终止程序】（退出码非 0）。
		// 为什么这里直接退出？—— 数据库打不开，一切功能都没法用，没有降级空间。
		log.Fatalf("数据库初始化失败: %v", err) // %v 是“按默认方式打印值”
	}
	// defer 注册“main 返回前【一定】执行的清理动作”：关闭数据库。
	// 为什么用 defer 而不是最后一行手动 Close？—— 无论函数从哪一行 return
	// （包括中途出错提前返回），defer 都保证执行。这是 Go 管理资源的惯用法。
	defer db.Close()

	// ---------- 第 2 步：全局基础服务（各模块的公共依赖） ----------

	// settings.New 创建“参数中心”：所有可调参数存在数据库里，别的模块都从这读。
	// 注意传进去的是 db（依赖注入：模块需要数据库，就由外部传给它的构造函数）。
	settingsSvc := settings.New(db)

	// 大陆直连交易所会被墙，配 HTTP 代理。
	if cfg.ProxyURL != "" { // “if 条件 { }” 条件为真才执行花括号里的内容
		// exchange.SetProxy 是包级函数，写入 exchange 包内全局变量，所有连接共享。
		exchange.SetProxy(cfg.ProxyURL)
		log.Printf("[启动] 交易所请求走代理: %s", cfg.ProxyURL) // Printf=格式化打印
	}

	// exchange.NewHub 创建交易所连接管理器：现货腿(gate) + 合约腿(binance)。
	// keyring 用于解密库里加密存储的 API Secret（安全升级）。
	keyring, err := crypto.NewKeyring(filepath.Dir(cfg.DBPath))
	if err != nil {
		log.Fatalf("初始化密钥失败: %v", err) // 密钥都起不来就别跑了
	}
	hub := exchange.NewHub(db, "gate", "binance", keyring)

	// hub.Reload()：从数据库读已保存的凭证，建立两条交易连接。
	// “if err := ...; err != nil” 是 if 带初始化语句：先执行 Reload 赋给 err，再判断。
	// 失败只打日志不退出——首次运行还没填过 Key 是正常情况，填好后前端会自动
	// Reload 热更新连接，无需重启。所以这里“提醒”就够了。
	if err := hub.Reload(); err != nil {
		log.Printf("[启动] 交易所连接未就绪（请到“API配置”页保存凭证）: %v", err)
	} else { // if 不成立时走这里
		log.Println("[启动] 交易所连接就绪：gate(现货) + binance(合约)")
	}

	// ---------- 第 3 步：7 个业务模块（依赖注入的“接线处”） ----------
	// 每个模块只拿它需要的依赖：要读参数拿 settings，要写库拿 db，要下单拿 hub。
	// 拿得越少、耦合越低——这是依赖注入的核心价值。

	apiconfigSvc := apiconfig.New(db, hub, keyring)                                       // ① 存 API 凭证 + 测连接（Secret 加密落库）
	marketSvc := market.New(hub, settingsSvc)                                             // ② 行情筛选（只需连接+参数）
	tradeSvc := trade.New(db, hub, settingsSvc)                                           // ③ 下单（要写持仓/日志，所以拿 db）
	accountSvc := account.New(db, hub, settingsSvc, tradeSvc)                             // ④ 账户（要读 trade 落库的持仓）
	alertSvc := alert.New(db, hub, settingsSvc, accountSvc, tradeSvc)                     // ⑤ 预警
	autotradeSvc := autotrade.New(settingsSvc, marketSvc, tradeSvc, accountSvc, alertSvc) // ⑥ 自动交易

	// ---------- 第 4 步：启动时对账（风险控制） ----------
	// 目的：程序重启后，把数据库里记的持仓和交易所【实际】持仓比对一遍，
	// 发现不一致立即告警——否则程序会基于错误的前提继续交易。
	// 只在交易连接就绪时做（没配 API 就没法对账，跳过即可）。
	if hub.Ready() {
		if report, err := accountSvc.Reconcile(); err != nil {
			log.Printf("[启动] 对账失败（不影响启动，可稍后手动触发）: %v", err)
		} else if !report.IsConsistent {
			log.Printf("[启动] ⚠️ 对账发现 %d/%d 个持仓不一致，请人工核查！", report.Mismatches, report.Total)
			_ = notify.Notify(settingsSvc.GetMailConfig(), "启动对账发现不一致",
				fmt.Sprintf("程序启动对账发现 %d/%d 个持仓与交易所不一致，请尽快核查。", report.Mismatches, report.Total))
		} else {
			log.Printf("[启动] 对账通过：%d 个持仓与交易所一致", report.Total)
		}
	}

	// ---------- 第 5 步：启动自动交易循环（独立 goroutine） ----------

	// “go 函数名()” = 在一个【新的 goroutine】（Go 的轻量线程）里异步执行。
	// 这一行立即返回、继续往下，RunLoop 在后台并发跑。
	// 为什么自动交易必须另开 goroutine？—— 下面的 HTTP 服务会【阻塞】当前
	// 线程直到退出；如果自动交易也在主线跑，就永远轮不到它。所以：
	// HTTP 占住主 goroutine，自动交易在子 goroutine 陪跑。
	go autotradeSvc.RunLoop()

	// ---------- 第 6 步：启动定时数据库备份（独立 goroutine） ----------
	// 设置 backup_interval_hours>0 时，每隔 N 小时把数据库做一份一致性快照到 backups/ 目录，
	// 只保留最近 5 份。数据坏了/误删时能找回（运维兜底）。
	go backupLoop(db, cfg.DBPath, settingsSvc)

	// ---------- 第 7 步：启动 HTTP 服务（阻塞主 goroutine，程序的生命线） ----------

	// httpserver.New 把全部模块塞进 Server，注册所有 REST API 路由。
	server := httpserver.New(cfg, apiconfigSvc, marketSvc, tradeSvc, accountSvc, alertSvc, settingsSvc, autotradeSvc)
	log.Printf("[启动] HTTP 服务监听 http://%s （浏览器打开即可使用）", cfg.ListenAddr)

	// 用一个通道让 HTTP 服务在【独立 goroutine】里跑，主 goroutine 等待两类信号：
	//   a) HTTP 服务出错退出（errCh）；
	//   b) 收到 Ctrl+C / SIGTERM（quitCh）——触发优雅停机。
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Run() // Run() 阻塞，出错才返回
	}()

	// signal.Notify：注册"想捕获的系统信号"。Ctrl+C(SIGINT) 和 kill(SIGTERM) 都会
	// 送到 quitCh，主 goroutine 收到后开始优雅退出，而不是被系统直接杀死。
	quitCh := make(chan os.Signal, 1)
	signal.Notify(quitCh, os.Interrupt, syscall.SIGTERM)

	// 阻塞等待：哪个通道先来信号就走哪个分支。
	select {
	case err := <-errCh: // HTTP 服务自己挂了
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	case sig := <-quitCh: // 收到退出信号 → 优雅停机
		log.Printf("[退出] 收到信号 %v，正在优雅停机...", sig)
		// 1. 停止接收新请求，等已处理的请求完成（最多 5 秒）。
		if err := server.Shutdown(5 * time.Second); err != nil {
			log.Printf("[退出] HTTP 关闭: %v", err)
		}
		// 2. 退出前再备份一次数据库（抓住最新状态）。
		if err := db.Backup(backupPath(cfg.DBPath)); err != nil {
			log.Printf("[退出] 退出前备份失败: %v", err)
		}
		log.Println("[退出] 已优雅退出")
	}
}

// backupLoop 定时备份：每 backup_interval_hours 小时备份一次，只保留最近 5 份。
func backupLoop(db *database.DB, dbPath string, settingsSvc *settings.Service) {
	for { // 无限循环（在独立 goroutine 里跑）
		hours := settingsSvc.GetInt(settings.KeyBackupIntervalHours) // 间隔每轮现读
		if hours <= 0 {                                              // 0 = 不自动备份：睡 1 小时再问一次（可能用户后来改了设置）
			time.Sleep(1 * time.Hour)
			continue
		}
		time.Sleep(time.Duration(hours) * time.Hour) // 睡到下一个备份点
		if err := db.Backup(backupPath(dbPath)); err != nil {
			log.Printf("[备份] 失败: %v", err)
			continue
		}
		pruneBackups(filepath.Dir(dbPath), 5) // 只留最近 5 份
	}
}

// backupPath 生成备份文件路径：<数据目录>/backups/deltacrypto-<时间戳>.db。
func backupPath(dbPath string) string {
	dir := filepath.Dir(dbPath)
	base := filepath.Base(dbPath) // "deltacrypto.db"
	ext := filepath.Ext(base)     // ".db"
	name := strings.TrimSuffix(base, ext)
	backupDir := filepath.Join(dir, "backups")
	ts := time.Now().Format("20060102-150405")
	return filepath.Join(backupDir, fmt.Sprintf("%s-%s%s", name, ts, ext))
}

// pruneBackups 只保留最近 keep 份备份，删除更旧的（防止备份无限堆积占满磁盘）。
func pruneBackups(dir string, keep int) {
	backupDir := filepath.Join(dir, "backups")
	// filepath.Glob 找所有 .db 备份文件。
	files, err := filepath.Glob(filepath.Join(backupDir, "deltacrypto-*.db"))
	if err != nil {
		log.Printf("[备份] 清理失败: %v", err)
		return
	}
	if len(files) <= keep {
		return // 没超过保留数，不用删
	}
	// 按文件名排序（时间戳格式保证了字典序 = 时间序），删最旧的。
	sort.Strings(files)
	for _, f := range files[:len(files)-keep] {
		if err := os.Remove(f); err != nil {
			log.Printf("[备份] 删除旧备份失败: %v", err)
		}
	}
}
