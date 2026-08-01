// 【阅读顺序 04】SQLite 连接与建表。
// 本文件只负责“打开数据库 + 建表”，业务读写分散在各模块里。
// 选型：个人工具用 SQLite（嵌入式、零运维、备份=拷文件）；驱动用 modernc.org/sqlite
// （纯 Go 实现，不需要 CGO，Docker 能静态打包）。
// 语法点预览：空导入、struct 嵌入、0o 八进制、DSN 字符串、%w 错误包装、切片循环。
package database

// import 导入用到的包。
import (
	"database/sql"  // Go 标准库：通用数据库接口（Open/Query/Exec/QueryRow）
	"fmt"           // 格式化：fmt.Errorf（构造带上下文错误）
	"os"            // 创建目录：os.MkdirAll
	"path/filepath" // 路径操作：filepath.Dir
	"strings"       // 字符串替换（VACUUM INTO 的路径转义用）

	_ "modernc.org/sqlite" // 空导入：只触发 init() 把驱动注册进 database/sql，代码里不直接用
	// 为什么必须导入？—— 数据库驱动靠【init() 函数】在 import 时自动把自己注册
	// 进 database/sql（注册名 "sqlite"）。不 import 就不会注册，sql.Open 就找不到驱动。
	// “_” 占住名字：告诉 Go“我就是要导入它，但我不用它的标识符，别报‘导入了没用’”。
)

// DB 是 database/sql 的薄封装。
// “struct 嵌入”：把 *sql.DB 作为匿名字段嵌进来，DB 就【自动获得】*sql.DB 的全部方法
// （Query/Exec/QueryRow...），可以直接 db.Exec(...) 调用。
// 为什么还要包一层？—— 有了自己的身份，将来想给所有 SQL 加日志/埋点，改这一个类型即可。
type DB struct {
	*sql.DB
}

// Open 打开（不存在则创建）数据库文件，并自动建表。
// 函数签名：Open(path string) (*DB, error)——入参 path，返回 (数据库对象, 错误)。
func Open(path string) (*DB, error) {
	// filepath.Dir(path)：取路径的目录部分。比如 "./data/deltacrypto.db" → "./data"。
	if dir := filepath.Dir(path); dir != "." {
		// os.MkdirAll 递归创建目录（不存在则建）；0o755 是权限：用户可读写执行，其他人只读执行。
		// 0o 前缀是 Go 1.13+ 的八进制写法。为什么 Open 要负责建目录？——
		// 用户可能直接写 ./data/xxx.db，但 data/ 第一次跑不存在，SQLite 不会自动建目录。
		if err := os.MkdirAll(dir, 0o755); err != nil {
			// fmt.Errorf + %w：构造带上下文的错误。“%w”把底层错误包进来（错误链），
			// 外层 errors.Is/As 还能剥开找回最初的错误。
			return nil, fmt.Errorf("创建数据目录失败: %w", err)
		}
	}
	// DSN（数据源名称）是“连什么库 + 什么参数”的字符串。
	// 里面的 _pragma= 是 modernc 驱动提供的“连上后自动执行 PRAGMA”的捷径：
	//   journal_mode(WAL)  → 用预写日志模式。SQLite 默认写锁整库，WAL 允许读写并发、
	//                       本项目读多写少，收益明显。
	//   busy_timeout(5000) → 另一连接占着文件锁时最多等 5 秒，而不是立刻报 locked。
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	// sql.Open("sqlite", dsn)：建立数据库连接池（懒加载，真正连上是第一次执行 SQL 时）。
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	// 限制连接池为 1 个连接。为什么？—— SQLite 同一时刻只允许【一个】写者，
	// 多连接并发写会频繁撞锁（SQLITE_BUSY）。本项目单进程低频工具，根本不需要并发写，
	// 把池子压到 1 个就从根上消灭锁冲突。
	// ⚠️ 注意：如果换 MySQL 这种天然支持并发的库，绝不能这样设置。
	sqlDB.SetMaxOpenConns(1)

	// “&DB{sqlDB}” 复合字面量：创建 DB 对象，嵌入的 *sql.DB 字段填上 sqlDB。
	db := &DB{sqlDB}
	if err := db.migrate(); err != nil { // 建表（migrate 方法见下）
		return nil, err
	}
	return db, nil // 成功：返回 (DB, nil)——nil 表示没有错误
}

// migrate 建表。
// 为什么是“启动时跑一遍建表”？—— 程序要“拿到代码就能跑”，第一次启动自动建表。
// IF NOT EXISTS 让每次启动重复执行都安全（幂等）——这就是最简版的【数据库迁移】。
func (db *DB) migrate() error {
	// 把所有建表语句放进一个【切片】再循环执行——【数据驱动】：
	// 加一张新表只需往切片里加一行字符串，不用复制整段“Exec + if err”样板。
	stmts := []string{ // []string{...} 字符串切片字面量：花括号里列出元素
		// 模块 1：交易所 API 凭证表
		`CREATE TABLE IF NOT EXISTS exchange_api (
			id INTEGER PRIMARY KEY AUTOINCREMENT, -- 自增主键
			exchange TEXT NOT NULL,               -- binance / gate
			label TEXT DEFAULT '',
			api_key TEXT NOT NULL,
			api_secret TEXT NOT NULL,             -- ⚠️ 明文存储：本机个人工具、监听 localhost，
			                                     --    明文换实现简单；部署到公网必须加密（风险已在 README 注明）
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`, // 注意：SQL 用反引号 ` 括起来（Go 里叫 raw string，不转义、可跨行）
		// 模块 7：设置表（key-value，所有可调参数都在这里）
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,  -- key 是主键：参数名唯一
			value TEXT NOT NULL
		)`,
		// 对冲持仓表：建仓时 INSERT，平仓时把 status 置 closed
		`CREATE TABLE IF NOT EXISTS hedge_position (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			symbol TEXT NOT NULL,
			spot_exchange TEXT NOT NULL,
			swap_exchange TEXT NOT NULL,
			spot_amount REAL NOT NULL,  -- REAL = 浮点数（Go 里对应 float64）
			swap_amount REAL NOT NULL,
			spot_entry_price REAL NOT NULL,
			swap_entry_price REAL NOT NULL,
			entry_basis_pct REAL NOT NULL,       -- 入场基差（%）——slow sell 判断用
			status TEXT NOT NULL DEFAULT 'open', -- open / closed
			opened_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			closed_at DATETIME
		)`,
		// 操作日志表（交易/自动交易/预警模块写入）
		`CREATE TABLE IF NOT EXISTS trade_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			time DATETIME DEFAULT CURRENT_TIMESTAMP,
			module TEXT NOT NULL,
			level TEXT NOT NULL DEFAULT 'info',
			action TEXT DEFAULT '',
			symbol TEXT DEFAULT '',
			message TEXT DEFAULT ''
		)`,
		// 预警记录表（预警模块写入）
		`CREATE TABLE IF NOT EXISTS alert_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			time DATETIME DEFAULT CURRENT_TIMESTAMP,
			type TEXT NOT NULL,                  -- funding_negative / adl / liquidation / balance
			symbol TEXT DEFAULT '',
			level TEXT NOT NULL DEFAULT 'warning',
			message TEXT DEFAULT '',
			mail_sent INTEGER DEFAULT 0          -- SQLite 没有 bool 类型，用 0/1 整数
		)`,
		// 成交记录表（交易引擎写入；详情页“成交记录”页签）
		`CREATE TABLE IF NOT EXISTS trade_fill (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			position_id INTEGER DEFAULT 0,       -- 关联的对冲持仓 id（0=未关联）
			symbol TEXT NOT NULL,
			exchange TEXT NOT NULL,              -- binance / gate
			market_type TEXT NOT NULL,           -- spot / swap
			side TEXT NOT NULL,                  -- buy / sell
			price REAL NOT NULL,
			amount REAL NOT NULL,
			cost_usdt REAL NOT NULL,
			fee REAL DEFAULT 0,
			fee_currency TEXT DEFAULT '',
			order_id TEXT DEFAULT '',
			maker INTEGER DEFAULT 0,             -- 1=Maker 成交 / 0=Taker 成交
			traded_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		// 资金费流水表（从交易所同步；详情页“资金费率流水”页签）
		// UNIQUE(exchange, symbol, income_at)：唯一约束，同步去重的“物理保证”——
		// 同一笔结算重复同步会被数据库挡下（见 detail.go 的 INSERT OR IGNORE）。
		`CREATE TABLE IF NOT EXISTS funding_payment (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			exchange TEXT NOT NULL,
			symbol TEXT NOT NULL,
			amount REAL NOT NULL,                -- 收入为正 / 支出为负（USDT）
			income_id TEXT DEFAULT '',           -- 交易所侧流水号
			income_at DATETIME NOT NULL,
			UNIQUE(exchange, symbol, income_at)
		)`,
		// 请求幂等表（防重复下单）：request_id 是主键 = 全局唯一，
		// 同一 request_id 只能插入一次；交易模块用 INSERT OR IGNORE 判断是否已处理过。
		`CREATE TABLE IF NOT EXISTS request_log (
			request_id TEXT PRIMARY KEY,
			action TEXT NOT NULL,      -- open / close
			symbol TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		// 收益快照表（自动交易每轮记录；详情页“收益曲线”页签）
		`CREATE TABLE IF NOT EXISTS profit_snapshot (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			symbol TEXT NOT NULL,
			net_profit REAL DEFAULT 0,           -- 净收益
			basis_pnl REAL DEFAULT 0,            -- 期现收益
			funding_cum REAL DEFAULT 0,          -- 费率累计
			fee_cum REAL DEFAULT 0,              -- 手续费累计
			ts DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
	}
	// for + range 遍历切片：每次迭代 s 是当前一条建表语句。
	for _, s := range stmts {
		// db.Exec 执行一条 SQL（建表语句没有返回值，用 Exec；查数据用 Query）。
		if _, err := db.Exec(s); err != nil {
			// “_” 丢弃 Exec 的返回值（无意义），只看 err。
			// %w 包装错误：附加上下文（哪一步建表失败），保留底层错误链。
			return fmt.Errorf("建表失败: %w", err)
		}
	}
	return nil // 全部建表成功，返回 nil（无错误）
}

// Backup 把数据库做一份一致性快照到 dstPath。
// 原理：SQLite 的 `VACUUM INTO '文件'` 会在【不打断当前连接】的前提下，把整个库
// （含 WAL 里还没落盘的数据）压缩成一个新的数据库文件——比直接拷贝 .db 文件更可靠
// （直接拷可能漏掉 WAL 里还没合并的数据）。
// ⚠️ 注意：VACUUM INTO 的目标路径不支持绑定参数（`?`），只能拼进 SQL 字符串里；
// 这里是配置/代码内部传入的受信任路径，用单引号转义后拼接是安全的（不是用户输入）。
func (db *DB) Backup(dstPath string) error {
	escaped := strings.ReplaceAll(dstPath, "'", "''") // 路径里的单引号翻倍转义
	_, err := db.Exec("VACUUM INTO '" + escaped + "'")
	if err != nil {
		return fmt.Errorf("数据库备份失败: %w", err)
	}
	return nil
}
