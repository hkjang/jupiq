package api

import (
	"strings"
	"sync"
	"time"
)

const (
	loginAttemptWindow = 10 * time.Minute
	loginAttemptLimit  = 8
)

type loginAttempt struct {
	windowStarted time.Time
	failures      int
}

type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]loginAttempt
	now      func() time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: map[string]loginAttempt{}, now: time.Now}
}

func loginAttemptKey(ip, username string) string {
	return strings.TrimSpace(ip) + "\x00" + strings.ToLower(strings.TrimSpace(username))
}

func (l *loginLimiter) allow(ip, username string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	key, now := loginAttemptKey(ip, username), l.now()
	attempt, ok := l.attempts[key]
	if !ok || now.Sub(attempt.windowStarted) >= loginAttemptWindow {
		if ok {
			delete(l.attempts, key)
		}
		return true
	}
	return attempt.failures < loginAttemptLimit
}

func (l *loginLimiter) failed(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key, now := loginAttemptKey(ip, username), l.now()
	attempt, ok := l.attempts[key]
	if !ok || now.Sub(attempt.windowStarted) >= loginAttemptWindow {
		attempt = loginAttempt{windowStarted: now}
	}
	attempt.failures++
	l.attempts[key] = attempt
}

func (l *loginLimiter) succeeded(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, loginAttemptKey(ip, username))
}
