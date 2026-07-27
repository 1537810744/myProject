// 程序入口：装配全部模块，启动自动交易协程与 HTTP 服务。
//
// 整体形态：单进程 + Goroutine（需求文档明确：不用微服务，简单为本）。
//   - 主 goroutine   -> HTTP 服务（前端 RESTful API + 静态页面）
//   - 一个子 goroutine -> 自动交易循环（每 N 秒一轮）
package main

import (
	"log"

	"deltacrypto/internal/config"
	"deltacrypto/internal/database"
	"deltacrypto/internal/exchange"
	"deltacrypto/internal/httpserver"
	"deltacrypto/internal/service/account"
	"deltacrypto/internal/service/alert"
	"deltacrypto/internal/service/apiconfig"
	"deltacrypto/internal/service/autotrade"
	"deltacrypto/internal/service/market"
	"deltacrypto/internal/service/settings"
	"deltacrypto/internal/service/trade"
)

func main() {
	// 1. 配置与数据库
	cfg := config.Load()
	db, err := database.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("数据库初始化失败: %v", err)
	}
	defer db.Close()

	// 2. 基础设施：设置模块（其他模块的参数来源）+ 交易所连接管理器
	settingsSvc := settings.New(db)
	hub := exchange.NewHub(db, "gate", "binance") // 现货腿 gate / 合约腿 binance
	// 启动时尝试用已保存的凭证建连；失败不致命（可能首次运行还没配置）
	if err := hub.Reload(); err != nil {
		log.Printf("[启动] 交易所连接未就绪（请到“API配置”页保存凭证）: %v", err)
	} else {
		log.Println("[启动] 交易所连接就绪：gate(现货) + binance(合约)")
	}

	// 3. 业务模块（依赖关系见 README 模块调用图）
	apiconfigSvc := apiconfig.New(db, hub)
	marketSvc := market.New(hub, settingsSvc)
	tradeSvc := trade.New(db, hub, settingsSvc)
	accountSvc := account.New(hub, settingsSvc, tradeSvc)
	alertSvc := alert.New(db, hub, settingsSvc, accountSvc, tradeSvc)
	autotradeSvc := autotrade.New(settingsSvc, marketSvc, tradeSvc, accountSvc, alertSvc)

	// 4. 自动交易循环（常驻 goroutine，内部根据开关自行休眠）
	go autotradeSvc.RunLoop()

	// 5. HTTP 服务（阻塞主协程）
	server := httpserver.New(cfg, apiconfigSvc, marketSvc, tradeSvc, accountSvc, alertSvc, settingsSvc, autotradeSvc)
	log.Printf("[启动] HTTP 服务监听 http://%s （浏览器打开即可使用）", cfg.ListenAddr)
	if err := server.Run(); err != nil {
		log.Fatalf("HTTP 服务退出: %v", err)
	}
}
