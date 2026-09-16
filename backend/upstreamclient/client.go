// Package upstreamclient 消费 model-surge-upstream 的下发面。
// 该服务是静态配置中心：只回答"目标长什么样"，不代理数据面流量。
package upstreamclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aceaura/model-surge-relay/backend/apperr"
)

type Client struct {
	BaseURL     string
	DeliveryKey string
	HTTP        *http.Client
}

func New(baseURL, deliveryKey string) *Client {
	return &Client{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		DeliveryKey: deliveryKey,
		HTTP:        &http.Client{Timeout: 10 * time.Second},
	}
}

// Listing 是目录条目，不含凭据。
type Listing struct {
	ID            string `json:"id"`
	Account       string `json:"account"`
	ProviderID    string `json:"provider_id"`
	Protocol      string `json:"protocol"`
	NativeModel   string `json:"native_model"`
	ContextWindow int    `json:"context_window,omitempty"`
	Enabled       bool   `json:"enabled"`
}

type QuotaReport struct {
	Account string          `json:"account"`
	Raw     json.RawMessage `json:"raw,omitempty"`
}

func (c *Client) Models(ctx context.Context) ([]Listing, error) {
	var body struct {
		Models []Listing `json:"models"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/models", nil, &body); err != nil {
		return nil, err
	}
	return body.Models, nil
}

func (c *Client) Resolve(ctx context.Context, modelID string) (ResolvedTarget, error) {
	payload := map[string]string{"model_id": modelID}
	var target ResolvedTarget
	if err := c.do(ctx, http.MethodPost, "/v1/resolve", payload, &target); err != nil {
		return ResolvedTarget{}, err
	}
	return target, nil
}

func (c *Client) Quota(ctx context.Context, account string) (QuotaReport, error) {
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/v1/accounts/"+account+"/quota", nil, &raw); err != nil {
		return QuotaReport{}, err
	}
	return QuotaReport{Account: account, Raw: raw}, nil
}

// Ready 供健康检查使用；探测目录端点，可达即为真。
func (c *Client) Ready(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := c.Models(ctx)
	return err == nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body *bytes.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return apperr.New(apperr.Internal, fmt.Sprintf("encode upstream request: %v", err))
		}
		body = bytes.NewReader(raw)
	} else {
		body = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return apperr.New(apperr.Internal, fmt.Sprintf("build upstream request: %v", err))
	}
	req.Header.Set("Authorization", "Bearer "+c.DeliveryKey)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		// 不可达是可重试的：调用方可换目标或稍后再试。
		return apperr.New(apperr.TargetUnavailable, fmt.Sprintf("upstream unreachable: %v", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return apperr.New(apperr.NotFound, fmt.Sprintf("upstream returned 404 for %s", path))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apperr.New(apperr.TargetUnavailable,
			fmt.Sprintf("upstream returned %d for %s", resp.StatusCode, path))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return apperr.New(apperr.Internal, fmt.Sprintf("decode upstream response: %v", err))
	}
	return nil
}
