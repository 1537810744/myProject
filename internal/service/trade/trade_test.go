package trade

import (
	"testing"
	"time"

	"deltacrypto/internal/model"
)

// TestComputeRoundUSDT 拆单金额 + 粉尘处理。
func TestComputeRoundUSDT(t *testing.T) {
	// 正常：min(原子5, 剩余50) = 5
	if got := computeRoundUSDT(5, 50, 5); got != 5 {
		t.Errorf("computeRoundUSDT(5,50,5) = %v, want 5", got)
	}
	// 粉尘带走：原子5、剩余7，执行5后剩2<5 → 本轮全下 7
	if got := computeRoundUSDT(5, 7, 5); got != 7 {
		t.Errorf("computeRoundUSDT(5,7,5) = %v, want 7（粉尘一并带走）", got)
	}
	// 恰好整除：原子5、剩余5，rest=0 → 5
	if got := computeRoundUSDT(5, 5, 5); got != 5 {
		t.Errorf("computeRoundUSDT(5,5,5) = %v, want 5", got)
	}
	// 剩余小于原子：直接全下
	if got := computeRoundUSDT(5, 3, 5); got != 3 {
		t.Errorf("computeRoundUSDT(5,3,5) = %v, want 3", got)
	}
	// 剩余 0：不再下单
	if got := computeRoundUSDT(5, 0, 5); got != 0 {
		t.Errorf("computeRoundUSDT(5,0,5) = %v, want 0", got)
	}
}

// TestMergeLegOutcome 合并多轮成交到腿汇总（数量/加权均价/订单号）。
func TestMergeLegOutcome(t *testing.T) {
	leg := &model.LegResult{Exchange: "gate", MarketType: "spot"}
	out1 := &LegOutcome{
		Amount: 2, CostUSDT: 100, // 均价 50
		Fills: []Fill{{Exchange: "gate", MarketType: "spot", Side: "buy", Price: 50, Amount: 2, CostUSDT: 100, OrderID: "o1"}},
	}
	mergeLegOutcome(leg, out1)
	if leg.Amount != 2 || leg.AvgPrice != 50 || leg.CostUSDT != 100 {
		t.Fatalf("第一轮合并错误: %+v", leg)
	}
	if len(leg.OrderIDs) != 1 || leg.OrderIDs[0] != "o1" {
		t.Fatalf("订单号未收集: %v", leg.OrderIDs)
	}
	out2 := &LegOutcome{
		Amount: 2, CostUSDT: 60, // 均价 30
		Fills: []Fill{{Side: "buy", Price: 30, Amount: 2, CostUSDT: 60, OrderID: "o2"}},
	}
	mergeLegOutcome(leg, out2)
	// 加权均价 = (100+60)/4 = 40
	if leg.Amount != 4 || leg.CostUSDT != 160 || leg.AvgPrice != 40 {
		t.Fatalf("第二轮合并（加权均价）错误: %+v", leg)
	}
	if len(leg.OrderIDs) != 2 {
		t.Fatalf("订单号应累计 2 个: %v", leg.OrderIDs)
	}
	// Side 取最后一笔
	if leg.Side != "buy" {
		t.Fatalf("Side 应为 buy: %s", leg.Side)
	}
}

// TestActionName 建仓/平仓中文名。
func TestActionName(t *testing.T) {
	if got := actionName(true); got != "建仓" {
		t.Errorf("actionName(true) = %q, want 建仓", got)
	}
	if got := actionName(false); got != "平仓" {
		t.Errorf("actionName(false) = %q, want 平仓", got)
	}
}

// TestTradeRequestTime 编译期类型正确性（确保 model 结构字段存在）。
func TestTradeRequestTime(t *testing.T) {
	_ = model.TradeRequest{Symbol: "BTC/USDT", Action: "open", TotalUSDT: 50, AtomUSDT: 5, DustUSDT: 5, RequestID: "req-1"}
	_ = time.Now()
}
