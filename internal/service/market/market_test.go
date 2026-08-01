package market

import "testing"

// TestIsRising 费率趋势判断：前一半均值 < 后一半均值 = 上升。
func TestIsRising(t *testing.T) {
	rising := []float64{0.01, 0.02, 0.03, 0.04, 0.05, 0.06} // 前 3 均值 < 后 3 均值
	if !isRising(rising) {
		t.Error("上升序列应判定为 rising")
	}
	falling := []float64{0.06, 0.05, 0.04, 0.03, 0.02, 0.01}
	if isRising(falling) {
		t.Error("下降序列不应判定为 rising")
	}
	// 数据太少无法判断：放行（返回 true）
	if !isRising([]float64{0.01}) {
		t.Error("单元素序列应放行（true）")
	}
	if !isRising([]float64{}) {
		t.Error("空序列应放行（true）")
	}
}

// TestAverage 均值计算。
func TestAverage(t *testing.T) {
	if got := average([]float64{1, 2, 3, 4}); got != 2.5 {
		t.Errorf("average([1,2,3,4]) = %v, want 2.5", got)
	}
	if got := average([]float64{}); got != 0 {
		t.Errorf("average([]) = %v, want 0（防御：空切片返回 0）", got)
	}
}

// TestZipBase 把两个平行列表 zip 成 map。
func TestZipBase(t *testing.T) {
	bases := []string{"BTC/USDT", "ETH/USDT"}
	ccxts := []string{"BTC/USDT:USDT", "ETH/USDT:USDT"}
	m := zipBase(bases, ccxts)
	if m["BTC/USDT"] != "BTC/USDT:USDT" {
		t.Errorf("zipBase 结果错误: %v", m)
	}
	if len(m) != 2 {
		t.Errorf("zipBase 应有 2 项: %v", m)
	}
}
