// Package mosdnsclient 是 controller 对 mosdns 插件 API 的隔离层，便于测试替换。
package mosdnsclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var ErrConflict = errors.New("mosdns snapshot version conflict")
var ErrUnknown = errors.New("mosdns request outcome unknown")

type Client interface {
	Status(context.Context) (Status, error)
	Validate(context.Context, Snapshot) (ValidateResult, error)
	Apply(context.Context, Snapshot) (ApplyResult, error)
	Match(context.Context, string) (any, error)
	Flush(context.Context, string) error
	UpstreamStatus(context.Context, string) (UpstreamSnapshot, error)
	ApplyUpstream(context.Context, string, UpstreamSnapshot) (UpstreamSnapshot, error)
}
type UpstreamSnapshot struct {
	Version                uint64     `json:"version"`
	ExpectedCurrentVersion uint64     `json:"expected_current_version"`
	Mode                   string     `json:"mode"`
	Concurrent             int        `json:"concurrent"`
	Socks5                 string     `json:"socks5,omitempty"`
	Upstreams              []Upstream `json:"upstreams"`
	Checksum               string     `json:"checksum,omitempty"`
}
type Upstream struct {
	Tag      string `json:"tag"`
	Addr     string `json:"addr"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
}
type Status struct {
	State           string `json:"state"`
	SnapshotVersion uint64 `json:"snapshot_version"`
	Checksum        string `json:"checksum"`
}
type ValidateResult struct {
	Valid           bool   `json:"valid"`
	Checksum        string `json:"checksum"`
	RuleCount       int    `json:"rule_count"`
	RegexpRuleCount int    `json:"regexp_rule_count"`
}
type ApplyResult struct {
	Applied         bool   `json:"applied"`
	PreviousVersion uint64 `json:"previous_version"`
	Version         uint64 `json:"version"`
	Checksum        string `json:"checksum"`
}
type Snapshot struct {
	SchemaVersion          uint32    `json:"schema_version"`
	Version                uint64    `json:"version"`
	ExpectedCurrentVersion uint64    `json:"expected_current_version"`
	GeneratedAt            time.Time `json:"generated_at"`
	Checksum               string    `json:"checksum"`
	BlockRCode             int       `json:"block_rcode"`
	Rules                  []Rule    `json:"rules"`
}
type Rule struct {
	ID        int64  `json:"id"`
	Category  string `json:"category"`
	Action    string `json:"action"`
	MatchType string `json:"match_type"`
	Pattern   string `json:"pattern"`
	Priority  int    `json:"priority"`
	Source    string `json:"source"`
	Comment   string `json:"comment"`
}
type HTTPClient struct {
	baseURL, token string
	http           *http.Client
}

func New(baseURL, token string, timeout time.Duration) *HTTPClient {
	return &HTTPClient{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: &http.Client{Timeout: timeout}}
}
func ReadToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read mosdns token: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("mosdns token is empty")
	}
	return token, nil
}
func (c *HTTPClient) Status(ctx context.Context) (Status, error) {
	var value Status
	return value, c.request(ctx, http.MethodGet, "/plugins/dynamic_rules/status", nil, &value)
}
func (c *HTTPClient) Validate(ctx context.Context, s Snapshot) (ValidateResult, error) {
	var value ValidateResult
	return value, c.request(ctx, http.MethodPost, "/plugins/dynamic_rules/validate", s, &value)
}
func (c *HTTPClient) Apply(ctx context.Context, s Snapshot) (ApplyResult, error) {
	var value ApplyResult
	return value, c.request(ctx, http.MethodPut, "/plugins/dynamic_rules/snapshot", s, &value)
}
func (c *HTTPClient) Match(ctx context.Context, qname string) (any, error) {
	var value any
	return value, c.request(ctx, http.MethodPost, "/plugins/dynamic_rules/match", map[string]string{"qname": qname}, &value)
}
func (c *HTTPClient) Flush(ctx context.Context, tag string) error {
	if tag != "cache_local" && tag != "cache_remote" {
		return fmt.Errorf("unsupported cache tag %q", tag)
	}
	return c.request(ctx, http.MethodGet, "/plugins/"+tag+"/flush", nil, nil)
}
func (c *HTTPClient) UpstreamStatus(ctx context.Context, group string) (UpstreamSnapshot, error) {
	if group != "local_dns" && group != "remote_dns" {
		return UpstreamSnapshot{}, fmt.Errorf("unsupported upstream group %q", group)
	}
	var value UpstreamSnapshot
	return value, c.request(ctx, http.MethodGet, "/plugins/"+group+"/status", nil, &value)
}
func (c *HTTPClient) ApplyUpstream(ctx context.Context, group string, snapshot UpstreamSnapshot) (UpstreamSnapshot, error) {
	if group != "local_dns" && group != "remote_dns" {
		return UpstreamSnapshot{}, fmt.Errorf("unsupported upstream group %q", group)
	}
	var value UpstreamSnapshot
	return value, c.request(ctx, http.MethodPut, "/plugins/"+group+"/snapshot", snapshot, &value)
}
func (c *HTTPClient) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ErrUnknown
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return ErrConflict
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mosdns API %s %s: %s", method, path, resp.Status)
	}
	if output != nil {
		return json.NewDecoder(resp.Body).Decode(output)
	}
	return nil
}
