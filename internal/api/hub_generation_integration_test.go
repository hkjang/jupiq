package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/auth"
	"github.com/hkjang/jupiq/internal/secure"
	"github.com/hkjang/jupiq/internal/store"
)

func TestHubTestRejectsResponseAfterTargetReplacementIntegration(t *testing.T) {
	dsn := os.Getenv("JUPIQ_INTEGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("set JUPIQ_INTEGRATION_TEST_DSN to run PostgreSQL integration coverage")
	}
	ip := nonLoopbackIPv4(t)
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	usersStarted := make(chan struct{})
	releaseUsers := make(chan struct{})
	var signalOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseUsers) })
	remote := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token token-a" {
			http.Error(w, "unexpected token", http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/hub/api/info"):
			_, _ = w.Write([]byte(`{"version":"A-5.3"}`))
		case strings.HasSuffix(r.URL.Path, "/hub/api/users"):
			signalOnce.Do(func() { close(usersStarted) })
			select {
			case <-releaseUsers:
				_, _ = w.Write([]byte(`[]`))
			case <-r.Context().Done():
			}
		default:
			http.NotFound(w, r)
		}
	}))
	remote.Listener = listener
	remote.Start()
	defer remote.Close()
	baseURL := fmt.Sprintf("http://%s:%d", ip, listener.Addr().(*net.TCPAddr).Port)

	cipher, err := secure.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(context.Background(), dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	marker := fmt.Sprintf("hub-test-generation-%d", time.Now().UnixNano())
	verifyTLS := false
	hub, err := database.CreateHub(context.Background(), store.HubWrite{
		Name: marker, Network: "test", BaseURL: baseURL, VerifyTLS: &verifyTLS, APIToken: "token-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.DeleteHub(context.Background(), hub.ID) }()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/hubs/"+strconv.FormatInt(hub.ID, 10)+"/test", nil)
	request.SetPathValue("id", strconv.FormatInt(hub.ID, 10))
	request = withPrincipal(request, auth.Principal{UserPermissions: []string{"hubs:write"}})
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&Server{Store: database}).hubTest(response, request)
	}()

	select {
	case <-usersStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("Hub test did not reach the blocked endpoint A users check")
	}
	if _, err := database.UpdateHub(context.Background(), hub.ID, store.HubWrite{
		BaseURL: baseURL + "/replacement", APIToken: "token-b",
	}); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(releaseUsers) })
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Hub test did not finish after releasing endpoint A")
	}
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"hub_configuration_changed"`) {
		t.Fatalf("stale endpoint A test was reported as success: status=%d body=%s", response.Code, response.Body.String())
	}
}
