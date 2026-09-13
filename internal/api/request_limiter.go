package api

import (
	"sync"
	"time"
)

const (
	// oidcStartWindow·oidcStartIPLimit는 OIDC 로그인 시작 요청의 IP별 상한이다.
	// 이 경로는 누구나 부를 수 있고 부를 때마다 제공자의 Discovery 문서를 받아
	// 오므로, 익명 요청 하나가 Keycloak으로 가는 요청 하나로 증폭된다. 자동
	// 로그인(silent SSO)이 켜지면 탭 세션마다 한 번씩 정상적으로도 들어오므로
	// 사무실 NAT 뒤의 사용자 수를 넉넉히 넘는 값으로 둔다.
	oidcStartWindow   = time.Minute
	oidcStartIPLimit  = 120
	requestLimiterMax = 10000
)

type requestWindow struct {
	started time.Time
	count   int
}

// requestLimiter는 키별로 고정 창 안의 요청 수를 센다. 실패만 세는
// loginLimiter와 달리 호출 자체가 비싼 경로(호출마다 외부 요청이 나가는
// OIDC 시작)에 쓰며, 창은 첫 요청 시각부터 시작해 막힌 요청이 창을 늘리지 않는다.
type requestLimiter struct {
	mu     sync.Mutex
	window time.Duration
	limit  int
	hits   map[string]requestWindow
	now    func() time.Time
}

func newRequestLimiter(window time.Duration, limit int) *requestLimiter {
	return &requestLimiter{window: window, limit: limit, hits: map[string]requestWindow{}, now: time.Now}
}

// allow는 key의 요청 하나를 세고 상한 안이면 true를 돌려준다.
func (l *requestLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.prune(now)
	current, ok := l.hits[key]
	if !ok || now.Sub(current.started) >= l.window {
		current = requestWindow{started: now}
	}
	current.count++
	l.hits[key] = current
	return current.count <= l.limit
}

// prune은 loginLimiter.prune과 같은 규칙으로 지도가 무한히 자라지 않게 한다:
// 상한에 닿으면 만료된 창을 버리고, 그래도 넘치면 가장 오래된 창부터 버린다.
func (l *requestLimiter) prune(now time.Time) {
	if len(l.hits) < requestLimiterMax {
		return
	}
	for key, current := range l.hits {
		if now.Sub(current.started) >= l.window {
			delete(l.hits, key)
		}
	}
	for len(l.hits) >= requestLimiterMax {
		var oldestKey string
		var oldest time.Time
		for key, current := range l.hits {
			if oldestKey == "" || current.started.Before(oldest) {
				oldestKey, oldest = key, current.started
			}
		}
		delete(l.hits, oldestKey)
	}
}
