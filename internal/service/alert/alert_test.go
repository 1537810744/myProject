package alert

import (
	"strings"
	"testing"

	"deltacrypto/internal/model"
)

// TestBoolToInt bool → 0/1。
func TestBoolToInt(t *testing.T) {
	if got := boolToInt(true); got != 1 {
		t.Errorf("boolToInt(true) = %d, want 1", got)
	}
	if got := boolToInt(false); got != 0 {
		t.Errorf("boolToInt(false) = %d, want 0", got)
	}
}

// TestSummary 预警摘要：有预警拼多行，无预警给默认文案。
func TestSummary(t *testing.T) {
	if got := Summary(nil); got != "本轮无预警" {
		t.Errorf("空摘要错误: %q", got)
	}
	fired := []model.AlertRecord{
		{Message: "预警A"},
		{Message: "预警B"},
	}
	s := Summary(fired)
	if !strings.Contains(s, "预警A") || !strings.Contains(s, "预警B") {
		t.Errorf("摘要应包含两条预警: %q", s)
	}
	if !strings.HasPrefix(s, "- ") {
		t.Errorf("摘要每行应以 - 开头: %q", s)
	}
}
