package auth

import (
	"sync"
	"time"
)

const (
	maxLoginLimiterEntries = 4_096
	loginAttemptRetention  = 10 * time.Minute
)

type LoginLimiter struct {
	mu       sync.Mutex
	failures map[string]attempt
}
type attempt struct {
	count    int
	until    time.Time
	lastSeen time.Time
}

func NewLoginLimiter() *LoginLimiter { return &LoginLimiter{failures: make(map[string]attempt)} }
func (l *LoginLimiter) Allowed(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	item, exists := l.failures[key]
	if !exists || item.until.IsZero() {
		return true
	}
	if !item.until.After(time.Now()) {
		delete(l.failures, key)
		return true
	}
	return false
}
func (l *LoginLimiter) Failed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if _, exists := l.failures[key]; !exists && len(l.failures) >= maxLoginLimiterEntries {
		l.evict(now)
	}
	item := l.failures[key]
	item.count++
	item.lastSeen = now
	if item.count >= 5 {
		item.until = now.Add(5 * time.Minute)
		item.count = 0
	}
	l.failures[key] = item
}
func (l *LoginLimiter) Succeeded(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}

func (l *LoginLimiter) evict(now time.Time) {
	for key, item := range l.failures {
		if !item.until.After(now) && now.Sub(item.lastSeen) >= loginAttemptRetention {
			delete(l.failures, key)
		}
	}
	for len(l.failures) >= maxLoginLimiterEntries {
		for key := range l.failures {
			delete(l.failures, key)
			break
		}
	}
}
