// Package mail은 사내 SMTP 릴레이로 이벤트 알림을 보낸다.
//
// 사내 릴레이는 포트 25·인증 없음·TLS 없음이 흔하므로 인증과 암호화는 선택
// 사항이고, 전송은 서버가 알리는 대로 맞춘다(auto). 여기 있는 어떤 것도
// 요청을 막지 않는다 — 발송은 배경에서 일어나고, 시도마다 기록이 남아
// 관리자가 무엇이 건물 밖으로 나갔는지 볼 수 있다.
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

var (
	ErrDisabled = errors.New("메일 알림이 꺼져 있습니다")
	ErrInvalid  = errors.New("메일 설정이 올바르지 않습니다")
)

// Message는 한 수신자에게 가는 한 통이다.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Deliver는 연결을 열어 한 통을 보낸다. 관리 화면의 시험 발송이 실제 설정으로
// 릴레이를 증명할 수 있도록 내보낸다.
func Deliver(ctx context.Context, config Config, message Message) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(message.To) == "" {
		return fmt.Errorf("%w: 수신자가 없습니다", ErrInvalid)
	}
	client, err := dial(ctx, config)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	if err := startSession(client, config); err != nil {
		return err
	}
	if err := client.Mail(strings.TrimSpace(config.FromAddress)); err != nil {
		return fmt.Errorf("MAIL FROM 실패: %w", err)
	}
	if err := client.Rcpt(strings.TrimSpace(message.To)); err != nil {
		return fmt.Errorf("RCPT TO 실패: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA 실패: %w", err)
	}
	if _, err := writer.Write([]byte(compose(config, message, time.Now()))); err != nil {
		return fmt.Errorf("본문 전송 실패: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("본문 종료 실패: %w", err)
	}
	return client.Quit()
}

func dial(ctx context.Context, config Config) (*smtp.Client, error) {
	dialer := &net.Dialer{Timeout: config.Timeout}
	var connection net.Conn
	var err error
	if config.Security == SecurityTLS {
		connection, err = tls.DialWithDialer(dialer, "tcp", config.endpoint(), config.tlsConfig())
		if err != nil {
			return nil, fmt.Errorf("SMTP TLS 연결 실패: %w", err)
		}
	} else {
		connection, err = dialer.DialContext(ctx, "tcp", config.endpoint())
		if err != nil {
			return nil, fmt.Errorf("SMTP 연결 실패: %w", err)
		}
	}
	// 릴레이가 응답을 멈춰도 발송 goroutine이 영원히 잡혀 있지 않게 한다.
	_ = connection.SetDeadline(time.Now().Add(config.Timeout))
	client, err := smtp.NewClient(connection, config.Host)
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("SMTP 세션 시작 실패: %w", err)
	}
	return client, nil
}

// startSession은 릴레이가 허용하는 만큼만 암호화하고 인증한다. 인증 없는 사내
// 릴레이와 둘 다 요구하는 호스팅 제공자가 같은 설정 항목으로 동작한다.
func startSession(client *smtp.Client, config Config) error {
	if err := client.Hello(helloName(config)); err != nil {
		return fmt.Errorf("EHLO 실패: %w", err)
	}
	if config.Security == SecurityStartTLS || config.Security == SecurityAuto {
		if supported, _ := client.Extension("STARTTLS"); supported {
			if err := client.StartTLS(config.tlsConfig()); err != nil {
				return fmt.Errorf("STARTTLS 실패: %w", err)
			}
		} else if config.Security == SecurityStartTLS {
			return fmt.Errorf("%w: 서버가 STARTTLS를 지원하지 않습니다", ErrInvalid)
		}
	}
	if strings.TrimSpace(config.Username) == "" {
		return nil
	}
	supported, mechanisms := client.Extension("AUTH")
	if !supported {
		return fmt.Errorf("%w: 서버가 인증을 지원하지 않습니다. 사용자 이름을 비우고 쓰세요", ErrInvalid)
	}
	upper := strings.ToUpper(mechanisms)
	switch {
	case strings.Contains(upper, "PLAIN"):
		return client.Auth(smtp.PlainAuth("", config.Username, config.Password, config.Host))
	case strings.Contains(upper, "LOGIN"):
		return client.Auth(loginAuth{username: config.Username, password: config.Password, host: config.Host})
	default:
		return client.Auth(smtp.CRAMMD5Auth(config.Username, config.Password))
	}
}

func (c Config) tlsConfig() *tls.Config {
	return &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.SkipTLSVerify} //nolint:gosec // 사설 인증서를 쓰는 사내 릴레이를 위한 명시적 선택
}

// helloName은 EHLO 이름을 보내는 주소의 도메인으로 둔다. 인사말을 검사하는
// 릴레이는 컨테이너 호스트 이름보다 이쪽을 잘 받아 준다.
func helloName(config Config) string {
	if index := strings.LastIndex(config.FromAddress, "@"); index >= 0 && index+1 < len(config.FromAddress) {
		return config.FromAddress[index+1:]
	}
	return "localhost"
}

// loginAuth는 여러 사내 릴레이가 PLAIN 대신 쓰는 LOGIN 방식이다. 표준
// 라이브러리는 PLAIN과 CRAM-MD5만 제공한다.
type loginAuth struct{ username, password, host string }

func (a loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS && server.Name != a.host {
		return "", nil, errors.New("LOGIN 인증은 신뢰할 수 있는 서버에서만 사용합니다")
	}
	return "LOGIN", nil, nil
}

func (a loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimRight(string(fromServer), ": ")) {
	case "username":
		return []byte(a.username), nil
	case "password":
		return []byte(a.password), nil
	}
	return nil, fmt.Errorf("알 수 없는 LOGIN 요청: %s", fromServer)
}

// compose는 MIME 메시지를 만든다. 한글 제목과 본문은 UTF-8 헤더 이전의
// 릴레이·클라이언트도 바르게 보여 주도록 인코딩한다.
func compose(config Config, message Message, now time.Time) string {
	var builder strings.Builder
	builder.WriteString("From: " + encodeAddress(config.Address()) + "\r\n")
	builder.WriteString("To: " + strings.TrimSpace(message.To) + "\r\n")
	builder.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", message.Subject) + "\r\n")
	builder.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	builder.WriteString("MIME-Version: 1.0\r\n")
	builder.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	builder.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	builder.WriteString("Auto-Submitted: auto-generated\r\n")
	builder.WriteString("X-Jupiq-Notification: 1\r\n")
	builder.WriteString("\r\n")
	builder.WriteString(normalizeBody(message.Body))
	return builder.String()
}

func encodeAddress(address string) string {
	open := strings.LastIndex(address, "<")
	if open <= 0 {
		return address
	}
	return mime.QEncoding.Encode("utf-8", strings.TrimSpace(address[:open])) + " " + address[open:]
}

// normalizeBody는 CRLF 줄 끝으로 맞춘다. 줄 머리의 점은 여기서 건드리지
// 않는다 — smtp.Client.Data가 돌려주는 DotWriter가 이미 이스케이프하므로 한 번
// 더 하면 수신자에게 점이 둘 보인다.
func normalizeBody(body string) string {
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	if !strings.HasSuffix(body, "\r\n") {
		body += "\r\n"
	}
	return body
}
