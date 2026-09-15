package mail

import (
	"fmt"
	"strings"
	"time"
)

// Notification은 수신자를 정하기 전의 이벤트 메일 한 통이다.
type Notification struct {
	Event   string
	Subject string
	Lines   []string
	// Path는 메일 속 링크가 가리킬 이 앱의 경로다. base_url이 있을 때만 절대
	// 주소가 된다.
	Path string
	// Reference는 같은 대상에 대한 기록을 묶어 보거나 중복을 막는 표시다
	// (예: approval:12, hub:3, api_key:7). 본문이 아니라 기록에만 남는다.
	Reference string
}

// Render는 본문을 만든다. 링크와, 왜 이 메일이 왔는지 알리는 꼬리말을 붙인다.
func (n Notification) Render(config Config) string {
	lines := append([]string{}, n.Lines...)
	if link := config.Link(n.Path); n.Path != "" && link != "" {
		lines = append(lines, "", "바로 열기: "+link)
	}
	lines = append(lines, "", "—", "이 메일은 jupiq 메일 알림 설정에 따라 자동으로 발송되었습니다. 관리자 설정 → 메일 알림에서 이벤트별로 끌 수 있습니다.")
	return strings.Join(lines, "\n")
}

// ApprovalRequested는 검토 또는 승인 차례가 된 사람에게 보낸다. stage는
// "review"(팀장 선검토) 또는 "approve"다.
func ApprovalRequested(approvalID int64, name, requester, stage, reason string) Notification {
	turn := "승인"
	if stage == "review" {
		turn = "검토"
	}
	lines := []string{fmt.Sprintf("%s 님이 요청한 '%s'이(가) %s을 기다리고 있습니다.", requester, name, turn)}
	if strings.TrimSpace(reason) != "" {
		lines = append(lines, "", quote(reason))
	}
	return Notification{
		Event:     EventApprovalRequested,
		Subject:   fmt.Sprintf("[jupiq] %s 요청: %s", turn, name),
		Lines:     lines,
		Path:      "/approvals",
		Reference: fmt.Sprintf("approval:%d", approvalID),
	}
}

// ApprovalDecided는 요청자에게 결과를 알린다. decision은 "approved",
// "rejected", "failed"(승인됐지만 실행이 실패) 중 하나다.
func ApprovalDecided(approvalID int64, name, actor, decision, reason string) Notification {
	var result, subject string
	switch decision {
	case "approved":
		result, subject = "승인되어 실행되었습니다", "승인됨"
	case "failed":
		result, subject = "승인되었지만 실행에 실패했습니다. 관리자에게 문의하세요", "실행 실패"
	default:
		result, subject = "반려되었습니다", "반려됨"
	}
	lines := []string{fmt.Sprintf("'%s' 요청이 %s 님에 의해 %s.", name, actor, result)}
	if strings.TrimSpace(reason) != "" {
		lines = append(lines, "", quote(reason))
	}
	return Notification{
		Event:     EventApprovalDecided,
		Subject:   fmt.Sprintf("[jupiq] %s: %s", subject, name),
		Lines:     lines,
		Path:      "/approvals",
		Reference: fmt.Sprintf("approval:%d", approvalID),
	}
}

// HubHealthChange는 Hub 상태가 실제로 넘어갔을 때만 알림을 만든다. degraded인
// 채로 있는 동안 수집 주기마다 되풀이 보내면 첫 주에 규칙으로 통째로
// 버려지므로, healthy↔degraded 전이 하나에 한 통이다. 아직 한 번도 수집되지
// 않은 Hub(status가 비어 있거나 unknown)가 처음 실패하는 것도 전이로 본다 —
// 등록 직후 토큰이 틀린 경우가 바로 그것이다.
func HubHealthChange(hubID int64, name, previousStatus string, success bool, cause string) (Notification, bool) {
	wasDegraded := previousStatus == "degraded"
	switch {
	case success && wasDegraded:
		return HubHealth(hubID, name, true, ""), true
	case !success && !wasDegraded:
		return HubHealth(hubID, name, false, cause), true
	}
	return Notification{}, false
}

// HubHealth는 Hub 수집이 멈추거나 되살아났을 때 Hub 관리자에게 보낸다.
func HubHealth(hubID int64, name string, healthy bool, cause string) Notification {
	if healthy {
		return Notification{
			Event:     EventHubHealth,
			Subject:   fmt.Sprintf("[jupiq] Hub 복구: %s", name),
			Lines:     []string{fmt.Sprintf("'%s' Hub 수집이 다시 정상입니다.", name)},
			Path:      "/hubs",
			Reference: fmt.Sprintf("hub:%d", hubID),
		}
	}
	lines := []string{fmt.Sprintf("'%s' Hub에서 수집이 실패해 상태가 degraded로 바뀌었습니다. 복구될 때까지 이 Hub의 사용자·서버 현황은 갱신되지 않습니다.", name)}
	if strings.TrimSpace(cause) != "" {
		lines = append(lines, "", "원인: "+truncateRunes(strings.TrimSpace(cause), 300))
	}
	return Notification{
		Event:     EventHubHealth,
		Subject:   fmt.Sprintf("[jupiq] Hub 수집 실패: %s", name),
		Lines:     lines,
		Path:      "/hubs",
		Reference: fmt.Sprintf("hub:%d", hubID),
	}
}

// ExpiringKey는 KeysExpiring이 한 통으로 묶는 키 하나다.
type ExpiringKey struct {
	ID        int64
	Name      string
	Prefix    string
	ExpiresAt time.Time
}

// KeysExpiring은 한 사용자의 만료 임박 키를 한 통에 묶는다. 키마다 한 통씩
// 보내면 같은 날 만료되는 키 셋에 세 통이 온다.
func KeysExpiring(keys []ExpiringKey, now time.Time) Notification {
	lines := []string{"다음 개인 API 키가 곧 만료됩니다. 만료 뒤에는 그 키를 쓰는 자동화가 인증에 실패합니다. 내 정보 → API 키에서 회전하거나 새로 발급하세요.", ""}
	references := make([]string, 0, len(keys))
	for _, key := range keys {
		days := int(key.ExpiresAt.Sub(now).Hours() / 24)
		lines = append(lines, fmt.Sprintf("- %s (%s…) — %s 만료, %d일 남음", key.Name, key.Prefix, key.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC"), days))
		references = append(references, fmt.Sprintf("api_key:%d", key.ID))
	}
	subject := "[jupiq] API 키 만료 임박"
	if len(keys) == 1 {
		subject = fmt.Sprintf("[jupiq] API 키 만료 임박: %s", keys[0].Name)
	}
	return Notification{Event: EventKeyExpiring, Subject: subject, Lines: lines, Path: "/personal", Reference: strings.Join(references, ",")}
}

// TestMessage는 관리 화면에서 릴레이가 동작하는지 증명한다.
func TestMessage() Notification {
	return Notification{
		Event:   EventTest,
		Subject: "[jupiq] SMTP 시험 발송",
		Lines:   []string{"jupiq 관리자 설정에서 보낸 시험 메일입니다.", "이 메일을 받았다면 SMTP 릴레이 설정이 정상입니다."},
	}
}

func quote(body string) string {
	lines := strings.Split(truncateRunes(strings.TrimSpace(body), 500), "\n")
	for index, line := range lines {
		lines[index] = "> " + line
	}
	return strings.Join(lines, "\n")
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
