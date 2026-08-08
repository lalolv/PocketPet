package pet

import (
	"sync"
	"time"
)

// Clock 是时钟抽象，tick 引擎通过它取当前时间；
// 测试注入 FakeClock 以避免真实 sleep。
type Clock interface {
	Now() time.Time
}

// RealClock 返回真实系统时间。
type RealClock struct{}

// Now 实现 Clock。
func (RealClock) Now() time.Time { return time.Now() }

// FakeClock 是可手动推进的测试时钟，并发安全。
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFakeClock 创建起始时间为 now 的假时钟。
func NewFakeClock(now time.Time) *FakeClock {
	return &FakeClock{now: now}
}

// Now 实现 Clock。
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance 把假时钟推进 d。
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
