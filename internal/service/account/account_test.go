package account

import (
	"testing"
	"time"
)

// TestSafeDiv 安全除法：除 0 返回 0。
func TestSafeDiv(t *testing.T) {
	if got := safeDiv(10, 2); got != 5 {
		t.Errorf("safeDiv(10,2) = %v, want 5", got)
	}
	if got := safeDiv(10, 0); got != 0 {
		t.Errorf("safeDiv(10,0) = %v, want 0（防御除0）", got)
	}
}

// TestAbsFloat 绝对值。
func TestAbsFloat(t *testing.T) {
	if got := absFloat(-3.5); got != 3.5 {
		t.Errorf("absFloat(-3.5) = %v, want 3.5", got)
	}
	if got := absFloat(3.5); got != 3.5 {
		t.Errorf("absFloat(3.5) = %v, want 3.5", got)
	}
}

// TestMinFloat / TestMaxFloat 大小比较。
func TestMinMaxFloat(t *testing.T) {
	if got := minFloat(1, 2); got != 1 {
		t.Errorf("minFloat(1,2) = %v, want 1", got)
	}
	if got := maxFloat(1, 2); got != 2 {
		t.Errorf("maxFloat(1,2) = %v, want 2", got)
	}
}

// TestHumanDuration 人性化时长。
func TestHumanDuration(t *testing.T) {
	day := 24 * time.Hour
	if got := humanDuration(day + 2*time.Hour); got != "1天2小时" {
		t.Errorf("1天2小时: got %q", got)
	}
	if got := humanDuration(3*time.Hour + 25*time.Minute); got != "3小时25分" {
		t.Errorf("3小时25分: got %q", got)
	}
	if got := humanDuration(5 * time.Minute); got != "5分钟" {
		t.Errorf("5分钟: got %q", got)
	}
	if got := humanDuration(-time.Hour); got != "0分钟" { // 负数防御
		t.Errorf("负数应为 0分钟: got %q", got)
	}
}

// TestNextFundingTime 下一个资金费结算点：UTC 的 0/8/16 点，且晚于当前。
func TestNextFundingTime(t *testing.T) {
	// 选一个 UTC 5 点的时刻：下一个结算点应是 8 点。
	now := time.Date(2026, 8, 1, 5, 30, 0, 0, time.UTC)
	next := nextFundingTime(now)
	if !next.After(now) {
		t.Errorf("nextFundingTime 应晚于当前: %v", next)
	}
	// 结算点的小时数（UTC）必须是 0/8/16 之一。
	hour := next.UTC().Hour()
	if hour != 0 && hour != 8 && hour != 16 {
		t.Errorf("结算点小时应为 0/8/16，got %d", hour)
	}
	// 与当前时刻差不超过 8 小时。
	if next.Sub(now) > 8*time.Hour || next.Sub(now) <= 0 {
		t.Errorf("结算点应在 8 小时以内: %v", next.Sub(now))
	}
}
