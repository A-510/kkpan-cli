package transfer

import (
	"context"
	"io"
	"sync"
	"time"
)

// RateLimiter 实现线程安全的令牌桶字节限速器
type RateLimiter struct {
	mu           sync.Mutex
	bytesPerSec  int64
	tokens       float64
	capacity     float64
	lastRefillAt time.Time
}

// NewRateLimiter 创建限速器；bytesPerSec <= 0 表示不限制传输速率
func NewRateLimiter(bytesPerSec int64) *RateLimiter {
	if bytesPerSec <= 0 {
		return &RateLimiter{bytesPerSec: 0}
	}
	capBytes := float64(bytesPerSec)
	if capBytes < 32*1024 {
		capBytes = 32 * 1024
	}
	return &RateLimiter{
		bytesPerSec:  bytesPerSec,
		tokens:       capBytes,
		capacity:     capBytes,
		lastRefillAt: time.Now(),
	}
}

// Wait 阻塞直到成功消费 n 字节令牌或上下文被取消
func (r *RateLimiter) Wait(ctx context.Context, n int) error {
	if r == nil || r.bytesPerSec <= 0 || n <= 0 {
		return nil
	}

	remaining := float64(n)
	for remaining > 0 {
		r.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(r.lastRefillAt).Seconds()
		if elapsed > 0 {
			r.tokens += elapsed * float64(r.bytesPerSec)
			if r.tokens > r.capacity {
				r.tokens = r.capacity
			}
			r.lastRefillAt = now
		}

		if r.tokens >= remaining {
			r.tokens -= remaining
			r.mu.Unlock()
			return nil
		}

		if r.tokens > 0 {
			remaining -= r.tokens
			r.tokens = 0
		}
		needSec := remaining / float64(r.bytesPerSec)
		if needSec < 0.005 {
			needSec = 0.005
		}
		if needSec > 0.2 {
			needSec = 0.2
		}
		r.mu.Unlock()

		timer := time.NewTimer(time.Duration(needSec * float64(time.Second)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

// LimitedReader 为任意 io.Reader 叠加限速与上下文取消支持
type LimitedReader struct {
	reader  io.Reader
	limiter *RateLimiter
	ctx     context.Context
}

// NewLimitedReader 包装底层 Reader 并绑定限速器
func NewLimitedReader(ctx context.Context, r io.Reader, limiter *RateLimiter) *LimitedReader {
	if ctx == nil {
		ctx = context.Background()
	}
	return &LimitedReader{
		reader:  r,
		limiter: limiter,
		ctx:     ctx,
	}
}

func (l *LimitedReader) Read(p []byte) (int, error) {
	if err := l.ctx.Err(); err != nil {
		return 0, err
	}
	// 将单次读取上限压在 64KB 以避免高延迟突发占用令牌
	buf := p
	if l.limiter != nil && l.limiter.bytesPerSec > 0 && len(buf) > 64*1024 {
		buf = p[:64*1024]
	}
	n, err := l.reader.Read(buf)
	if n > 0 && l.limiter != nil {
		if wErr := l.limiter.Wait(l.ctx, n); wErr != nil {
			return n, wErr
		}
	}
	return n, err
}

// ProgressSnapshot 描述单个传输任务的实时进度快照
type ProgressSnapshot struct {
	Name        string
	Completed   int64
	Total       int64
	BytesPerSec float64
	Done        bool
}
