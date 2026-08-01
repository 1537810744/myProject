package database

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenAndMigrate 打开数据库并成功建表（幂等，重复打开不报错）。
func TestOpenAndMigrate(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer db.Close()

	// 重复 Open（模拟重启）应安全：IF NOT EXISTS 保证幂等。
	db2, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("重复打开失败: %v", err)
	}
	defer db2.Close()

	// 确认关键表存在（查 sqlite_master）。
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('settings','hedge_position','trade_log','request_log')`).Scan(&n); err != nil {
		t.Fatalf("查询表失败: %v", err)
	}
	if n != 4 {
		t.Fatalf("应有 4 张关键表，实际 %d", n)
	}
}

// TestExecQueryRoundtrip 写入再读出（验证 database/sql 基本通路）。
func TestExecQueryRoundtrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO settings(key, value) VALUES(?,?)`, "k", "v"); err != nil {
		t.Fatalf("插入失败: %v", err)
	}
	var v string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, "k").Scan(&v); err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if v != "v" {
		t.Fatalf("取值错误: %q", v)
	}
}

// TestRequestLogUnique request_log 的 request_id 唯一性：重复插入第二次被拒。
func TestRequestLogUnique(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO request_log(request_id, action, symbol) VALUES(?,?,?)`, "rid-1", "open", "BTC/USDT"); err != nil {
		t.Fatalf("第一次插入失败: %v", err)
	}
	// 第二次同 id：用 INSERT OR IGNORE 会被忽略（AffectedRows==0）
	res, err := db.Exec(`INSERT OR IGNORE INTO request_log(request_id, action, symbol) VALUES(?,?,?)`, "rid-1", "open", "BTC/USDT")
	if err != nil {
		t.Fatalf("重复插入报错: %v", err)
	}
	n, _ := res.RowsAffected()
	if n != 0 {
		t.Fatalf("重复 request_id 应被忽略（Affected=0），实际 %d", n)
	}
}

// TestBackup 备份生成一个可重新打开的数据库文件。
func TestBackup(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	db, err := Open(src)
	if err != nil {
		t.Fatal(err)
	}
	// 写点数据再备份
	if _, err := db.Exec(`INSERT INTO settings(key, value) VALUES(?,?)`, "backup-key", "1"); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "backup.db")
	if err := db.Backup(dst); err != nil {
		t.Fatalf("Backup 失败: %v", err)
	}
	db.Close()

	// 备份文件能独立打开且数据在
	restore, err := Open(dst)
	if err != nil {
		t.Fatalf("备份文件打不开: %v", err)
	}
	defer restore.Close()
	var v string
	if err := restore.QueryRow(`SELECT value FROM settings WHERE key='backup-key'`).Scan(&v); err != nil || v != "1" {
		t.Fatalf("备份中的数据丢失: v=%q err=%v", v, err)
	}
	// 清理备份文件
	os.Remove(dst)
}
