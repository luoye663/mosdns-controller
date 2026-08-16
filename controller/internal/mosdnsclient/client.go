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
var ErrRejected = errors.New("mosdns rejected request")

type Client interface {
	Status(context.Context) (Status, error)
	Validate(context.Context, Snapshot) (ValidateResult, error)
	Apply(context.Context, Snapshot) (ApplyResult, error)
	Match(context.Context, string) (any, error)
	Flush(context.Context, string) error
	SetCacheEnabled(context.Context, bool) error
	SetCacheTTL(context.Context, int) error
	NegativeCache(context.Context, string) (NegativeCacheSettings, error)
	SetNegativeCache(context.Context, string, NegativeCacheSettings) (NegativeCacheSettings, error)
	RegistryStatus(context.Context) (RegistrySnapshot, error)
	RegistryRuntimeStatus(context.Context) (RegistryRuntimeStatus, error)
	ApplyRegistry(context.Context, RegistrySnapshot) (RegistrySnapshot, error)
	FlushRegistry(context.Context, string, uint64) error
	AddressFamilyStatus(context.Context) (AddressFamilySnapshot, error)
	ApplyAddressFamily(context.Context, AddressFamilySnapshot) (AddressFamilySnapshot, error)
	AuditStatus(context.Context) (AuditStatus, error)
}
type RegistrySnapshot struct {
	SchemaVersion          uint32              `json:"schema_version"`
	Version                uint64              `json:"version"`
	ExpectedCurrentVersion uint64              `json:"expected_current_version"`
	DefaultGroupID         string              `json:"default_group_id"`
	Groups                 []UpstreamGroup     `json:"groups"`
	Cache                  RegistryCacheConfig `json:"cache"`
	Protection             ProtectionConfig    `json:"protection"`
}
type UpstreamGroup struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	Enabled        bool             `json:"enabled"`
	Mode           string           `json:"mode"`
	Concurrent     int              `json:"concurrent"`
	Socks5         string           `json:"socks5,omitempty"`
	Bootstrap      string           `json:"bootstrap,omitempty"`
	BootstrapVer   int              `json:"bootstrap_version"`
	MaxInFlight    *int             `json:"max_in_flight,omitempty"`
	QueryTimeoutMS *int             `json:"query_timeout_ms,omitempty"`
	Upstreams      []Upstream       `json:"upstreams"`
	ECS            ECSConfig        `json:"ecs"`
	Cache          GroupCacheConfig `json:"cache"`
}
type ECSConfig struct {
	Mode    string `json:"mode"`
	Mask4   int    `json:"mask4"`
	Mask6   int    `json:"mask6"`
	Preset4 string `json:"preset4,omitempty"`
	Preset6 string `json:"preset6,omitempty"`
}
type GroupCacheConfig struct {
	Enabled bool `json:"enabled"`
	Size    int  `json:"size"`
}
type RegistryCacheConfig struct {
	Enabled  bool                `json:"enabled"`
	LazyTTL  int                 `json:"lazy_ttl"`
	Negative NegativeCacheConfig `json:"negative"`
}
type ProtectionConfig struct {
	GlobalMaxInFlight          int    `json:"global_max_in_flight"`
	DefaultGroupMaxInFlight    int    `json:"default_group_max_in_flight"`
	DefaultGroupQueryTimeoutMS int    `json:"default_group_query_timeout_ms"`
	OverloadAction             string `json:"overload_action"`
}
type RuntimeConcurrency struct {
	InFlight int64 `json:"in_flight"`
	Limit    int   `json:"limit"`
}
type GroupRuntimeStatus struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	InFlight int64  `json:"in_flight"`
	Limit    int    `json:"limit"`
}
type RegistryRuntimeStatus struct {
	RegistryVersion uint64               `json:"registry_version"`
	Global          RuntimeConcurrency   `json:"global"`
	Groups          []GroupRuntimeStatus `json:"groups"`
}
type NegativeCacheConfig struct {
	Enabled bool   `json:"enabled"`
	TTL     uint32 `json:"ttl"`
}
type Upstream struct {
	Tag       string `json:"tag"`
	Addr      string `json:"addr"`
	Priority  int    `json:"priority"`
	Weight    int    `json:"weight"`
	TimeoutMS int    `json:"timeout_ms"`
}
type AddressFamilySnapshot struct {
	Version                uint64 `json:"version"`
	ExpectedCurrentVersion uint64 `json:"expected_current_version"`
	Mode                   string `json:"mode"`
}
type NegativeCacheSettings struct {
	Enabled    bool   `json:"enabled"`
	TTLSeconds uint32 `json:"ttl_seconds"`
}
type Status struct {
	SchemaVersion         uint32    `json:"schema_version"`
	PluginVersion         string    `json:"plugin_version"`
	MosdnsBase            string    `json:"mosdns_base"`
	State                 string    `json:"state"`
	SnapshotVersion       uint64    `json:"snapshot_version"`
	Checksum              string    `json:"checksum"`
	RuleCount             int       `json:"rule_count"`
	RegexpRuleCount       int       `json:"regexp_rule_count"`
	LoadedAt              time.Time `json:"loaded_at"`
	LastCompileDurationMS int64     `json:"last_compile_duration_ms"`
	SnapshotFileOK        bool      `json:"snapshot_file_ok"`
	MemoryRSSBytes        int64     `json:"memory_rss_bytes"`
}
type AuditStatus struct {
	QueueDepth    int   `json:"queue_depth"`
	QueueCapacity int   `json:"queue_capacity"`
	DroppedEvents int64 `json:"dropped_events"`
}
type ValidateResult struct {
	Valid             bool     `json:"valid"`
	Checksum          string   `json:"checksum"`
	RuleCount         int      `json:"rule_count"`
	RegexpRuleCount   int      `json:"regexp_rule_count"`
	CompileDurationMS int64    `json:"compile_duration_ms"`
	Warnings          []string `json:"warnings"`
}
type ApplyResult struct {
	Applied           bool      `json:"applied"`
	PreviousVersion   uint64    `json:"previous_version"`
	Version           uint64    `json:"version"`
	Checksum          string    `json:"checksum"`
	CompileDurationMS int64     `json:"compile_duration_ms"`
	PersistDurationMS int64     `json:"persist_duration_ms"`
	AppliedAt         time.Time `json:"applied_at"`
}
type Snapshot struct {
	SchemaVersion          uint32            `json:"schema_version"`
	Version                uint64            `json:"version"`
	ExpectedCurrentVersion uint64            `json:"expected_current_version"`
	GeneratedAt            time.Time         `json:"generated_at"`
	Checksum               string            `json:"checksum"`
	BlockRCode             int               `json:"block_rcode"`
	Rules                  []Rule            `json:"rules"`
	SubscriptionSets       []SubscriptionSet `json:"subscription_sets"`
}
type SubscriptionSet struct {
	SourceID        int64    `json:"source_id"`
	SourceName      string   `json:"source_name"`
	Category        string   `json:"category"`
	Action          string   `json:"action"`
	BindingID       int64    `json:"binding_id,omitempty"`
	UpstreamGroupID string   `json:"upstream_group_id,omitempty"`
	Priority        int      `json:"priority"`
	Domains         []string `json:"domains"`
}
type Rule struct {
	ID              int64    `json:"id"`
	Category        string   `json:"category"`
	Action          string   `json:"action"`
	UpstreamGroupID string   `json:"upstream_group_id,omitempty"`
	MatchType       string   `json:"match_type"`
	Pattern         string   `json:"pattern"`
	Priority        int      `json:"priority"`
	Source          string   `json:"source"`
	Comment         string   `json:"comment"`
	IPv4Addresses   []string `json:"ipv4_addresses,omitempty"`
	IPv6Addresses   []string `json:"ipv6_addresses,omitempty"`
	TTL             uint32   `json:"ttl,omitempty"`
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
	if !cacheTag(tag) {
		return fmt.Errorf("unsupported cache tag %q", tag)
	}
	return c.request(ctx, http.MethodGet, "/plugins/"+tag+"/flush", nil, nil)
}
func (c *HTTPClient) NegativeCache(ctx context.Context, tag string) (NegativeCacheSettings, error) {
	if !cacheTag(tag) {
		return NegativeCacheSettings{}, fmt.Errorf("unsupported cache tag %q", tag)
	}
	var value NegativeCacheSettings
	return value, c.request(ctx, http.MethodGet, "/plugins/"+tag+"/negative-cache", nil, &value)
}
func (c *HTTPClient) SetNegativeCache(ctx context.Context, tag string, settings NegativeCacheSettings) (NegativeCacheSettings, error) {
	if !cacheTag(tag) {
		return NegativeCacheSettings{}, fmt.Errorf("unsupported cache tag %q", tag)
	}
	var value NegativeCacheSettings
	err := c.request(ctx, http.MethodPut, "/plugins/"+tag+"/negative-cache", settings, &value)
	if err == nil && value != settings {
		err = errors.New("negative cache setting was not applied")
	}
	return value, err
}
func cacheTag(tag string) bool { return tag == "cache_local" || tag == "cache_remote" }
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
func (c *HTTPClient) RegistryStatus(ctx context.Context) (RegistrySnapshot, error) {
	var value RegistrySnapshot
	err := c.request(ctx, http.MethodGet, "/plugins/dynamic_upstreams/status", nil, &value)
	if err == nil && !validRegistrySnapshot(value, value.Version) {
		err = errors.New("dynamic upstream registry returned an invalid snapshot")
	}
	return value, err
}
func (c *HTTPClient) RegistryRuntimeStatus(ctx context.Context) (RegistryRuntimeStatus, error) {
	var value RegistryRuntimeStatus
	err := c.request(ctx, http.MethodGet, "/plugins/dynamic_upstreams/runtime-status", nil, &value)
	if err == nil && !validRegistryRuntimeStatus(value) {
		err = errors.New("dynamic upstream registry returned invalid runtime status")
	}
	return value, err
}

func validRegistryRuntimeStatus(status RegistryRuntimeStatus) bool {
	if status.RegistryVersion == 0 || status.Global.InFlight < 0 || status.Global.Limit < 1 {
		return false
	}
	seen := make(map[string]struct{}, len(status.Groups))
	for _, group := range status.Groups {
		if group.ID == "" || group.Name == "" || group.InFlight < 0 || group.Limit < 1 {
			return false
		}
		if _, exists := seen[group.ID]; exists {
			return false
		}
		seen[group.ID] = struct{}{}
	}
	return true
}
func (c *HTTPClient) ApplyRegistry(ctx context.Context, snapshot RegistrySnapshot) (RegistrySnapshot, error) {
	var value RegistrySnapshot
	err := c.request(ctx, http.MethodPut, "/plugins/dynamic_upstreams/snapshot", snapshot, &value)
	if err == nil && !validRegistrySnapshot(value, snapshot.Version) {
		err = fmt.Errorf("%w: dynamic upstream registry returned an invalid applied snapshot", ErrUnknown)
	}
	return value, err
}
func (c *HTTPClient) FlushRegistry(ctx context.Context, groupID string, expectedCurrentVersion uint64) error {
	var value struct {
		Flushed bool   `json:"flushed"`
		GroupID string `json:"group_id"`
	}
	input := struct {
		GroupID                string `json:"group_id"`
		ExpectedCurrentVersion uint64 `json:"expected_current_version"`
	}{GroupID: groupID, ExpectedCurrentVersion: expectedCurrentVersion}
	if err := c.request(ctx, http.MethodPost, "/plugins/dynamic_upstreams/flush", input, &value); err != nil {
		return err
	}
	if !value.Flushed || value.GroupID != groupID {
		return fmt.Errorf("%w: dynamic upstream registry flush response does not match the request", ErrUnknown)
	}
	return nil
}

func validRegistrySnapshot(snapshot RegistrySnapshot, expectedVersion uint64) bool {
	if (snapshot.SchemaVersion != 1 && snapshot.SchemaVersion != 2) || snapshot.Version != expectedVersion || snapshot.Version == 0 || snapshot.ExpectedCurrentVersion != 0 || snapshot.DefaultGroupID == "" || len(snapshot.Groups) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(snapshot.Groups))
	defaultEnabled := false
	for _, group := range snapshot.Groups {
		if group.ID == "" {
			return false
		}
		if _, exists := seen[group.ID]; exists {
			return false
		}
		seen[group.ID] = struct{}{}
		if group.ID == snapshot.DefaultGroupID {
			defaultEnabled = group.Enabled
		}
	}
	return defaultEnabled
}
func (c *HTTPClient) AddressFamilyStatus(ctx context.Context) (AddressFamilySnapshot, error) {
	var value AddressFamilySnapshot
	return value, c.request(ctx, http.MethodGet, "/plugins/address_family/status", nil, &value)
}
func (c *HTTPClient) ApplyAddressFamily(ctx context.Context, snapshot AddressFamilySnapshot) (AddressFamilySnapshot, error) {
	var value AddressFamilySnapshot
	return value, c.request(ctx, http.MethodPut, "/plugins/address_family/snapshot", snapshot, &value)
}
func (c *HTTPClient) AuditStatus(ctx context.Context) (AuditStatus, error) {
	var value AuditStatus
	return value, c.request(ctx, http.MethodGet, "/plugins/query_audit/status", nil, &value)
}
func (c *HTTPClient) request(ctx context.Context, method, path string, input, output any) error {
	isWrite := (method != http.MethodGet && method != http.MethodHead) || strings.HasSuffix(path, "/flush")
	unknown := func(err error) error {
		if !isWrite {
			return err
		}
		return fmt.Errorf("%w: %v", ErrUnknown, err)
	}
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
		return unknown(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return ErrConflict
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		err := fmt.Errorf("mosdns API %s %s: %s: %s", method, path, resp.Status, responseSummary(body))
		if isWrite && (resp.StatusCode < 400 || resp.StatusCode >= 500) {
			return unknown(err)
		}
		if isWrite {
			return fmt.Errorf("%w: %v", ErrRejected, err)
		}
		return err
	}
	if output != nil {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return unknown(fmt.Errorf("read mosdns API %s %s: %w", method, path, err))
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(output); err != nil {
			return unknown(fmt.Errorf("mosdns API %s %s returned non-JSON data; the required plugin endpoint may be unavailable: %s", method, path, responseSummary(body)))
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return unknown(fmt.Errorf("mosdns API %s %s returned trailing JSON data", method, path))
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
