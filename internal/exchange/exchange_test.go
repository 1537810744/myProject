package exchange

import "testing"

// TestBaseToSwap 内部币对 → 合约符号。
func TestBaseToSwap(t *testing.T) {
	cases := map[string]string{
		"BTC/USDT":      "BTC/USDT:USDT",
		"BTC/USDT:USDT": "BTC/USDT:USDT", // 已经是合约符号：幂等，原样返回
		"ETH/USDT":      "ETH/USDT:USDT",
	}
	for in, want := range cases {
		if got := BaseToSwap(in); got != want {
			t.Errorf("BaseToSwap(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSwapToBase 合约符号 → 内部币对。
func TestSwapToBase(t *testing.T) {
	cases := map[string]string{
		"BTC/USDT:USDT": "BTC/USDT",
		"BTC/USDT":      "BTC/USDT", // 没有冒号：原样返回
		"ETH/USDT:USDT": "ETH/USDT",
	}
	for in, want := range cases {
		if got := SwapToBase(in); got != want {
			t.Errorf("SwapToBase(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestF64 指针安全解引用：nil → 0，非 nil → 真值。
func TestF64(t *testing.T) {
	if got := f64(nil); got != 0 {
		t.Errorf("f64(nil) = %v, want 0", got)
	}
	v := 3.14
	if got := f64(&v); got != 3.14 {
		t.Errorf("f64(&3.14) = %v, want 3.14", got)
	}
}

// TestStr 指针安全解引用：nil → ""，非 nil → 真值。
func TestStr(t *testing.T) {
	if got := str(nil); got != "" {
		t.Errorf("str(nil) = %q, want empty", got)
	}
	v := "abc"
	if got := str(&v); got != "abc" {
		t.Errorf("str = %q, want abc", got)
	}
}

// TestBoolv 指针安全解引用：nil → false，非 nil → 真值。
func TestBoolv(t *testing.T) {
	if got := boolv(nil); got != false {
		t.Errorf("boolv(nil) = %v, want false", got)
	}
	v := true
	if got := boolv(&v); got != true {
		t.Errorf("boolv = %v, want true", got)
	}
}

// TestI64 指针安全解引用：nil → 0，非 nil → 真值。
func TestI64(t *testing.T) {
	if got := i64(nil); got != 0 {
		t.Errorf("i64(nil) = %v, want 0", got)
	}
	v := int64(99)
	if got := i64(&v); got != 99 {
		t.Errorf("i64 = %v, want 99", got)
	}
}
