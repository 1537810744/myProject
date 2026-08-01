package autotrade

import "testing"

// TestBreakerTrip 连续失败达到阈值触发跳闸。
func TestBreakerTrip(t *testing.T) {
	b := NewBreaker(3)
	// 前两次失败不跳闸
	if b.RecordFailure() {
		t.Fatal("第 1 次失败不应跳闸")
	}
	if b.RecordFailure() {
		t.Fatal("第 2 次失败不应跳闸")
	}
	// 第 3 次失败达到阈值 → 跳闸
	if !b.RecordFailure() {
		t.Fatal("第 3 次失败应触发跳闸")
	}
	if !b.IsTripped() {
		t.Fatal("跳闸后 IsTripped 应为 true")
	}
	// 跳闸后再失败不应重复触发
	if b.RecordFailure() {
		t.Fatal("已跳闸后不应重复返回刚触发")
	}
}

// TestBreakerSuccessReset 成功会清零失败计数。
func TestBreakerSuccessReset(t *testing.T) {
	b := NewBreaker(3)
	b.RecordFailure()
	b.RecordFailure()
	b.RecordSuccess() // 成功：清零
	if b.RecordFailure() {
		t.Fatal("清零后再失败一次不应跳闸")
	}
}

// TestBreakerReset 手动复位：清除跳闸状态，之后再失败能重新触发。
func TestBreakerReset(t *testing.T) {
	b := NewBreaker(1) // 阈值 1：一次失败就跳闸
	b.RecordFailure()
	if !b.IsTripped() {
		t.Fatal("应已跳闸")
	}
	b.Reset()
	if b.IsTripped() {
		t.Fatal("Reset 后不应再跳闸")
	}
	// 阈值仍是 1：Reset 后再失败一次，应该再次触发跳闸。
	if !b.RecordFailure() {
		t.Fatal("Reset 后再失败应重新触发")
	}
}

// TestBreakerSetMaxFail 动态调阈值。
func TestBreakerSetMaxFail(t *testing.T) {
	b := NewBreaker(10)
	b.SetMaxFail(2)
	if b.RecordFailure() { // 第 1 次失败：1 < 2，不应触发
		t.Fatal("第 1 次失败(未到阈值2)不应触发")
	}
	if !b.RecordFailure() { // 第 2 次失败：2 >= 2，应触发
		t.Fatal("第 2 次失败(达到阈值2)应触发")
	}
	// 跳闸后是【闩锁】状态：必须 Reset 才能重新计数（防反复横跳）。
	b.Reset()
	// 非法值兜底：0 → 兜底 1，Reset 后一次失败即触发。
	b.SetMaxFail(0)
	if !b.RecordFailure() {
		t.Fatal("阈值兜底为 1 后一次失败应触发")
	}
}
