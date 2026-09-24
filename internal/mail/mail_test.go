package mail

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRelay는 최소한의 SMTP 서버다. 대화를 기록해 jupiq가 실제로 무엇을
// 말했는지(인증을 시도했는지 포함) 단언할 수 있게 한다.
type fakeRelay struct {
	address string
	// auth는 EHLO 응답에 광고할 AUTH 방식이다(예: "PLAIN LOGIN"). 비면 광고하지
	// 않는다. STARTTLS는 절대 광고하지 않으므로 인증은 언제나 평문 위에서 일어난다.
	auth     string
	mu       sync.Mutex
	commands []string
	body     string
	listener net.Listener
}

func startRelay(t *testing.T) *fakeRelay {
	t.Helper()
	return startRelayOn(t, "127.0.0.1", "")
}

// startRelayOn은 주어진 주소에 릴레이를 띄운다. host가 루프백이 아니면 모든
// 인터페이스에 묶고 config()가 그 주소로 접속하게 한다 — 평문 인증 정책은
// 릴레이 주소가 루프백인지에 달려 있기 때문이다.
func startRelayOn(t *testing.T, host, auth string) *fakeRelay {
	t.Helper()
	bind := host
	if !loopbackHost(host) {
		bind = "0.0.0.0"
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(bind, "0"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	relay := &fakeRelay{address: net.JoinHostPort(host, port), auth: auth, listener: listener}
	go relay.serve()
	t.Cleanup(func() { _ = listener.Close() })
	return relay
}

// nonLoopbackAddress는 이 기계의 루프백이 아닌 IPv4 주소 하나를 돌려준다. 심사가
// 재현한 결함(172.19.24.103의 평문 릴레이)은 루프백에서는 드러나지 않는다.
func nonLoopbackAddress(t *testing.T) string {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("interface addresses: %v", err)
	}
	for _, address := range addresses {
		if network, ok := address.(*net.IPNet); ok && network.IP.To4() != nil && !network.IP.IsLoopback() {
			return network.IP.String()
		}
	}
	t.Skip("this machine has no non-loopback IPv4 address")
	return ""
}

func (f *fakeRelay) config() Config {
	host, port, _ := net.SplitHostPort(f.address)
	number := 0
	_, _ = fmt.Sscanf(port, "%d", &number)
	config := Default()
	config.Enabled, config.Host, config.Port, config.FromAddress = true, host, number, "jupiq@relay.internal"
	config.Timeout = 3 * time.Second
	return config
}

func (f *fakeRelay) transcript() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.commands...)
}

func (f *fakeRelay) message() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.body
}

func (f *fakeRelay) serve() {
	for {
		connection, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handle(connection)
	}
}

func (f *fakeRelay) handle(connection net.Conn) {
	defer connection.Close()
	reader := bufio.NewReader(connection)
	write := func(line string) { _, _ = connection.Write([]byte(line + "\r\n")) }
	write("220 relay.internal ESMTP jupiq-test")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.TrimSpace(line)
		f.mu.Lock()
		f.commands = append(f.commands, command)
		f.mu.Unlock()
		switch {
		case strings.HasPrefix(strings.ToUpper(command), "EHLO"):
			write("250-relay.internal")
			if f.auth != "" {
				write("250-AUTH " + f.auth)
			}
			write("250 8BITMIME")
		case strings.HasPrefix(strings.ToUpper(command), "AUTH LOGIN"):
			// 자격증명은 base64로 되돌아오며 그대로 대화에 남는다 — 테스트가
			// 평문 연결에서 비밀번호가 실제로 릴레이에 닿았는지 확인하기 위해서다.
			for _, prompt := range []string{"VXNlcm5hbWU6", "UGFzc3dvcmQ6"} {
				write("334 " + prompt)
				answer, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				f.mu.Lock()
				f.commands = append(f.commands, strings.TrimSpace(answer))
				f.mu.Unlock()
			}
			write("235 Authentication succeeded")
		case strings.HasPrefix(strings.ToUpper(command), "AUTH "):
			write("235 Authentication succeeded")
		case strings.HasPrefix(strings.ToUpper(command), "MAIL FROM"), strings.HasPrefix(strings.ToUpper(command), "RCPT TO"):
			write("250 OK")
		case strings.ToUpper(command) == "DATA":
			write("354 End data with <CR><LF>.<CR><LF>")
			var body strings.Builder
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(line, "\r\n") == "." {
					break
				}
				body.WriteString(line)
			}
			f.mu.Lock()
			f.body = body.String()
			f.mu.Unlock()
			write("250 Queued")
		case strings.ToUpper(command) == "QUIT":
			write("221 Bye")
			return
		default:
			write("250 OK")
		}
	}
}

// fakeStore는 서비스가 저장소에서 빌려 쓰는 네 가지를 메모리로 흉내 낸다.
type fakeStore struct {
	mu         sync.Mutex
	settings   map[string]any
	password   string
	emails     map[int64]string
	deliveries []Delivery
	settingsFn func() error
}

func (f *fakeStore) MailSettings(context.Context) (json.RawMessage, string, error) {
	if f.settingsFn != nil {
		if err := f.settingsFn(); err != nil {
			return nil, "", err
		}
	}
	raw, _ := json.Marshal(f.settings)
	return raw, f.password, nil
}

func (f *fakeStore) UserEmails(_ context.Context, ids []int64) (map[int64]string, error) {
	result := map[int64]string{}
	for _, id := range ids {
		if email, ok := f.emails[id]; ok {
			result[id] = email
		}
	}
	return result, nil
}

func (f *fakeStore) RecordMailDelivery(_ context.Context, delivery Delivery) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delivery.ID = int64(len(f.deliveries) + 1)
	f.deliveries = append(f.deliveries, delivery)
	return delivery.ID, nil
}

func (f *fakeStore) FinishMailDelivery(ctx context.Context, id int64, status string, attempts int, errorMessage string) error {
	// 실제 저장소처럼 끝난 컨텍스트로는 쓰지 않는다. 발송 예산을 다 쓴 뒤의
	// 기록이 같은 컨텍스트를 물려받으면 행이 영원히 queued로 남는 결함을 잡는다.
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for index := range f.deliveries {
		if f.deliveries[index].ID == id {
			f.deliveries[index].Status, f.deliveries[index].Attempts, f.deliveries[index].ErrorMessage = status, attempts, errorMessage
		}
	}
	return nil
}

func (f *fakeStore) snapshot() []Delivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Delivery(nil), f.deliveries...)
}

func (f *fakeStore) waitSettled(t *testing.T, want int) []Delivery {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		items := f.snapshot()
		settled := 0
		for _, item := range items {
			if item.Status != StatusQueued {
				settled++
			}
		}
		if len(items) == want && settled == want {
			return items
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("deliveries did not settle: %+v", f.snapshot())
	return nil
}

func relaySettings(relay *fakeRelay) map[string]any {
	config := relay.config()
	return map[string]any{"enabled": true, "smtp_host": config.Host, "smtp_port": config.Port, "from_address": config.FromAddress, "timeout_seconds": 3, "base_url": "https://jupiq.internal"}
}

func TestDeliverSpeaksPlainSMTPWithoutAuthByDefault(t *testing.T) {
	relay := startRelay(t)
	config := relay.config()
	config.FromName = "jupiq 알림"
	err := Deliver(context.Background(), config, Message{To: "ops@corp.internal", Subject: "승인 요청", Body: "첫 줄\n.점으로 시작하는 줄\n끝"})
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	transcript := strings.Join(relay.transcript(), "\n")
	for _, want := range []string{"EHLO relay.internal", "MAIL FROM:<jupiq@relay.internal>", "RCPT TO:<ops@corp.internal>", "DATA", "QUIT"} {
		if !strings.Contains(transcript, want) {
			t.Errorf("transcript lacks %q:\n%s", want, transcript)
		}
	}
	if strings.Contains(transcript, "AUTH") || strings.Contains(transcript, "STARTTLS") {
		t.Errorf("relay without AUTH/STARTTLS must not be asked for them:\n%s", transcript)
	}
	body := relay.message()
	for _, want := range []string{"Subject: =?utf-8?q?", "From: =?utf-8?q?", "<jupiq@relay.internal>", "Auto-Submitted: auto-generated", "\r\n..점으로 시작하는 줄\r\n", "X-Jupiq-Notification: 1"} {
		if !strings.Contains(body, want) {
			t.Errorf("message lacks %q:\n%s", want, body)
		}
	}
}

// 심사가 재현한 결함: 루프백이 아닌 주소의 릴레이가 STARTTLS 없이 AUTH만
// 광고하면 비밀번호가 base64 평문으로 릴레이에 닿았다(LOGIN). PLAIN이 함께
// 광고되면 표준 PlainAuth가 거부해 발송 전체가 깨졌다. 정책: 암호화되지 않은
// 연결로는 루프백 릴레이가 아닌 한 자격증명을 보내지 않고, 이유를 설정 오류로
// 돌려준다.
func TestDeliverRefusesCredentialsOnPlaintextConnectionToNonLoopbackRelay(t *testing.T) {
	host := nonLoopbackAddress(t)
	secret := "test-password"
	for _, advertised := range []string{"LOGIN", "PLAIN LOGIN"} {
		relay := startRelayOn(t, host, advertised)
		config := relay.config()
		config.Username, config.Password = "jupiq", secret
		err := Deliver(context.Background(), config, Message{To: "ops@corp.internal", Subject: "승인 요청", Body: "본문"})
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "암호화") {
			t.Fatalf("AUTH %s over plaintext to %s: want a configuration error naming encryption, got %v", advertised, host, err)
		}
		transcript := strings.Join(relay.transcript(), "\n")
		if strings.Contains(transcript, "AUTH") || strings.Contains(transcript, base64.StdEncoding.EncodeToString([]byte(secret))) {
			t.Fatalf("AUTH %s: credentials reached the relay over plaintext:\n%s", advertised, transcript)
		}
		if !strings.Contains(transcript, "EHLO") || strings.Contains(transcript, "MAIL FROM") {
			t.Fatalf("AUTH %s: session must stop after EHLO without sending mail:\n%s", advertised, transcript)
		}
	}
}

// 루프백 릴레이(같은 기계의 postfix 등)는 표준 라이브러리의 PLAIN과 같은 규칙으로
// 평문 인증을 허용한다. LOGIN 경로도 같은 규칙을 따르고 실제로 인증한다.
func TestDeliverAuthenticatesOverPlaintextToLoopbackRelay(t *testing.T) {
	secret := "test-password"
	for advertised, wantCommand := range map[string]string{"LOGIN": "AUTH LOGIN", "PLAIN": "AUTH PLAIN "} {
		relay := startRelayOn(t, "127.0.0.1", advertised)
		config := relay.config()
		config.Username, config.Password = "jupiq", secret
		if err := Deliver(context.Background(), config, Message{To: "ops@corp.internal", Subject: "승인 요청", Body: "본문"}); err != nil {
			t.Fatalf("AUTH %s on loopback: %v", advertised, err)
		}
		transcript := strings.Join(relay.transcript(), "\n")
		if !strings.Contains(transcript, wantCommand) || !strings.Contains(transcript, "MAIL FROM") || !strings.Contains(transcript, "QUIT") {
			t.Fatalf("AUTH %s on loopback: expected %q followed by a full send:\n%s", advertised, wantCommand, transcript)
		}
		if advertised == "LOGIN" && !strings.Contains(transcript, base64.StdEncoding.EncodeToString([]byte(secret))) {
			t.Fatalf("LOGIN did not answer the password prompt:\n%s", transcript)
		}
	}
}

func TestValidateRejectsCredentialsThatCouldOnlyTravelInPlaintext(t *testing.T) {
	config := Default()
	config.Host, config.FromAddress, config.Username, config.Password = "relay.internal", "jupiq@corp.internal", "jupiq", "test-password"
	config.Security = SecurityNone
	if err := config.Validate(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "mail.security") {
		t.Fatalf("security=none with a username on a remote relay must be rejected at save time: %v", err)
	}
	for _, security := range []string{SecurityAuto, SecurityStartTLS, SecurityTLS} {
		config.Security = security
		if err := config.Validate(); err != nil {
			t.Fatalf("security=%s with a username must be accepted (the session decides): %v", security, err)
		}
	}
	config.Security, config.Host = SecurityNone, "127.0.0.1"
	if err := config.Validate(); err != nil {
		t.Fatalf("security=none on a loopback relay must be accepted: %v", err)
	}
	for host, want := range map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true, "relay.internal": false, "172.19.24.103": false, "127.0.0.2": false} {
		if got := loopbackHost(host); got != want {
			t.Errorf("loopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestNotifyDoesNothingWhenDisabled(t *testing.T) {
	relay := startRelay(t)
	settings := relaySettings(relay)
	settings["enabled"] = false
	store := &fakeStore{settings: settings, emails: map[int64]string{2: "ops@corp.internal"}}
	service := NewService(store, nil)
	service.Notify(context.Background(), TestMessage(), 1, []int64{2})
	time.Sleep(50 * time.Millisecond)
	if len(store.snapshot()) != 0 || len(relay.transcript()) != 0 {
		t.Fatalf("disabled mail sent something: %+v %v", store.snapshot(), relay.transcript())
	}
}

func TestNotifySkipsIncompleteConfigurationEvenWhenEnabled(t *testing.T) {
	store := &fakeStore{settings: map[string]any{"enabled": true, "from_address": "jupiq@corp.internal"}, emails: map[int64]string{2: "ops@corp.internal"}}
	sent := 0
	service := NewService(store, nil)
	service.SetSender(func(context.Context, Config, Message) error { sent++; return nil })
	service.Notify(context.Background(), TestMessage(), 1, []int64{2})
	if sent != 0 || len(store.snapshot()) != 0 {
		t.Fatalf("mail without smtp_host was attempted: sent=%d deliveries=%+v", sent, store.snapshot())
	}
	if err := ParseConfig(json.RawMessage(`{"enabled":true,"from_address":"jupiq@corp.internal"}`), "").Validate(); err == nil || !strings.Contains(err.Error(), "smtp_host") {
		t.Fatalf("missing host must be reported by name: %v", err)
	}
}

func TestNotifyReturnsImmediatelyAndRecordsFailureWhenRelayIsDown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	host, port, _ := net.SplitHostPort(address)
	number := 0
	_, _ = fmt.Sscanf(port, "%d", &number)
	store := &fakeStore{settings: map[string]any{"enabled": true, "smtp_host": host, "smtp_port": number, "from_address": "jupiq@corp.internal", "timeout_seconds": 1}, emails: map[int64]string{2: "ops@corp.internal"}}
	service := NewService(store, nil)
	started := time.Now()
	service.Notify(context.Background(), HubHealth(3, "lab", false, "dial tcp: connection refused"), 0, []int64{2})
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Notify blocked the caller for %s", elapsed)
	}
	items := store.waitSettled(t, 1)
	if items[0].Status != StatusFailed || items[0].Attempts != 2 || !strings.Contains(items[0].ErrorMessage, "SMTP 연결 실패") {
		t.Fatalf("failure was not recorded with both attempts: %+v", items[0])
	}
	if items[0].Recipient != "ops@corp.internal" || items[0].Event != EventHubHealth || items[0].Reference != "hub:3" {
		t.Fatalf("delivery record is incomplete: %+v", items[0])
	}
}

// TCP는 받되 인사말을 보내지 않는 릴레이. 시도 하나가 접속 대기와 세션 대기를
// 모두 쓰는 가장 느린 경우라 예산 계산이 맞는지 여기서 드러난다.
func TestNotifyRecordsFailureWhenRelayAcceptsButNeverGreets(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			defer connection.Close()
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	number := 0
	_, _ = fmt.Sscanf(port, "%d", &number)
	store := &fakeStore{settings: map[string]any{"enabled": true, "smtp_host": host, "smtp_port": number, "from_address": "jupiq@corp.internal", "timeout_seconds": 1}, emails: map[int64]string{2: "ops@corp.internal"}}
	service := NewService(store, nil)
	started := time.Now()
	service.Notify(context.Background(), HubHealth(3, "lab", false, "no greeting"), 0, []int64{2})
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Notify blocked the caller for %s", elapsed)
	}
	items := store.waitSettled(t, 1)
	if items[0].Status != StatusFailed || items[0].Attempts != 2 || !strings.Contains(items[0].ErrorMessage, "SMTP 세션 시작 실패") {
		t.Fatalf("silent relay must be recorded as failed after both attempts: %+v", items[0])
	}
}

// 발송이 예산을 끝까지 쓰더라도 결과는 기록돼야 한다. 기록이 발송과 같은
// 컨텍스트를 쓰면 이 경우 UPDATE가 deadline으로 실패해 행이 queued로 남는다.
func TestDeliverRecordsOutcomeAfterTheSendBudgetIsExhausted(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store, nil)
	attempts := 0
	service.SetSender(func(ctx context.Context, _ Config, _ Message) error {
		attempts++
		<-ctx.Done()
		return ctx.Err()
	})
	config := Default()
	config.Host, config.FromAddress, config.Timeout = "relay.internal", "jupiq@corp.internal", 100*time.Millisecond
	delivery := Delivery{Event: EventTest, Recipient: "ops@corp.internal", Subject: "예산", Status: StatusQueued}
	delivery.ID, _ = store.RecordMailDelivery(context.Background(), delivery)
	started := time.Now()
	service.deliver(delivery, config, Message{To: delivery.Recipient})
	if budget := deliveryBudget(config); time.Since(started) > budget+time.Second {
		t.Fatalf("delivery overran its budget of %s: %s", budget, time.Since(started))
	}
	items := store.snapshot()
	if attempts != 2 || items[0].Status != StatusFailed || items[0].Attempts != 2 || !strings.Contains(items[0].ErrorMessage, "deadline") {
		t.Fatalf("outcome was not recorded after the budget ran out: attempts=%d %+v", attempts, items[0])
	}
	if want := 2*(2*config.Timeout) + retryDelay; deliveryBudget(config) != want {
		t.Fatalf("budget must cover dial+session per attempt plus the retry wait: got %s want %s", deliveryBudget(config), want)
	}
}

// 승인 요청 한 건이 승인자 N명에게 N개의 연결을 동시에 열지 않는다. 상한을 넘는
// 수신자는 기록만 먼저 남고 자리가 나면 보낸다.
func TestNotifyBoundsConcurrentDeliveries(t *testing.T) {
	emails := map[int64]string{}
	ids := []int64{}
	for id := int64(2); id < 2+3*maxConcurrentDeliveries; id++ {
		emails[id] = fmt.Sprintf("user%d@corp.internal", id)
		ids = append(ids, id)
	}
	store := &fakeStore{settings: map[string]any{"enabled": true, "smtp_host": "relay.internal", "from_address": "jupiq@corp.internal"}, emails: emails}
	service := NewService(store, nil)
	var mu sync.Mutex
	active, peak, total := 0, 0, 0
	service.SetSender(func(context.Context, Config, Message) error {
		mu.Lock()
		active++
		total++
		if active > peak {
			peak = active
		}
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return nil
	})
	service.Notify(context.Background(), ApprovalRequested(9, "서버 7 start 요청", "actor", "approve", ""), 1, ids)
	if queued := store.snapshot(); len(queued) != len(ids) {
		t.Fatalf("every recipient must be recorded before Notify returns: %d of %d", len(queued), len(ids))
	}
	items := store.waitSettled(t, len(ids))
	mu.Lock()
	defer mu.Unlock()
	if peak > maxConcurrentDeliveries || total != len(ids) {
		t.Fatalf("peak concurrency %d exceeds %d (total sent %d)", peak, maxConcurrentDeliveries, total)
	}
	for _, item := range items {
		if item.Status != StatusSent {
			t.Fatalf("waiting recipient was not sent: %+v", item)
		}
	}
}

func TestNotifyNeverMailsTheActorAndDeduplicatesAddresses(t *testing.T) {
	relay := startRelay(t)
	store := &fakeStore{settings: relaySettings(relay), emails: map[int64]string{1: "actor@corp.internal", 2: "ops@corp.internal", 3: "OPS@corp.internal", 4: ""}}
	service := NewService(store, nil)
	service.Notify(context.Background(), ApprovalRequested(9, "서버 7 start 요청", "actor", "approve", ""), 1, []int64{1, 2, 3, 4, 2})
	items := store.waitSettled(t, 1)
	if items[0].Recipient != "ops@corp.internal" || items[0].Status != StatusSent || items[0].Attempts != 1 {
		t.Fatalf("expected one sent mail to ops only: %+v", items)
	}
	if actor := items[0].ActorUserID; actor == nil || *actor != 1 {
		t.Fatalf("actor was not recorded: %+v", items[0])
	}
	body := relay.message()
	if !strings.Contains(body, "https://jupiq.internal/approvals") {
		t.Fatalf("mail lacks the approvals link:\n%s", body)
	}
}

func TestEventSwitchStopsOnlyThatEvent(t *testing.T) {
	store := &fakeStore{settings: map[string]any{"enabled": true, "smtp_host": "relay.internal", "from_address": "jupiq@corp.internal", "notify_hub_health": false}, emails: map[int64]string{2: "ops@corp.internal"}}
	var mu sync.Mutex
	events := []string{}
	service := NewService(store, nil)
	service.SetSender(func(_ context.Context, _ Config, message Message) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, message.Subject)
		return nil
	})
	service.Notify(context.Background(), HubHealth(1, "lab", false, "boom"), 0, []int64{2})
	service.Notify(context.Background(), ApprovalDecided(4, "서버 1 stop 요청", "admin", "approved", ""), 0, []int64{2})
	items := store.waitSettled(t, 1)
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || !strings.Contains(events[0], "승인됨") || items[0].Event != EventApprovalDecided {
		t.Fatalf("hub health switch did not isolate its event: events=%v deliveries=%+v", events, items)
	}
}

func TestSendNowRecordsSuccessAndFailure(t *testing.T) {
	relay := startRelay(t)
	store := &fakeStore{settings: relaySettings(relay)}
	service := NewService(store, nil)
	if err := service.SendNow(context.Background(), TestMessage(), 1, "admin@corp.internal"); err != nil {
		t.Fatalf("test mail failed: %v", err)
	}
	service.SetSender(func(context.Context, Config, Message) error { return errors.New("550 relay denied") })
	if err := service.SendNow(context.Background(), TestMessage(), 1, "admin@corp.internal"); err == nil {
		t.Fatal("failed send returned nil")
	}
	items := store.snapshot()
	if len(items) != 2 || items[0].Status != StatusSent || items[1].Status != StatusFailed || items[1].ErrorMessage != "550 relay denied" {
		t.Fatalf("both outcomes must be recorded: %+v", items)
	}
	for _, item := range items {
		if item.Event != EventTest || item.Subject == "" || item.Recipient != "admin@corp.internal" {
			t.Fatalf("record lacks event/subject/recipient: %+v", item)
		}
	}
	store.settings["enabled"] = false
	if err := service.SendNow(context.Background(), TestMessage(), 1, "admin@corp.internal"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled mail must refuse the test send: %v", err)
	}
}

func TestParseConfigDefaultsMatchAnInternalRelay(t *testing.T) {
	config := ParseConfig(nil, "")
	if config.Enabled || config.Port != 25 || config.Security != SecurityAuto || config.Timeout != 10*time.Second || config.SkipTLSVerify || config.Username != "" {
		t.Fatalf("defaults are not port 25 / no auth / auto security / off: %+v", config)
	}
	if !config.Allows(EventApprovalRequested) || !config.Allows(EventTest) {
		t.Fatal("events without an explicit switch must be allowed")
	}
	implicit := ParseConfig(json.RawMessage(`{"smtp_port":465,"security":"AUTO","notify_key_expiry":false}`), "s3cret")
	if implicit.Security != SecurityTLS || implicit.Password != "s3cret" || implicit.Allows(EventKeyExpiring) {
		t.Fatalf("465 must imply tls, password must flow from the secret, switch must be honoured: %+v", implicit)
	}
	if err := (Config{Host: "relay", Port: 25, FromAddress: "jupiq@corp", Security: "ssl", Timeout: time.Second}).Validate(); err == nil {
		t.Fatal("unknown security mode was accepted")
	}
}

func TestKeysExpiringBundlesOneMailPerOwner(t *testing.T) {
	now := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	notification := KeysExpiring([]ExpiringKey{{ID: 1, Name: "ci", Prefix: "jqk_abc", ExpiresAt: now.Add(3 * 24 * time.Hour)}, {ID: 2, Name: "notebook", Prefix: "jqk_def", ExpiresAt: now.Add(6 * 24 * time.Hour)}}, now)
	body := notification.Render(Config{})
	if !strings.Contains(body, "- ci (jqk_abc…) — 2026-09-19 00:00 UTC 만료, 3일 남음") || !strings.Contains(body, "- notebook (jqk_def…)") {
		t.Fatalf("bundle lacks key lines:\n%s", body)
	}
	if notification.Reference != "api_key:1,api_key:2" || strings.Contains(body, "바로 열기") {
		t.Fatalf("reference or link is wrong: %q\n%s", notification.Reference, body)
	}
}

func TestHubHealthChangeFiresOnlyOnTransitions(t *testing.T) {
	cases := []struct {
		previous string
		success  bool
		want     bool
		healthy  bool
	}{
		{"healthy", false, true, false},
		{"unknown", false, true, false},
		{"degraded", false, false, false},
		{"degraded", true, true, true},
		{"healthy", true, false, false},
		{"unknown", true, false, false},
	}
	for _, tc := range cases {
		notification, changed := HubHealthChange(1, "lab", tc.previous, tc.success, "boom")
		if changed != tc.want {
			t.Errorf("previous=%s success=%v: changed=%v want %v", tc.previous, tc.success, changed, tc.want)
		}
		if changed && tc.healthy != strings.Contains(notification.Subject, "복구") {
			t.Errorf("previous=%s success=%v: unexpected subject %q", tc.previous, tc.success, notification.Subject)
		}
	}
}
