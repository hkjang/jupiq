package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/hkjang/jupiq/internal/mail"
	"github.com/hkjang/jupiq/internal/store"
)

func (s *Server) registerMail(mux router) {
	mux.HandleFunc("GET /api/v1/mail/deliveries", s.require("settings:read", s.mailDeliveries))
	mux.HandleFunc("POST /api/v1/mail/test", s.require("settings:write", s.mailTest))
}

// mailDeliveries는 무엇이 건물 밖으로 나갔는지 보여 준다. 본문은 기록에 없다.
func (s *Server) mailDeliveries(w http.ResponseWriter, r *http.Request) {
	limit, ok := queryIntOrReject(w, r, "limit", 50)
	if !ok {
		return
	}
	page, err := s.Store.ListMailDeliveries(r.Context(), r.URL.Query().Get("status"), limit)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	data(w, http.StatusOK, page)
}

// mailTest는 저장된 설정으로 실제 한 통을 보내고 결과를 그 자리에서 돌려준다.
// 릴레이 설정은 한 번에 맞는 일이 드물다.
func (s *Server) mailTest(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Recipient string `json:"recipient"`
	}
	if err := decodeJSON(r, &input); err != nil {
		apiError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	p := principal(r)
	recipient := strings.TrimSpace(input.Recipient)
	if recipient == "" {
		recipient = strings.TrimSpace(p.User.Email)
	}
	if !mail.ValidAddress(recipient) {
		apiError(w, r, http.StatusBadRequest, "invalid_recipient", "수신자 메일 주소를 입력해 주세요")
		return
	}
	if s.Mail == nil {
		apiError(w, r, http.StatusServiceUnavailable, "mail_unavailable", "메일 서비스가 구성되지 않았습니다")
		return
	}
	err := s.Mail.SendNow(r.Context(), mail.TestMessage(), p.User.ID, recipient)
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "mail.test", "mail", "", map[bool]string{true: "success", false: "failure"}[err == nil], errorText(err), nil, map[string]any{"recipient": recipient}))
	switch {
	case err == nil:
		data(w, http.StatusOK, map[string]any{"sent": true, "recipient": recipient})
	case errors.Is(err, mail.ErrDisabled):
		apiError(w, r, http.StatusConflict, "mail_disabled", "메일 알림이 꺼져 있습니다. 켜고 저장한 뒤 다시 시도하세요")
	case errors.Is(err, mail.ErrInvalid):
		apiError(w, r, http.StatusBadRequest, "mail_invalid", err.Error())
	default:
		apiError(w, r, http.StatusBadGateway, "mail_send_failed", "시험 발송에 실패했습니다: "+err.Error())
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// notifyMail은 메일 서비스가 있을 때만 알림을 보낸다. 모든 호출자가 요청 경로
// 위에 있으므로 실패는 사용자에게 드러나지 않는다.
func (s *Server) notifyMail(ctx context.Context, notification mail.Notification, actorUserID int64, recipients []int64) {
	if s.Mail == nil || len(recipients) == 0 {
		return
	}
	s.Mail.Notify(ctx, notification, actorUserID, recipients)
}

// notifyPermissionHolders는 전역으로 그 권한을 가진 사람 모두에게 보낸다.
func (s *Server) notifyPermissionHolders(ctx context.Context, notification mail.Notification, actorUserID int64, permission string) {
	if s.Mail == nil {
		return
	}
	recipients, err := s.Store.UsersWithGlobalPermission(ctx, permission)
	if err != nil {
		s.Logger.Warn("mail recipients lookup failed", "permission", permission, "error", err)
		return
	}
	s.notifyMail(ctx, notification, actorUserID, recipients)
}

// notifyHubHealth는 Hub 상태가 실제로 바뀌었을 때만 Hub 관리자에게 알린다.
// previous는 이번 결과를 쓰기 전에 읽은 Hub라 그 Status가 직전 상태다.
func (s *Server) notifyHubHealth(ctx context.Context, previous store.Hub, stored, success bool, cause string, actorUserID int64) {
	if s.Mail == nil || !stored {
		return
	}
	if notification, changed := mail.HubHealthChange(previous.ID, previous.Name, previous.Status, success, cause); changed {
		s.notifyPermissionHolders(ctx, notification, actorUserID, "hubs:write")
	}
}

// approvalRequesterName은 승인 요청 문서에서 요청자 이름을 읽는다.
func approvalRequesterName(details map[string]any) string {
	name, _ := details["requested_by"].(string)
	if strings.TrimSpace(name) == "" {
		return "알 수 없는 사용자"
	}
	return name
}

// approvalOwner는 결과를 받을 요청자다. 승인 요청은 대상 작업 API가 요청자를
// 소유자로 만들므로 소유자가 곧 요청자다.
func approvalOwner(approval store.Resource) []int64 {
	if approval.OwnerUserID == nil {
		return nil
	}
	return []int64{*approval.OwnerUserID}
}
