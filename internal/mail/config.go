package mail

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	// SettingKey는 settings 테이블에서 이 설정이 저장되는 문서 키다. 문서의
	// 필드는 표준의 이름(mail.enabled, mail.smtp_host, …)에서 접두사를 뗀 것이다.
	SettingKey = "mail"
	// SecretKey는 SMTP 비밀번호가 암호화 저장되는 secrets 키다. 설정 API는
	// 이 값을 돌려주지 않고 '설정됨'만 알린다.
	SecretKey = "mail.password"

	SecurityAuto     = "auto"
	SecurityNone     = "none"
	SecurityStartTLS = "starttls"
	SecurityTLS      = "tls"

	// 사내 릴레이의 흔한 모습이 기본값이다: 25번 포트, 인증 없음, 서버가
	// 알리는 대로 암호화.
	defaultPort           = 25
	defaultTimeoutSeconds = 10
	maxTimeoutSeconds     = 120
)

// 이벤트 이름. 각 이름은 설정의 notify_<이벤트> 스위치와 짝이다. jupiq에서
// 사람이 실제로 기다리는 일만 고른다 — 이 메일이 오지 않으면 누군가 손해를
// 보거나 화면을 계속 새로고침하는 것들이다.
const (
	// EventApprovalRequested: 검토·승인 차례가 된 사람에게. 승인이 늦으면
	// 요청자의 서버 작업이 멈춰 있다.
	EventApprovalRequested = "approval.requested"
	// EventApprovalDecided: 요청자에게 승인·실행 실패·반려 결과를. 결과를 모르면
	// 승인 목록을 계속 새로고침한다.
	EventApprovalDecided = "approval.decided"
	// EventHubHealth: Hub 관리자에게 수집이 멈추거나 되살아났음을. 상태가 바뀔
	// 때만 보내며, 멈춘 채로 있는 동안 되풀이하지 않는다.
	EventHubHealth = "hub.health"
	// EventKeyExpiring: 키 소유자에게 API 키 만료가 다가왔음을, 키마다 한 번.
	// 만료되면 자동화가 소리 없이 401을 받기 시작한다.
	EventKeyExpiring = "key.expiring"
	// EventTest: 관리 화면의 시험 발송. 스위치가 없고 항상 보낸다.
	EventTest = "test"
)

// EventSettings는 이벤트별 스위치의 설정 필드다. 순서는 화면과 문서가 공유한다.
var EventSettings = []struct{ Event, Field string }{
	{EventApprovalRequested, "notify_approval_request"},
	{EventApprovalDecided, "notify_approval_decision"},
	{EventHubHealth, "notify_hub_health"},
	{EventKeyExpiring, "notify_key_expiry"},
}

// Config는 저장된 설정 문서와 복호화한 비밀번호에서 읽은 구성이다.
type Config struct {
	Enabled       bool
	Host          string
	Port          int
	Security      string
	SkipTLSVerify bool
	Username      string
	Password      string
	FromAddress   string
	FromName      string
	BaseURL       string
	Timeout       time.Duration
	// Events는 명시적으로 저장된 이벤트 스위치다. 없는 이벤트는 켜진 것으로
	// 본다 — 알림을 새로 더할 때 설정을 먼저 바꿀 필요가 없도록.
	Events map[string]bool
}

// Default는 새로 설치한 곳의 상태다. 꺼져 있다.
func Default() Config {
	return Config{Port: defaultPort, Security: SecurityAuto, FromName: "jupiq", Timeout: defaultTimeoutSeconds * time.Second, Events: map[string]bool{}}
}

// ParseConfig는 settings의 mail 문서를 읽는다. 깨진 문서는 기본값(꺼짐)이다.
func ParseConfig(raw json.RawMessage, password string) Config {
	var values map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &values)
	}
	return ReadConfig(values, password)
}

// ReadConfig는 이미 디코딩된 설정 map에서 구성을 만든다. 설정 검증이 저장 전에
// 같은 규칙으로 값을 보도록 내보낸다.
func ReadConfig(values map[string]any, password string) Config {
	config := Default()
	config.Password = password
	if values == nil {
		return config
	}
	config.Enabled, _ = values["enabled"].(bool)
	config.Host = stringValue(values, "smtp_host", "")
	config.Username = stringValue(values, "username", "")
	config.FromAddress = stringValue(values, "from_address", "")
	config.FromName = stringValue(values, "from_name", config.FromName)
	config.BaseURL = strings.TrimRight(stringValue(values, "base_url", ""), "/")
	config.Security = strings.ToLower(stringValue(values, "security", SecurityAuto))
	config.SkipTLSVerify, _ = values["skip_tls_verify"].(bool)
	if port, ok := numberValue(values, "smtp_port"); ok && port > 0 {
		config.Port = port
	}
	if seconds, ok := numberValue(values, "timeout_seconds"); ok && seconds > 0 {
		config.Timeout = time.Duration(seconds) * time.Second
	}
	// 465번 포트의 릴레이는 암시적 TLS라 별도 설정이 필요 없다.
	if config.Security == SecurityAuto && config.Port == 465 {
		config.Security = SecurityTLS
	}
	for _, item := range EventSettings {
		if enabled, ok := values[item.Field].(bool); ok {
			config.Events[item.Event] = enabled
		}
	}
	return config
}

// Validate는 켜진 설정이 실제로 보낼 수 있는지 확인한다. 검증은 저장 시점과
// 발송 시점 두 곳에서 같은 규칙으로 돈다.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("%w: mail.smtp_host가 필요합니다", ErrInvalid)
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("%w: mail.smtp_port는 1~65535 범위여야 합니다", ErrInvalid)
	}
	if !validAddress(c.FromAddress) {
		return fmt.Errorf("%w: mail.from_address는 메일 주소여야 합니다", ErrInvalid)
	}
	switch c.Security {
	case SecurityAuto, SecurityNone, SecurityStartTLS, SecurityTLS:
	default:
		return fmt.Errorf("%w: mail.security는 auto, none, starttls, tls 중 하나여야 합니다", ErrInvalid)
	}
	if seconds := int(c.Timeout / time.Second); seconds < 1 || seconds > maxTimeoutSeconds {
		return fmt.Errorf("%w: mail.timeout_seconds는 1~%d 범위여야 합니다", ErrInvalid, maxTimeoutSeconds)
	}
	// security=none에 사용자 이름이 있으면 자격증명이 평문으로 망을 건너게 되므로
	// 저장 시점에 막는다. auto는 릴레이가 STARTTLS를 알리면 되므로 세션이 정한다.
	if c.Security == SecurityNone && strings.TrimSpace(c.Username) != "" && !loopbackHost(c.Host) {
		return fmt.Errorf("%w: mail.security가 none이면 자격증명이 평문으로 나가므로 사용자 이름을 비우거나 starttls·tls를 쓰세요", ErrInvalid)
	}
	return nil
}

// Allows는 이벤트를 보내도 되는지 알린다. 시험 발송과 알려지지 않은 이벤트는
// 항상 보낸다.
func (c Config) Allows(event string) bool {
	if enabled, known := c.Events[event]; known {
		return enabled
	}
	return true
}

// Address는 RFC 5322 From 헤더 값이다.
func (c Config) Address() string {
	from := strings.TrimSpace(c.FromAddress)
	if name := strings.TrimSpace(c.FromName); name != "" {
		return fmt.Sprintf("%s <%s>", name, from)
	}
	return from
}

// Link는 메일 속 링크를 만든다. base_url이 없으면 빈 값이라 본문에 링크 줄이
// 빠진다 — 상대 경로는 메일에서 아무 데도 가리키지 않는다.
func (c Config) Link(path string) string {
	if c.BaseURL == "" {
		return ""
	}
	return c.BaseURL + "/" + strings.TrimLeft(path, "/")
}

func (c Config) endpoint() string {
	return net.JoinHostPort(strings.TrimSpace(c.Host), fmt.Sprint(c.Port))
}

// validAddress는 시험 발송 수신자와 보내는 주소에 쓰는 느슨한 검사다. 릴레이가
// 최종 판정을 하므로 여기서는 헤더를 깨뜨릴 값만 거른다.
func validAddress(address string) bool {
	address = strings.TrimSpace(address)
	at := strings.LastIndex(address, "@")
	if at < 1 || at == len(address)-1 || len(address) > 254 {
		return false
	}
	return !strings.ContainsAny(address, " <>,\r\n\t\"")
}

// ValidAddress는 validAddress를 API 경계가 쓰도록 내보낸 것이다.
func ValidAddress(address string) bool { return validAddress(address) }

func stringValue(values map[string]any, key, fallback string) string {
	if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func numberValue(values map[string]any, key string) (int, bool) {
	switch typed := values[key].(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case json.Number:
		if value, err := typed.Int64(); err == nil {
			return int(value), true
		}
	}
	return 0, false
}
