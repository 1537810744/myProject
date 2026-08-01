// 【风险控制】熔断器（circuit breaker，也叫"电路断路器"）。
// 职责：统计"连续失败次数"，超过阈值就【跳闸】（tripped）。
// 跳闸之后不再执行交易，直到人工恢复（Resume/Reset）。
// 为什么需要它？—— 系统持续出错时（比如交易所连不上、账号被封），如果不熔断，
// 程序会 15 秒一次地反复尝试，把真金白银越搞越糟，还轰炸你的通知。熔断 = 及时止损。
//
// 设计：这是一个【纯逻辑】类型——它不碰数据库、不发通知、不读设置；
// 触发后的副作用（停机 + 告警）由调用方 autotrade.Service 处理。
// 这样它就能被单元测试完整覆盖（见 breaker_test.go）。
// 语法点：struct、方法接收者、sync.Mutex、指针接收者改字段。
package autotrade

import (
	"sync"
)

// Breaker 熔断器。
type Breaker struct {
	mu       sync.Mutex // 互斥锁：熔断器可能被后台循环写、被 HTTP 接口读，必须加锁
	maxFail  int        // 连续失败多少次触发跳闸（阈值）
	failures int        // 当前连续失败次数
	tripped  bool       // 是否已跳闸
}

// NewBreaker 创建熔断器。maxFail < 1 时兜底为 1（至少 1 次失败就跳闸）。
func NewBreaker(maxFail int) *Breaker {
	if maxFail < 1 {
		maxFail = 1
	}
	return &Breaker{maxFail: maxFail}
}

// SetMaxFail 动态调整阈值（从设置模块读，用户可调）。
func (b *Breaker) SetMaxFail(n int) {
	b.mu.Lock()         // 写操作加锁
	defer b.mu.Unlock() // 解锁挂在 defer：任何 return 都执行
	if n < 1 {
		n = 1
	}
	b.maxFail = n
}

// RecordSuccess 记一次成功：清零连续失败计数（成功了说明系统恢复正常）。
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
}

// RecordFailure 记一次失败：连续失败 +1；达到阈值就跳闸。
// 返回 true 表示【刚刚】触发跳闸——调用方此时该停机 + 告警。
func (b *Breaker) RecordFailure() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if !b.tripped && b.failures >= b.maxFail {
		b.tripped = true
		return true // 刚刚跳闸
	}
	return false
}

// IsTripped 是否已跳闸。
func (b *Breaker) IsTripped() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tripped
}

// Reset 人工恢复：清除跳闸状态和失败计数（配合"恢复交易"接口调用）。
func (b *Breaker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tripped = false
	b.failures = 0
}
