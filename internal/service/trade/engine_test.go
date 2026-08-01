package trade

import (
	"errors"
	"strings"
	"testing"
	"time"

	"deltacrypto/internal/exchange"
)

// ---------- 测试替身（fake）：实现 trade.Exchange 接口，不碰真实交易所 ----------

// fakeEx 一个假的交易所连接：只支持 Taker 市价成交（引擎测试用 Taker 模式）。
type fakeEx struct {
	id       string
	filled   float64 // 每次市价单成交多少
	avgPrice float64 // 成交均价
	err      error   // 模拟下单失败
}

func (f *fakeEx) ID() string { return f.id }
func (f *fakeEx) AmountToPrecision(marketType, baseSymbol string, amount float64) float64 {
	return amount
}
func (f *fakeEx) CreateMarketOrder(marketType, baseSymbol, side string, amount float64) (*exchange.MarketOrderResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &exchange.MarketOrderResult{OrderID: "id-" + f.id + "-" + side, Filled: f.filled, AvgPrice: f.avgPrice, Cost: f.filled * f.avgPrice}, nil
}
func (f *fakeEx) CreateMarketOrderWithParams(baseSymbol, side string, amount float64, params map[string]any) (*exchange.MarketOrderResult, error) {
	return f.CreateMarketOrder("swap", baseSymbol, side, amount)
}
func (f *fakeEx) CreateLimitOrder(marketType, baseSymbol, side string, amount, price float64) (string, error) {
	return "", errors.New("测试中不使用限价单")
}
func (f *fakeEx) CancelOrder(marketType, baseSymbol, orderID string) error { return nil }
func (f *fakeEx) FetchOrderStatus(marketType, baseSymbol, orderID string) (*exchange.OrderStatus, error) {
	return nil, errors.New("测试中不使用查单")
}
func (f *fakeEx) FetchLevelPrice(marketType, baseSymbol, side string, level int) (float64, error) {
	return 0, errors.New("测试中不使用盘口")
}
func (f *fakeEx) IsOutOfLevel(marketType, baseSymbol, side string, level int, myPrice float64) (bool, error) {
	return false, nil
}

// newTestEngine 造一个用 Taker 模式的测试引擎。
func newTestEngine(spot, swap Exchange) *Engine {
	return NewEngine(spot, swap,
		EngineConfig{MaxNetExposure: 0, MaxRetry: 1, PollInterval: time.Millisecond},
		LegConfig{OrderMethod: "taker"},
		func(level, action, symbol, msg string) {}, // 日志回调：测试里忽略
	)
}

// ---------- 引擎测试 ----------

// TestHedgeOnceSuccess 正常双腿对冲：两腿都成交，数量一致。
func TestHedgeOnceSuccess(t *testing.T) {
	spot := &fakeEx{id: "gate", filled: 10, avgPrice: 100}
	swap := &fakeEx{id: "binance", filled: 10, avgPrice: 100}
	e := newTestEngine(spot, swap)

	spotOut, swapOut, err := e.HedgeOnce("BTC/USDT", "buy", "sell", 10, false)
	if err != nil {
		t.Fatalf("HedgeOnce 应成功: %v", err)
	}
	if spotOut.Amount != 10 {
		t.Errorf("现货腿数量 = %v, want 10", spotOut.Amount)
	}
	if swapOut.Amount != 10 {
		t.Errorf("合约腿数量 = %v, want 10", swapOut.Amount)
	}
}

// TestHedgeOnceSpotFails 现货腿失败 → 整体失败。
func TestHedgeOnceSpotFails(t *testing.T) {
	spot := &fakeEx{id: "gate", err: errors.New("Insufficient funds")}
	swap := &fakeEx{id: "binance", filled: 10, avgPrice: 100}
	e := newTestEngine(spot, swap)

	_, _, err := e.HedgeOnce("BTC/USDT", "buy", "sell", 10, false)
	if err == nil {
		t.Fatal("现货腿失败应返回错误")
	}
	if !strings.Contains(err.Error(), "优先腿") {
		t.Errorf("错误应指出优先腿: %v", err)
	}
}

// TestHedgeOnceSwapFailsExposure 合约腿失败 → 提示净敞口需人工处理。
func TestHedgeOnceSwapFailsExposure(t *testing.T) {
	spot := &fakeEx{id: "gate", filled: 10, avgPrice: 100}
	swap := &fakeEx{id: "binance", err: errors.New("Insufficient funds")}
	e := newTestEngine(spot, swap)

	_, _, err := e.HedgeOnce("BTC/USDT", "buy", "sell", 10, false)
	if err == nil {
		t.Fatal("合约腿失败应返回错误")
	}
	if !strings.Contains(err.Error(), "净敞口") {
		t.Errorf("错误应提示净敞口: %v", err)
	}
}

// TestHedgeOnceExposureLimit 两腿数量差超过净敞口上限 → 停止并报错。
func TestHedgeOnceExposureLimit(t *testing.T) {
	spot := &fakeEx{id: "gate", filled: 10, avgPrice: 100}
	swap := &fakeEx{id: "binance", filled: 5, avgPrice: 100} // 合约只成交 5，差 5
	e := NewEngine(spot, swap,
		EngineConfig{MaxNetExposure: 2, MaxRetry: 1, PollInterval: time.Millisecond}, // 上限 2
		LegConfig{OrderMethod: "taker"},
		func(level, action, symbol, msg string) {})

	_, _, err := e.HedgeOnce("BTC/USDT", "buy", "sell", 10, false)
	if err == nil {
		t.Fatal("敞口超限应返回错误")
	}
	if !strings.Contains(err.Error(), "净敞口") {
		t.Errorf("错误应提示净敞口超限: %v", err)
	}
}

// ---------- 错误分类重试测试 ----------

// TestIsRetryable 可重试/不可重试错误分类。
func TestIsRetryable(t *testing.T) {
	retryable := []string{
		"connection timeout after 5000ms", "econnreset", "rate limit reached",
		"too many requests", "service busy", "http 429",
	}
	for _, msg := range retryable {
		if !isRetryable(errors.New(msg)) {
			t.Errorf("应判定可重试: %s", msg)
		}
	}
	notRetryable := []string{
		"Insufficient funds", "invalid symbol", "order not found",
		"Permission denied", "bad request",
	}
	for _, msg := range notRetryable {
		if isRetryable(errors.New(msg)) {
			t.Errorf("不应判定可重试: %s", msg)
		}
	}
	if isRetryable(nil) {
		t.Error("nil 错误不应可重试")
	}
}
