package mail

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// Delivery는 발송 시도 하나의 기록이다. 본문은 담지 않는다 — 제목과
// 수신자면 "안 왔다"는 문의에 답하기에 충분하고, 본문까지 담으면 기록이
// 그 자체로 유출 경로가 된다.
type Delivery struct {
	ID           int64     `json:"id"`
	Event        string    `json:"event"`
	Recipient    string    `json:"recipient"`
	Subject      string    `json:"subject"`
	Reference    string    `json:"reference,omitempty"`
	ActorUserID  *int64    `json:"actor_user_id,omitempty"`
	Status       string    `json:"status"`
	Attempts     int       `json:"attempts"`
	ErrorMessage string    `json:"error_message,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

const (
	StatusQueued = "queued"
	StatusSent   = "sent"
	StatusFailed = "failed"
)

const (
	// deliveryAttempts와 retryDelay: 잠깐 연결을 거부하는 릴레이는 흔하고, 알림을
	// 잃는 것이 몇 초 기다리는 것보다 나쁘다.
	deliveryAttempts = 2
	retryDelay       = 2 * time.Second
	// maxConcurrentDeliveries는 동시에 열리는 릴레이 연결의 상한이다. 승인 요청
	// 한 건이 승인자 수만큼 연결을 열지 않고, 응답 없는 릴레이 앞에서 요청 속도만큼
	// 연결이 쌓이지 않는다. 넘치는 발송은 기록만 먼저 남고 자리가 나면 나간다.
	maxConcurrentDeliveries = 4
	// recordTimeout은 발송 결과를 저장소에 쓰는 시간이다. 발송 예산과 별개다.
	recordTimeout = 10 * time.Second
)

// deliveryBudget은 배경 발송 한 건의 최악 시간이다. dial()은 접속(Dialer.Timeout)
// 뒤 세션에 다시 Timeout의 deadline을 걸므로 시도 하나가 최대 2×Timeout이고,
// 여기에 재시도 사이의 대기를 더한다.
func deliveryBudget(config Config) time.Duration {
	return time.Duration(deliveryAttempts)*2*config.Timeout + time.Duration(deliveryAttempts-1)*retryDelay
}

// Store는 서비스가 저장소에서 빌려 쓰는 네 가지다. 사용자 명부는 새로 만들지
// 않는다 — 계정 id를 메일 주소로 바꾸는 조회 하나만 쓴다.
type Store interface {
	// MailSettings는 mail 문서와 복호화한 비밀번호를 한 스냅샷으로 돌려준다.
	MailSettings(ctx context.Context) (json.RawMessage, string, error)
	// UserEmails는 활성 사용자의 주소를 id별로 돌려준다. 주소가 없는 사용자는
	// 빠진다.
	UserEmails(ctx context.Context, userIDs []int64) (map[int64]string, error)
	RecordMailDelivery(ctx context.Context, delivery Delivery) (int64, error)
	FinishMailDelivery(ctx context.Context, id int64, status string, attempts int, errorMessage string) error
}

// Sender는 실제 전송이다. 테스트가 릴레이 없이 서비스를 몰 수 있도록 바꿔 끼운다.
type Sender func(ctx context.Context, config Config, message Message) error

type Service struct {
	store  Store
	logger *slog.Logger
	send   Sender
	now    func() time.Time
	// inflight는 배경 발송을 센다. 종료 시 잠깐 기다려 기록이 끊기지 않게 한다.
	inflight sync.WaitGroup
	// slots는 동시 발송 세마포어다. 자리 하나가 릴레이 연결 하나다.
	slots chan struct{}
}

func NewService(store Store, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: store, logger: logger, send: Deliver, now: func() time.Time { return time.Now().UTC() }, slots: make(chan struct{}, maxConcurrentDeliveries)}
}

// SetSender는 전송을 바꾼다(테스트용).
func (s *Service) SetSender(sender Sender) { s.send = sender }

// Config는 저장된 설정을 읽는다.
func (s *Service) Config(ctx context.Context) (Config, error) {
	raw, password, err := s.store.MailSettings(ctx)
	if err != nil {
		return Config{}, err
	}
	return ParseConfig(raw, password), nil
}

// Notify는 수신자를 주소로 바꾸고 배경에서 보낸다. 어떤 요청도 메일 서버를
// 기다리지 않는다. 자기가 한 일은 자기에게 보내지 않으며(actorUserID 제외),
// 주소가 없는 사용자는 조용히 건너뛴다. 꺼져 있거나 설정이 모자라면 아무것도
// 보내지 않고, 모자란 이유는 로그에 남긴다.
func (s *Service) Notify(ctx context.Context, notification Notification, actorUserID int64, recipientUserIDs []int64) {
	if s == nil {
		return
	}
	config, err := s.Config(ctx)
	if err != nil {
		s.logger.Warn("mail settings unreadable", "event", notification.Event, "error", err)
		return
	}
	if !config.Enabled || !config.Allows(notification.Event) {
		return
	}
	if err := config.Validate(); err != nil {
		s.logger.Warn("mail is enabled but not sendable", "event", notification.Event, "error", err)
		return
	}
	addresses := s.resolve(ctx, recipientUserIDs, actorUserID)
	if len(addresses) == 0 {
		return
	}
	body := notification.Render(config)
	var actor *int64
	if actorUserID > 0 {
		actor = &actorUserID
	}
	for _, address := range addresses {
		delivery := Delivery{Event: notification.Event, Recipient: address, Subject: notification.Subject, Reference: notification.Reference, ActorUserID: actor, Status: StatusQueued}
		// 기록은 여기서 바로 남긴다. 자리가 없어 발송이 기다리는 동안에도 관리
		// 화면에는 queued로 보인다.
		delivery.ID = s.record(ctx, delivery)
		s.inflight.Add(1)
		go func(delivery Delivery) {
			defer s.inflight.Done()
			s.slots <- struct{}{}
			defer func() { <-s.slots }()
			s.deliver(delivery, config, Message{To: address, Subject: notification.Subject, Body: body})
		}(delivery)
	}
}

// SendNow는 바로 보내고 결과를 돌려준다. 관리자의 시험 발송 단추가 쓴다.
func (s *Service) SendNow(ctx context.Context, notification Notification, actorUserID int64, recipient string) error {
	config, err := s.Config(ctx)
	if err != nil {
		return err
	}
	if !config.Enabled {
		return ErrDisabled
	}
	if err := config.Validate(); err != nil {
		return err
	}
	var actor *int64
	if actorUserID > 0 {
		actor = &actorUserID
	}
	delivery := Delivery{Event: notification.Event, Recipient: recipient, Subject: notification.Subject, Reference: notification.Reference, ActorUserID: actor, Status: StatusQueued}
	delivery.ID = s.record(ctx, delivery)
	delivery.Attempts = 1
	// 시도 하나의 최악은 접속 대기 + 세션 대기 = 2×Timeout이다(deliveryBudget 참고).
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*config.Timeout+5*time.Second)
	defer cancel()
	err = s.send(sendCtx, config, Message{To: recipient, Subject: notification.Subject, Body: notification.Render(config)})
	s.complete(delivery, err)
	return err
}

// Wait는 배경 발송이 끝날 때까지 최대 grace만큼 기다린다(종료 시).
func (s *Service) Wait(grace time.Duration) {
	done := make(chan struct{})
	go func() {
		s.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grace):
		s.logger.Warn("mail delivery shutdown wait timed out", "grace", grace)
	}
}

// deliver는 deliveryAttempts만큼 시도하고 결과를 기록한다. 예산은 최악의 경우
// (매 시도가 접속·세션 대기를 다 쓰는 릴레이)까지 덮는다.
func (s *Service) deliver(delivery Delivery, config Config, message Message) {
	ctx, cancel := context.WithTimeout(context.Background(), deliveryBudget(config))
	defer cancel()
	var err error
	for attempt := 1; attempt <= deliveryAttempts; attempt++ {
		delivery.Attempts = attempt
		if err = s.send(ctx, config, message); err == nil {
			break
		}
		if attempt < deliveryAttempts {
			select {
			case <-ctx.Done():
			case <-time.After(retryDelay):
			}
		}
	}
	s.complete(delivery, err)
}

// complete는 결과를 기록한다. 발송 컨텍스트를 물려받지 않는다 — 예산을 끝까지
// 쓴 발송의 기록이 같은 deadline으로 실패하면 행이 영원히 queued로 남는다.
func (s *Service) complete(delivery Delivery, cause error) {
	status, message := StatusSent, ""
	if cause != nil {
		status, message = StatusFailed, cause.Error()
		// 비밀번호는 오류 문자열에 들어가지 않는다(smtp 패키지는 응답 코드만
		// 돌려준다). 수신자와 이벤트만 남긴다.
		s.logger.Warn("notification mail failed", "event", delivery.Event, "recipient", delivery.Recipient, "attempts", delivery.Attempts, "error", cause)
	}
	if delivery.ID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), recordTimeout)
	defer cancel()
	if err := s.store.FinishMailDelivery(ctx, delivery.ID, status, delivery.Attempts, message); err != nil {
		s.logger.Warn("mail delivery status was not recorded", "id", delivery.ID, "error", err)
	}
}

func (s *Service) record(ctx context.Context, delivery Delivery) int64 {
	id, err := s.store.RecordMailDelivery(context.WithoutCancel(ctx), delivery)
	if err != nil {
		s.logger.Warn("mail delivery was not recorded", "event", delivery.Event, "error", err)
		return 0
	}
	return id
}

// resolve는 계정 id를 고유한 주소로 바꾼다. 행위자는 빼서 자기 일을 자기에게
// 알리지 않는다.
func (s *Service) resolve(ctx context.Context, recipientUserIDs []int64, actorUserID int64) []string {
	wanted := make([]int64, 0, len(recipientUserIDs))
	seenID := map[int64]struct{}{}
	for _, id := range recipientUserIDs {
		if id <= 0 || id == actorUserID {
			continue
		}
		if _, duplicate := seenID[id]; duplicate {
			continue
		}
		seenID[id] = struct{}{}
		wanted = append(wanted, id)
	}
	if len(wanted) == 0 {
		return nil
	}
	sort.Slice(wanted, func(i, j int) bool { return wanted[i] < wanted[j] })
	emails, err := s.store.UserEmails(ctx, wanted)
	if err != nil {
		s.logger.Warn("mail recipients were not resolved", "error", err)
		return nil
	}
	seen := map[string]struct{}{}
	addresses := make([]string, 0, len(wanted))
	for _, id := range wanted {
		address := strings.TrimSpace(emails[id])
		if !validAddress(address) {
			continue
		}
		key := strings.ToLower(address)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		addresses = append(addresses, address)
	}
	return addresses
}
