package config

import (
	"testing"
)

// TestLoadDefaults 不设任何环境变量时，应返回默认值。
func TestLoadDefaults(t *testing.T) {
	t.Setenv("LISTEN_ADDR", "")
	t.Setenv("DB_PATH", "")
	t.Setenv("PROXY_URL", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("AUTH_TOKEN", "")
	cfg := Load()
	if cfg.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("ListenAddr 默认值错误: %s", cfg.ListenAddr)
	}
	if cfg.DBPath != "./data/deltacrypto.db" {
		t.Errorf("DBPath 默认值错误: %s", cfg.DBPath)
	}
	if cfg.ProxyURL != "" {
		t.Errorf("ProxyURL 默认应为空: %s", cfg.ProxyURL)
	}
	if cfg.AuthToken != "" {
		t.Errorf("AuthToken 默认应为空: %s", cfg.AuthToken)
	}
}

// TestLoadEnvOverride 设置环境变量时，应覆盖默认值。
func TestLoadEnvOverride(t *testing.T) {
	t.Setenv("LISTEN_ADDR", "0.0.0.0:9999")
	t.Setenv("DB_PATH", "/tmp/x.db")
	t.Setenv("AUTH_TOKEN", "s3cret")
	cfg := Load()
	if cfg.ListenAddr != "0.0.0.0:9999" {
		t.Errorf("ListenAddr 覆盖失败: %s", cfg.ListenAddr)
	}
	if cfg.DBPath != "/tmp/x.db" {
		t.Errorf("DBPath 覆盖失败: %s", cfg.DBPath)
	}
	if cfg.AuthToken != "s3cret" {
		t.Errorf("AuthToken 覆盖失败: %s", cfg.AuthToken)
	}
}

// TestFirstEnv 按优先级取第一个非空环境变量。
func TestFirstEnv(t *testing.T) {
	t.Setenv("A", "")
	t.Setenv("B", "from-b")
	t.Setenv("C", "from-c")
	// 按顺序找：A 空、B 非空 → 返回 B
	if got := firstEnv("A", "B", "C"); got != "from-b" {
		t.Errorf("firstEnv 应取 B: got %q", got)
	}
	// 都不存在 → 空串
	t.Setenv("B", "")
	t.Setenv("C", "")
	if got := firstEnv("A", "B", "C"); got != "" {
		t.Errorf("全空应返回空串: got %q", got)
	}
}

// TestGetEnv 未设置时返回 fallback。
func TestGetEnv(t *testing.T) {
	t.Setenv("SOME_KEY", "")
	if got := getEnv("SOME_KEY", "fb"); got != "fb" {
		t.Errorf("getEnv fallback 失败: got %q", got)
	}
	t.Setenv("SOME_KEY", "v")
	if got := getEnv("SOME_KEY", "fb"); got != "v" {
		t.Errorf("getEnv 取值失败: got %q", got)
	}
}
