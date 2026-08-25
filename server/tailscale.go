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
	"time"
)

type AuthKey struct {
	Secret string
	ID     string
}

type OAuthAccessToken struct {
	Token     string
	ExpiresAt time.Time
	Scopes    []string
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
	ExchangeOAuthToken(context.Context, string, string) (OAuthAccessToken, error)
	ValidateCredential(context.Context, string) error
	CreateAuthKey(context.Context, string, time.Duration, bool) (AuthKey, error)
	DeleteAuthKey(context.Context, string, string) error
	GetDevice(context.Context, string, string) (Device, error)
	SetDeviceRoutes(context.Context, string, string, []string) error
	DeleteDevice(context.Context, string, string) error
}

func (c *TailscaleClient) DeleteAuthKey(ctx context.Context, accessToken, keyID string) error {
	if keyID == "" {
		return nil
	}
	err := c.doJSON(ctx, accessToken, http.MethodDelete, c.tailnetURL("keys", keyID), nil, nil)
	var apiErr *HTTPError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

type TailscaleClient struct {
	baseURL    string
	tailnet    string
	httpClient *http.Client
	userAgent  string
}

func NewTailscaleClient(config Config) *TailscaleClient {
	return &TailscaleClient{
		baseURL:    strings.TrimRight(config.TailscaleBaseURL, "/"),
		tailnet:    config.TailscaleTailnet,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		userAgent:  "PinNode/0.1",
	}
}

func (c *TailscaleClient) ExchangeOAuthToken(ctx context.Context, clientID, clientSecret string) (OAuthAccessToken, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.apiURL("oauth", "token"), strings.NewReader(form.Encode()),
	)
	if err != nil {
		return OAuthAccessToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	response, err := c.httpClient.Do(req)
	if err != nil {
		return OAuthAccessToken{}, fmt.Errorf("请求 Tailscale OAuth token 失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return OAuthAccessToken{}, readHTTPError(response)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
		Scope       string `json:"scope"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return OAuthAccessToken{}, fmt.Errorf("解析 Tailscale OAuth token 失败: %w", err)
	}
	if payload.AccessToken == "" || payload.ExpiresIn <= 0 || payload.ExpiresIn > 24*60*60 ||
		(payload.TokenType != "" && !strings.EqualFold(payload.TokenType, "Bearer")) {
		return OAuthAccessToken{}, errors.New("Tailscale OAuth token 响应不完整")
	}
	return OAuthAccessToken{
		Token:     payload.AccessToken,
		ExpiresAt: time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second),
		Scopes:    strings.Fields(payload.Scope),
	}, nil
}

func (c *TailscaleClient) ValidateCredential(ctx context.Context, accessToken string) error {
	return c.doJSON(ctx, accessToken, http.MethodGet, c.tailnetURL("keys"), nil, nil)
}

func (c *TailscaleClient) CreateAuthKey(ctx context.Context, accessToken string, expiry time.Duration, ephemeral bool) (AuthKey, error) {
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
					"tags":          []string{managedDeviceTag},
				},
			},
		},
		"expirySeconds": seconds,
	}
	var response struct {
		Key string `json:"key"`
		ID  string `json:"id"`
	}
	if err := c.doJSON(ctx, accessToken, http.MethodPost, c.tailnetURL("keys"), payload, &response); err != nil {
		return AuthKey{}, err
	}
	if response.Key == "" || response.ID == "" {
		return AuthKey{}, errors.New("Tailscale API 未返回完整 auth key")
	}
	return AuthKey{Secret: response.Key, ID: response.ID}, nil
}

func (c *TailscaleClient) GetDevice(ctx context.Context, accessToken, deviceID string) (Device, error) {
	var response Device
	err := c.doJSON(ctx, accessToken, http.MethodGet, c.apiURL("device", deviceID), nil, &response)
	return response, err
}

func (c *TailscaleClient) SetDeviceRoutes(ctx context.Context, accessToken, deviceID string, routes []string) error {
	payload := map[string][]string{"routes": routes}
	return c.doJSON(ctx, accessToken, http.MethodPost, c.apiURL("device", deviceID, "routes"), payload, nil)
}

func (c *TailscaleClient) DeleteDevice(ctx context.Context, accessToken, deviceID string) error {
	err := c.doJSON(ctx, accessToken, http.MethodDelete, c.apiURL("device", deviceID), nil, nil)
	var apiErr *HTTPError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

func (c *TailscaleClient) doJSON(ctx context.Context, accessToken, method, endpoint string, payload any, result any) error {
	if accessToken == "" {
		return errors.New("Tailscale API access token 为空")
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
	req.Header.Set("Authorization", "Bearer "+accessToken)
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
