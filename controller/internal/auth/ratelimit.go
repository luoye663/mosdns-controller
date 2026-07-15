package auth

import (
	"sync"
	"time"
)

type LoginLimiter struct {
	mu       sync.Mutex
	failures map[string]attempt
}
type attempt struct {
	count int
	until time.Time
}

func NewLoginLimiter() *LoginLimiter { return &LoginLimiter{failures: make(map[string]attempt)} }
func (l *LoginLimiter) Allowed(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.failures[key].until.After(time.Now())
}
func (l *LoginLimiter) Failed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	item := l.failures[key]
	item.count++
	if item.count >= 5 {
		item.until = time.Now().Add(5 * time.Minute)
		item.count = 0
	}
	l.failures[key] = item
}
func (l *LoginLimiter) Succeeded(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}
