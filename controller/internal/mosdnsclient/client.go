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
	SetCacheEnabled(context.Context, bool) error
	SetCacheTTL(context.Context, int) error
	UpstreamStatus(context.Context, string) (UpstreamSnapshot, error)
	ApplyUpstream(context.Context, string, UpstreamSnapshot) (UpstreamSnapshot, error)
	ECSStatus(context.Context, string) (ECSSnapshot, error)
	ApplyECS(context.Context, string, ECSSnapshot) (ECSSnapshot, error)
	GeositeStatus(context.Context) (DomainSetStatus, error)
	ApplyGeosite(context.Context, DomainSetSnapshot) (DomainSetStatus, error)
	AuditStatus(context.Context) (AuditStatus, error)
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
type ECSSnapshot struct {
	Version                uint64 `json:"version"`
	ExpectedCurrentVersion uint64 `json:"expected_current_version"`
	Mode                   string `json:"mode"`
	Mask4                  int    `json:"mask4"`
	Mask6                  int    `json:"mask6"`
	Preset4                string `json:"preset4,omitempty"`
	Preset6                string `json:"preset6,omitempty"`
}
type DomainSetStatus struct {
	Version   uint64    `json:"version"`
	Checksum  string    `json:"checksum"`
	RuleCount int       `json:"rule_count"`
	LoadedAt  time.Time `json:"loaded_at"`
}
type DomainSetSnapshot struct {
	Version                uint64 `json:"version"`
	ExpectedCurrentVersion uint64 `json:"expected_current_version"`
	Rules                  string `json:"rules"`
}
type Status struct {
	State           string `json:"state"`
	SnapshotVersion uint64 `json:"snapshot_version"`
	Checksum        string `json:"checksum"`
}
type AuditStatus struct {
	QueueDepth    int   `json:"queue_depth"`
	QueueCapacity int   `json:"queue_capacity"`
	DroppedEvents int64 `json:"dropped_events"`
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
func (c *HTTPClient) SetCacheEnabled(ctx context.Context, enabled bool) error {
	var local, remote struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.request(ctx, http.MethodPut, "/plugins/cache_local/enabled", map[string]bool{"enabled": enabled}, &local); err != nil {
		return err
	}
	if err := c.request(ctx, http.MethodPut, "/plugins/cache_remote/enabled", map[string]bool{"enabled": enabled}, &remote); err != nil {
		return err
	}
	if local.Enabled != enabled || remote.Enabled != enabled {
		return errors.New("cache setting was not applied")
	}
	return nil
}
func (c *HTTPClient) SetCacheTTL(ctx context.Context, ttl int) error {
	for _, tag := range []string{"cache_local", "cache_remote"} {
		var result struct {
			TTL int `json:"ttl"`
		}
		if err := c.request(ctx, http.MethodPut, "/plugins/"+tag+"/ttl", map[string]int{"ttl": ttl}, &result); err != nil {
			return err
		}
		if result.TTL != ttl {
			return errors.New("cache TTL was not applied")
		}
	}
	return nil
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
func (c *HTTPClient) ECSStatus(ctx context.Context, group string) (ECSSnapshot, error) {
	if group != "local_dns" && group != "remote_dns" {
		return ECSSnapshot{}, fmt.Errorf("unsupported upstream group %q", group)
	}
	var value ECSSnapshot
	return value, c.request(ctx, http.MethodGet, "/plugins/ecs_"+strings.TrimSuffix(group, "_dns")+"/status", nil, &value)
}
func (c *HTTPClient) ApplyECS(ctx context.Context, group string, snapshot ECSSnapshot) (ECSSnapshot, error) {
	if group != "local_dns" && group != "remote_dns" {
		return ECSSnapshot{}, fmt.Errorf("unsupported upstream group %q", group)
	}
	var value ECSSnapshot
	return value, c.request(ctx, http.MethodPut, "/plugins/ecs_"+strings.TrimSuffix(group, "_dns")+"/snapshot", snapshot, &value)
}
func (c *HTTPClient) GeositeStatus(ctx context.Context) (DomainSetStatus, error) {
	var value DomainSetStatus
	return value, c.request(ctx, http.MethodGet, "/plugins/geosite_cn/status", nil, &value)
}
func (c *HTTPClient) ApplyGeosite(ctx context.Context, snapshot DomainSetSnapshot) (DomainSetStatus, error) {
	var value DomainSetStatus
	return value, c.request(ctx, http.MethodPut, "/plugins/geosite_cn/snapshot", snapshot, &value)
}
func (c *HTTPClient) AuditStatus(ctx context.Context) (AuditStatus, error) {
	var value AuditStatus
	return value, c.request(ctx, http.MethodGet, "/plugins/query_audit/status", nil, &value)
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("mosdns API %s %s: %s: %s", method, path, resp.Status, responseSummary(body))
	}
	if output != nil {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return fmt.Errorf("read mosdns API %s %s: %w", method, path, err)
		}
		if err := json.Unmarshal(body, output); err != nil {
			return fmt.Errorf("mosdns API %s %s returned non-JSON data; the required plugin endpoint may be unavailable: %s", method, path, responseSummary(body))
		}
	}
	return nil
}

func responseSummary(body []byte) string {
	value := strings.TrimSpace(string(body))
	if len(value) > 512 {
		value = value[:512] + "..."
	}
	if value == "" {
		return "empty response"
	}
	return value
}
