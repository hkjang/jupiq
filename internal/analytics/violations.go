package analytics

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// MaxViolations는 기록기의 상한이다. 차단된 요청은 페이지를 볼 때마다 되풀이
// 되므로 중요한 정보는 횟수가 아니라 어떤 출처가 막혔는가다 — 서로 다른 출처
// 100개면 스니펫 하나를 고치기에 충분하다.
const MaxViolations = 100

// Violation은 콘텐츠 보안 정책이 거부한 출처 하나다. 어떤 지시어가 거부했는지
// 함께 두어 화면이 무엇을 허용해야 하는지 말할 수 있게 한다.
type Violation struct {
	Origin    string    `json:"origin"`
	Directive string    `json:"directive"`
	Page      string    `json:"page"`
	Count     int       `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Allowed   bool      `json:"allowed"`
}

// Recorder는 브라우저가 신고한 정책 위반을 모은다. 일부러 메모리에만 둔다:
// 신고는 스니펫을 붙이는 사람을 위한 실시간 진단 자료지 감사 기록이 아니며,
// DB 밖에 두어야 브라우저가 마음껏 신고해도 저장소가 자라지 않는다.
type Recorder struct {
	mutex      sync.Mutex
	violations map[string]*Violation
	now        func() time.Time
}

func NewRecorder() *Recorder {
	return &Recorder{violations: make(map[string]*Violation), now: time.Now}
}

// Record는 차단된 요청 하나를 적는다. 브라우저 확장이나 data: URL처럼 http
// 출처가 아닌 것은 허용할 수도 없고 허용해도 쓸모가 없으므로 버린다.
func (r *Recorder) Record(blockedURI, directive, page string) {
	origin := originOf(blockedURI)
	if origin == "" {
		return
	}
	directive = strings.TrimSpace(strings.ToLower(directive))
	if index := strings.IndexByte(directive, ' '); index > 0 {
		directive = directive[:index]
	}
	if directive == "" {
		directive = "connect-src"
	}
	page = strings.TrimSpace(page)
	if len(page) > 512 {
		page = page[:512]
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	key := directive + " " + origin
	if existing, found := r.violations[key]; found {
		existing.Count++
		existing.LastSeen = r.now()
		existing.Page = page
		return
	}
	if len(r.violations) >= MaxViolations {
		r.evictOldest()
	}
	moment := r.now()
	r.violations[key] = &Violation{Origin: origin, Directive: directive, Page: page, Count: 1, FirstSeen: moment, LastSeen: moment}
}

func (r *Recorder) evictOldest() {
	var oldestKey string
	var oldest time.Time
	for key, violation := range r.violations {
		if oldestKey == "" || violation.LastSeen.Before(oldest) {
			oldestKey, oldest = key, violation.LastSeen
		}
	}
	delete(r.violations, oldestKey)
}

// List는 차단된 출처를 최근 것부터 돌려준다. 현재 설정이 이미 허용하는 것은
// 표시해 두어, 고친 스니펫이 계속 잔소리하지 않게 한다.
func (r *Recorder) List(config Config) []Violation {
	allowed := make(map[string]struct{})
	scripts, connects, images := config.PolicySources()
	for _, group := range [][]string{scripts, connects, images} {
		for _, origin := range group {
			allowed[strings.ToLower(strings.TrimSuffix(origin, "/"))] = struct{}{}
		}
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	items := make([]Violation, 0, len(r.violations))
	for _, violation := range r.violations {
		copied := *violation
		_, known := allowed[copied.Origin]
		copied.Allowed = known || matchesWildcard(copied.Origin, allowed)
		items = append(items, copied)
	}
	sort.Slice(items, func(first, second int) bool {
		if items[first].LastSeen.Equal(items[second].LastSeen) {
			return items[first].Origin < items[second].Origin
		}
		return items[first].LastSeen.After(items[second].LastSeen)
	})
	return items
}

// Forget은 기록을 비운다. 관리자가 스니펫을 고친 뒤 아직 막히는 것이 있는지
// 확인할 때 쓴다.
func (r *Recorder) Forget() {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.violations = make(map[string]*Violation)
}

// matchesWildcard는 https://*.google-analytics.com 같은 정책 항목을 맞춘다.
func matchesWildcard(origin string, allowed map[string]struct{}) bool {
	host := origin
	if index := strings.Index(origin, "://"); index >= 0 {
		host = origin[index+3:]
	}
	for pattern := range allowed {
		star := strings.Index(pattern, "*.")
		if star < 0 {
			continue
		}
		if strings.HasPrefix(origin, pattern[:star]) && strings.HasSuffix(host, pattern[star+1:]) {
			return true
		}
	}
	return false
}

// AddAllowedHost는 쉼표로 나뉜 허용 목록에 출처 하나를 더한다. 기존 항목과
// 순서는 그대로 두고, 이미 있으면 목록을 바꾸지 않는다.
func AddAllowedHost(existing, origin string) string {
	origin = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(origin), "/"))
	if origin == "" {
		return existing
	}
	for _, host := range SplitHosts(existing) {
		if strings.EqualFold(host, origin) {
			return existing
		}
	}
	if strings.TrimSpace(existing) == "" {
		return origin
	}
	return strings.TrimSpace(existing) + ", " + origin
}
