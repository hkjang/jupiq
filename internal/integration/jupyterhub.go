package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type JupyterHubClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

type HubInfo struct {
	Version string `json:"version"`
}

type JupyterUser struct {
	Name         string                     `json:"name"`
	Admin        bool                       `json:"admin"`
	Pending      *string                    `json:"pending"`
	LastActivity *time.Time                 `json:"last_activity"`
	Server       string                     `json:"server"`
	Servers      map[string]json.RawMessage `json:"servers"`
	Roles        []string                   `json:"roles"`
	Groups       []string                   `json:"groups"`
	Raw          json.RawMessage            `json:"-"`
}

func NewJupyterHub(baseURL, token string, verifyTLS bool) (*JupyterHubClient, error) {
	if _, err := ValidateEndpoint(baseURL); err != nil {
		return nil, err
	}
	return &JupyterHubClient{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: newHTTPClient(HTTPOptions{VerifyTLS: verifyTLS, Timeout: 12 * time.Second})}, nil
}

func (c *JupyterHubClient) endpoint(path string) (string, error) {
	return joinURL(c.BaseURL, "hub/api/"+strings.TrimLeft(path, "/"))
}

func (c *JupyterHubClient) Info(ctx context.Context) (HubInfo, error) {
	endpoint, err := c.endpoint("info")
	if err != nil {
		return HubInfo{}, err
	}
	var info HubInfo
	_, _, err = doJSONWithAuthScheme(ctx, c.HTTP, http.MethodGet, endpoint, c.Token, "token", nil, &info, true)
	return info, err
}

func (c *JupyterHubClient) Users(ctx context.Context) ([]JupyterUser, error) {
	endpoint, err := c.endpoint("users")
	if err != nil {
		return nil, err
	}
	var raw []json.RawMessage
	if _, _, err := doJSONWithAuthScheme(ctx, c.HTTP, http.MethodGet, endpoint, c.Token, "token", nil, &raw, true); err != nil {
		return nil, err
	}
	users := make([]JupyterUser, 0, len(raw))
	for index, item := range raw {
		var user JupyterUser
		if err := json.Unmarshal(item, &user); err != nil {
			return nil, fmt.Errorf("JupyterHub 사용자 응답 %d 해석 실패: %w", index+1, err)
		}
		user.Raw = item
		users = append(users, user)
	}
	return users, nil
}

func (c *JupyterHubClient) serverEndpoint(username, serverName string) (string, error) {
	path := "users/" + url.PathEscape(username) + "/server"
	if serverName != "" {
		path = "users/" + url.PathEscape(username) + "/servers/" + url.PathEscape(serverName)
	}
	return c.endpoint(path)
}

func (c *JupyterHubClient) ServerAction(ctx context.Context, username, serverName, action string) error {
	endpoint, err := c.serverEndpoint(username, serverName)
	if err != nil {
		return err
	}
	switch action {
	case "start":
		_, _, err = doJSONWithAuthScheme(ctx, c.HTTP, http.MethodPost, endpoint, c.Token, "token", bytes.NewReader([]byte(`{}`)), nil, false)
	case "stop":
		_, _, err = doJSONWithAuthScheme(ctx, c.HTTP, http.MethodDelete, endpoint, c.Token, "token", nil, nil, false)
	case "restart":
		if _, _, err = doJSONWithAuthScheme(ctx, c.HTTP, http.MethodDelete, endpoint, c.Token, "token", nil, nil, false); err == nil {
			err = c.waitServerStopped(ctx, username, serverName)
			if err == nil {
				_, _, err = doJSONWithAuthScheme(ctx, c.HTTP, http.MethodPost, endpoint, c.Token, "token", bytes.NewReader([]byte(`{}`)), nil, false)
			}
		}
	default:
		err = fmt.Errorf("unsupported server action %q", action)
	}
	return err
}

func (c *JupyterHubClient) waitServerStopped(ctx context.Context, username, serverName string) error {
	userEndpoint, err := c.endpoint("users/" + url.PathEscape(username))
	if err != nil {
		return err
	}
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		var user JupyterUser
		status, _, requestErr := doJSONWithAuthScheme(ctx, c.HTTP, http.MethodGet, userEndpoint, c.Token, "token", nil, &user, false)
		if status == http.StatusNotFound {
			return nil
		}
		if requestErr != nil {
			return fmt.Errorf("서버 종료 상태 확인 실패: %w", requestErr)
		}
		running := false
		if serverName == "" {
			running = user.Server != ""
			if _, exists := user.Servers[""]; exists {
				running = true
			}
		} else {
			_, running = user.Servers[serverName]
		}
		if !running {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("서버 종료 대기 시간 초과: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
