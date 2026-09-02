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
	_, _, err = doJSON(ctx, c.HTTP, http.MethodGet, endpoint, c.Token, nil, &info, true)
	return info, err
}

func (c *JupyterHubClient) Users(ctx context.Context) ([]JupyterUser, error) {
	endpoint, err := c.endpoint("users")
	if err != nil {
		return nil, err
	}
	var raw []json.RawMessage
	if _, _, err := doJSON(ctx, c.HTTP, http.MethodGet, endpoint, c.Token, nil, &raw, true); err != nil {
		return nil, err
	}
	users := make([]JupyterUser, 0, len(raw))
	for _, item := range raw {
		var user JupyterUser
		if err := json.Unmarshal(item, &user); err != nil {
			continue
		}
		user.Raw = item
		users = append(users, user)
	}
	return users, nil
}

func (c *JupyterHubClient) ServerAction(ctx context.Context, username, serverName, action string) error {
	path := "users/" + url.PathEscape(username) + "/server"
	if serverName != "" {
		path = "users/" + url.PathEscape(username) + "/servers/" + url.PathEscape(serverName)
	}
	endpoint, err := c.endpoint(path)
	if err != nil {
		return err
	}
	switch action {
	case "start":
		_, _, err = doJSON(ctx, c.HTTP, http.MethodPost, endpoint, c.Token, bytes.NewReader([]byte(`{}`)), nil, false)
	case "stop":
		_, _, err = doJSON(ctx, c.HTTP, http.MethodDelete, endpoint, c.Token, nil, nil, false)
	case "restart":
		if _, _, err = doJSON(ctx, c.HTTP, http.MethodDelete, endpoint, c.Token, nil, nil, false); err == nil {
			_, _, err = doJSON(ctx, c.HTTP, http.MethodPost, endpoint, c.Token, bytes.NewReader([]byte(`{}`)), nil, false)
		}
	default:
		err = fmt.Errorf("unsupported server action %q", action)
	}
	return err
}
