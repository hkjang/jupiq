package mail

import (
	"bufio"
	"context"
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
	address  string
	mu       sync.Mutex
	commands []string
	body     string
	listener net.Listener
}

func startRelay(t *testing.T) *fakeRelay {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	relay := &fakeRelay{address: listener.Addr().String(), listener: listener}
	go relay.serve()
	t.Cleanup(func() { _ = listener.Close() })
	return relay
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
			write("250 8BITMIME")
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

func (f *fakeStore) FinishMailDelivery(_ context.Context, id int64, status string, attempts int, errorMessage string) error {
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
