// Package database 负责 SQLite 的连接、建表与通用读写。
// 选型说明：个人单进程工具，SQLite 嵌入式数据库零依赖、免运维，
// 使用 modernc.org/sqlite（纯 Go 实现），编译时不需要 CGO，方便 Docker 静态打包。
package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // 纯 Go 版 SQLite 驱动，驱动名为 "sqlite"
)

// DB 是 database/sql 的薄封装，方便各模块注入使用
type DB struct {
	*sql.DB
}

// Open 打开（不存在则创建）数据库，并自动建表
func Open(path string) (*DB, error) {
	// 确保数据文件所在目录存在
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据目录失败: %w", err)
		}
	}
	// _pragma：开启 WAL 提升并发读体验；busy_timeout 避免单文件锁冲突报错
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	// SQLite 单写者特性：限制连接数为 1，避免 SQLITE_BUSY
	sqlDB.SetMaxOpenConns(1)

	db := &DB{sqlDB}
	if err := db.migrate(); err != nil {
		return nil, err
	}
	return db, nil
}

// migrate 建表（IF NOT EXISTS，重复启动安全）
func (db *DB) migrate() error {
	stmts := []string{
		// 模块 1：交易所 API 凭证表
		`CREATE TABLE IF NOT EXISTS exchange_api (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			exchange TEXT NOT NULL,              -- binance / gate
			label TEXT DEFAULT '',
			api_key TEXT NOT NULL,
			api_secret TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		// 模块 7：设置表（键值对，所有可调参数都在这里）
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		// 对冲持仓表：建仓写入，平仓置 closed
		`CREATE TABLE IF NOT EXISTS hedge_position (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			symbol TEXT NOT NULL,
			spot_exchange TEXT NOT NULL,
			swap_exchange TEXT NOT NULL,
			spot_amount REAL NOT NULL,           -- 现货腿币数量
			swap_amount REAL NOT NULL,           -- 合约腿币数量
			spot_entry_price REAL NOT NULL,
			swap_entry_price REAL NOT NULL,
			entry_basis_pct REAL NOT NULL,       -- 入场基差（%）
			status TEXT NOT NULL DEFAULT 'open', -- open / closed
			opened_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			closed_at DATETIME
		)`,
		// 操作日志表（交易模块、自动交易模块写入）
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
			mail_sent INTEGER DEFAULT 0
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("建表失败: %w", err)
		}
	}
	return nil
}
