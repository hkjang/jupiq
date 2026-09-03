package api

import (
	"strings"
	"sync"
	"time"
)

const (
	loginAttemptWindow = 10 * time.Minute
	loginAttemptLimit  = 8
	loginIPLimit       = 40
	loginAccountLimit  = 16
	loginLimiterMax    = 10000
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
	return "pair\x00" + strings.TrimSpace(ip) + "\x00" + strings.ToLower(strings.TrimSpace(username))
}

func loginAttemptKeys(ip, username string) []string {
	return []string{
		loginAttemptKey(ip, username),
		"ip\x00" + strings.TrimSpace(ip),
		"account\x00" + strings.ToLower(strings.TrimSpace(username)),
	}
}

func loginLimit(key string) int {
	if strings.HasPrefix(key, "ip\x00") {
		return loginIPLimit
	}
	if strings.HasPrefix(key, "account\x00") {
		return loginAccountLimit
	}
	return loginAttemptLimit
}

func (l *loginLimiter) allow(ip, username string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, key := range loginAttemptKeys(ip, username) {
		attempt, ok := l.attempts[key]
		if !ok {
			continue
		}
		if now.Sub(attempt.windowStarted) >= loginAttemptWindow {
			delete(l.attempts, key)
			continue
		}
		if attempt.failures >= loginLimit(key) {
			return false
		}
	}
	return true
}

func (l *loginLimiter) failed(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.prune(now)
	for _, key := range loginAttemptKeys(ip, username) {
		attempt, ok := l.attempts[key]
		if !ok || now.Sub(attempt.windowStarted) >= loginAttemptWindow {
			attempt = loginAttempt{windowStarted: now}
		}
		attempt.failures++
		l.attempts[key] = attempt
	}
}

func (l *loginLimiter) succeeded(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, loginAttemptKey(ip, username))
	delete(l.attempts, "account\x00"+strings.ToLower(strings.TrimSpace(username)))
}

func (l *loginLimiter) prune(now time.Time) {
	if len(l.attempts) < loginLimiterMax {
		return
	}
	for key, attempt := range l.attempts {
		if now.Sub(attempt.windowStarted) >= loginAttemptWindow {
			delete(l.attempts, key)
		}
	}
	for len(l.attempts) >= loginLimiterMax {
		var oldestKey string
		var oldest time.Time
		for key, attempt := range l.attempts {
			if oldestKey == "" || attempt.windowStarted.Before(oldest) {
				oldestKey, oldest = key, attempt.windowStarted
			}
		}
		delete(l.attempts, oldestKey)
	}
}
