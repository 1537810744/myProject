package settings

import (
	"path/filepath"
	"testing"

	"deltacrypto/internal/database"
)

// newTestSvc 造一个基于临时数据库的设置服务。
func newTestSvc(t *testing.T) *Service {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db)
}

// TestDefaultsWritten New 后默认参数应已写入数据库。
func TestDefaultsWritten(t *testing.T) {
	s := newTestSvc(t)
	if got := s.Get(KeyGroupSizeUSDT); got != "50" {
		t.Errorf("默认 group_size 应为 50: %q", got)
	}
	if got := s.Get(KeyLeverage); got != "4" {
		t.Errorf("默认 leverage 应为 4: %q", got)
	}
}

// TestSetGet 写入再读取。
func TestSetGet(t *testing.T) {
	s := newTestSvc(t)
	if err := s.Set(KeyGroupSizeUSDT, "100"); err != nil {
		t.Fatal(err)
	}
	if got := s.Get(KeyGroupSizeUSDT); got != "100" {
		t.Errorf("Get 应返回刚设置的值: %q", got)
	}
	// 数值读取
	if got := s.GetFloat(KeyGroupSizeUSDT); got != 100 {
		t.Errorf("GetFloat = %v, want 100", got)
	}
}

// TestSetBatch 批量保存。
func TestSetBatch(t *testing.T) {
	s := newTestSvc(t)
	err := s.SetBatch(map[string]string{KeyGroupSizeUSDT: "60", KeyLeverage: "3"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Get(KeyGroupSizeUSDT) != "60" || s.Get(KeyLeverage) != "3" {
		t.Fatalf("批量保存失败: %s/%s", s.Get(KeyGroupSizeUSDT), s.Get(KeyLeverage))
	}
}

// TestHalt 停机状态：默认未停机，SetHalted(true) 后停机。
func TestHalt(t *testing.T) {
	s := newTestSvc(t)
	if s.IsHalted() {
		t.Fatal("默认不应停机")
	}
	if err := s.SetHalted(true); err != nil {
		t.Fatal(err)
	}
	if !s.IsHalted() {
		t.Fatal("SetHalted(true) 后应停机")
	}
	if err := s.SetHalted(false); err != nil {
		t.Fatal(err)
	}
	if s.IsHalted() {
		t.Fatal("SetHalted(false) 后应恢复")
	}
}

// TestDryRun 模拟盘开关。
func TestDryRun(t *testing.T) {
	s := newTestSvc(t)
	if s.DryRun() {
		t.Fatal("默认不应开模拟盘")
	}
	_ = s.Set(KeyDryRun, "1")
	if !s.DryRun() {
		t.Fatal("dry_run=1 后应开启模拟盘")
	}
}

// TestAll 返回全部参数（含元信息）。
func TestAll(t *testing.T) {
	s := newTestSvc(t)
	all := s.All()
	if len(all) != len(AllParams) {
		t.Fatalf("All() 条数 = %d, want %d", len(all), len(AllParams))
	}
	// 每条都带 4 个字段（value 可能为空——比如 smtp_host 默认就是空）
	for _, m := range all {
		if m["key"] == "" || m["description"] == "" {
			t.Fatalf("参数缺少 key/description: %v", m)
		}
		if _, ok := m["value"]; !ok {
			t.Fatalf("参数缺少 value 字段: %v", m)
		}
	}
}

// TestGetMailConfig webhook 配置读取。
func TestGetMailConfig(t *testing.T) {
	s := newTestSvc(t)
	_ = s.Set(KeyWebhookURL, "https://example.com/hook")
	cfg := s.GetMailConfig()
	if cfg.WebhookURL != "https://example.com/hook" {
		t.Errorf("WebhookURL 读取失败: %q", cfg.WebhookURL)
	}
}
