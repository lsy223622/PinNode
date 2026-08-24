package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

type AuthKey struct {
	Secret string
	ID     string
}

type Device struct {
	ID          string    `json:"id"`
	NodeID      string    `json:"nodeId"`
	Hostname    string    `json:"hostname"`
	Created     time.Time `json:"created"`
	Tags        []string  `json:"tags"`
	IsEphemeral bool      `json:"isEphemeral"`
}

// TailscaleAPI 是服务端需要的最小控制面接口，便于不接触真实 tailnet 地测试清理逻辑。
type TailscaleAPI interface {
	CreateAuthKey(context.Context, time.Duration, bool) (AuthKey, error)
	DeleteAuthKey(context.Context, string) error
	GetDevice(context.Context, string) (Device, error)
	SetDeviceRoutes(context.Context, string, []string) error
	DeleteDevice(context.Context, string) error
}

func (c *TailscaleClient) DeleteAuthKey(ctx context.Context, keyID string) error {
	if keyID == "" {
		return nil
	}
	err := c.doJSON(ctx, http.MethodDelete, c.tailnetURL("keys", keyID), nil, nil)
	var apiErr *HTTPError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

type TailscaleClient struct {
	baseURL      string
	tailnet      string
	clientID     string
	clientSecret string
	httpClient   *http.Client
	userAgent    string

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

func NewTailscaleClient(config Config) *TailscaleClient {
	return &TailscaleClient{
		baseURL:      strings.TrimRight(config.TailscaleBaseURL, "/"),
		tailnet:      config.TailscaleTailnet,
		clientID:     config.OAuthClientID,
		clientSecret: config.OAuthClientSecret,
		httpClient:   &http.Client{Timeout: 15 * time.Second},
		userAgent:    "PinNode/0.1",
	}
}

func (c *TailscaleClient) CreateAuthKey(ctx context.Context, expiry time.Duration, ephemeral bool) (AuthKey, error) {
	seconds := int64(expiry / time.Second)
	if seconds < 1 {
		return AuthKey{}, errors.New("auth key expiry 必须至少为 1 秒")
	}
	payload := map[string]any{
		"capabilities": map[string]any{
			"devices": map[string]any{
				"create": map[string]any{
					"reusable":      false,
					"ephemeral":     ephemeral,
					"preauthorized": true,
					"tags":          []string{"tag:rescue-gateway"},
				},
			},
		},
		"expirySeconds": seconds,
	}
	var response struct {
		Key string `json:"key"`
		ID  string `json:"id"`
	}
	if err := c.doJSON(ctx, http.MethodPost, c.tailnetURL("keys"), payload, &response); err != nil {
		return AuthKey{}, err
	}
	if response.Key == "" || response.ID == "" {
		return AuthKey{}, errors.New("Tailscale API 未返回完整 auth key")
	}
	return AuthKey{Secret: response.Key, ID: response.ID}, nil
}

func (c *TailscaleClient) GetDevice(ctx context.Context, deviceID string) (Device, error) {
	var response Device
	err := c.doJSON(ctx, http.MethodGet, c.apiURL("device", deviceID), nil, &response)
	return response, err
}

func (c *TailscaleClient) SetDeviceRoutes(ctx context.Context, deviceID string, routes []string) error {
	payload := map[string][]string{"routes": routes}
	return c.doJSON(ctx, http.MethodPost, c.apiURL("device", deviceID, "routes"), payload, nil)
}

func (c *TailscaleClient) DeleteDevice(ctx context.Context, deviceID string) error {
	err := c.doJSON(ctx, http.MethodDelete, c.apiURL("device", deviceID), nil, nil)
	var apiErr *HTTPError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

func (c *TailscaleClient) token(ctx context.Context) (string, error) {
	now := time.Now()
	c.mu.Lock()
	if c.accessToken != "" && now.Before(c.tokenExpiry) {
		token := c.accessToken
		c.mu.Unlock()
		return token, nil
	}
	c.mu.Unlock()

	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.clientID, c.clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 Tailscale OAuth token 失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return "", readHTTPError(response)
	}
	var tokenResponse struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&tokenResponse); err != nil {
		return "", fmt.Errorf("解析 Tailscale OAuth token 失败: %w", err)
	}
	if tokenResponse.AccessToken == "" {
		return "", errors.New("Tailscale OAuth 响应缺少 access_token")
	}
	expiresIn := time.Duration(tokenResponse.ExpiresIn) * time.Second
	if expiresIn <= 0 || expiresIn > time.Hour {
		expiresIn = time.Hour
	}
	c.mu.Lock()
	c.accessToken = tokenResponse.AccessToken
	c.tokenExpiry = time.Now().Add(expiresIn - time.Minute)
	c.mu.Unlock()
	return tokenResponse.AccessToken, nil
}

func (c *TailscaleClient) doJSON(ctx context.Context, method, endpoint string, payload any, result any) error {
	token, err := c.token(ctx)
	if err != nil {
		return err
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("编码 Tailscale 请求失败: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", c.userAgent)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("请求 Tailscale API 失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return readHTTPError(response)
	}
	if result == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(result); err != nil {
		return fmt.Errorf("解析 Tailscale API 响应失败: %w", err)
	}
	return nil
}

type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Tailscale API HTTP %d", e.StatusCode)
}

func readHTTPError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
	return &HTTPError{StatusCode: response.StatusCode, Body: string(body)}
}

func (c *TailscaleClient) apiURL(parts ...string) string {
	all := append([]string{"api", "v2"}, parts...)
	escaped := make([]string, 0, len(all))
	for _, part := range all {
		escaped = append(escaped, url.PathEscape(part))
	}
	return c.baseURL + "/" + path.Join(escaped...)
}

func (c *TailscaleClient) tailnetURL(parts ...string) string {
	all := append([]string{"api", "v2", "tailnet", c.tailnet}, parts...)
	escaped := make([]string, 0, len(all))
	for _, part := range all {
		escaped = append(escaped, url.PathEscape(part))
	}
	return c.baseURL + "/" + path.Join(escaped...)
}
