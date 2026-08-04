package mosdnsclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func registrySnapshot(version uint64) RegistrySnapshot {
	return RegistrySnapshot{SchemaVersion: 1, Version: version, DefaultGroupID: "default_dns", Groups: []UpstreamGroup{{ID: "default_dns", Name: "Default", Enabled: true, Mode: "race", Concurrent: 1, Upstreams: []Upstream{{Tag: "default", Addr: "https://dns.example/dns-query", Priority: 100, Weight: 1}}, ECS: ECSConfig{Mode: "off", Mask4: 24, Mask6: 48}, Cache: GroupCacheConfig{Enabled: true, Size: 1024}}}, Cache: RegistryCacheConfig{Enabled: true, Negative: NegativeCacheConfig{Enabled: true, TTL: 30}}}
}

func TestRegistryEndpointsAndStrictConsistency(t *testing.T) {
	current := registrySnapshot(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/plugins/dynamic_upstreams/status":
			_ = json.NewEncoder(w).Encode(current)
		case "/plugins/dynamic_upstreams/snapshot":
			if err := json.NewDecoder(r.Body).Decode(&current); err != nil {
				t.Fatal(err)
			}
			current.ExpectedCurrentVersion = 0
			_ = json.NewEncoder(w).Encode(current)
		case "/plugins/dynamic_upstreams/flush":
			var input struct {
				GroupID                string `json:"group_id"`
				ExpectedCurrentVersion uint64 `json:"expected_current_version"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input.ExpectedCurrentVersion != current.Version {
				t.Errorf("flush version=%d", input.ExpectedCurrentVersion)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"flushed": true, "group_id": input.GroupID})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := New(server.URL, "test-token", time.Second)
	status, err := client.RegistryStatus(context.Background())
	if err != nil || status.Version != 1 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	next := registrySnapshot(2)
	next.ExpectedCurrentVersion = 1
	if applied, err := client.ApplyRegistry(context.Background(), next); err != nil || applied.Version != 2 {
		t.Fatalf("applied=%+v err=%v", applied, err)
	}
	if err := client.FlushRegistry(context.Background(), "office_dns", 2); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryConflictAndMismatchedResponse(t *testing.T) {
	conflict := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusConflict) }))
	defer conflict.Close()
	if _, err := New(conflict.URL, "test", time.Second).ApplyRegistry(context.Background(), registrySnapshot(2)); err != ErrConflict {
		t.Fatalf("conflict error=%v", err)
	}
	mismatch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(registrySnapshot(3)) }))
	defer mismatch.Close()
	if _, err := New(mismatch.URL, "test", time.Second).ApplyRegistry(context.Background(), registrySnapshot(2)); err == nil {
		t.Fatal("mismatched registry response accepted")
	}
}

func TestApplyRegistryAcceptsValidServerNormalization(t *testing.T) {
	normalized := registrySnapshot(2)
	normalized.Groups[0].Name = "Normalized Remote"
	normalized.Groups[0].Upstreams[0].Addr = "https://dns.example/dns-query?normalized=1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(normalized)
	}))
	defer server.Close()
	requested := registrySnapshot(2)
	requested.ExpectedCurrentVersion = 1
	applied, err := New(server.URL, "test", time.Second).ApplyRegistry(context.Background(), requested)
	if err != nil || applied.Groups[0].Name != "Normalized Remote" {
		t.Fatalf("applied=%+v err=%v", applied, err)
	}
}

func TestWriteTransportAndDecodeFailuresAreUnknown(t *testing.T) {
	client := New("http://127.0.0.1:1", "test", 50*time.Millisecond)
	if _, err := client.ApplyRegistry(context.Background(), registrySnapshot(2)); !errors.Is(err, ErrUnknown) {
		t.Fatalf("transport error=%v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()
	if _, err := New(server.URL, "test", time.Second).ApplyRegistry(context.Background(), registrySnapshot(2)); !errors.Is(err, ErrUnknown) {
		t.Fatalf("decode error=%v", err)
	}
}

func TestExplicitWrite4xxIsRejectedNotUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer server.Close()
	_, err := New(server.URL, "test", time.Second).ApplyRegistry(context.Background(), registrySnapshot(2))
	if !errors.Is(err, ErrRejected) || errors.Is(err, ErrUnknown) {
		t.Fatalf("4xx error=%v", err)
	}
}

func TestStatusAcceptsCompleteRuleEngineResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"schema_version":3,"plugin_version":"dev","mosdns_base":"v5.3.4","state":"ready","snapshot_version":22,"checksum":"sha256:test","rule_count":12,"regexp_rule_count":2,"loaded_at":"2026-08-03T19:33:36Z","last_compile_duration_ms":7,"snapshot_file_ok":true,"memory_rss_bytes":20447232}`))
	}))
	defer server.Close()

	status, err := New(server.URL, "test", time.Second).Status(context.Background())
	if err != nil || status.SchemaVersion != 3 || status.SnapshotVersion != 22 || status.RuleCount != 12 || !status.SnapshotFileOK {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestValidateAndApplyAcceptCompleteRuleEngineResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/plugins/dynamic_rules/validate":
			_, _ = w.Write([]byte(`{"valid":true,"checksum":"sha256:test","rule_count":12,"regexp_rule_count":2,"compile_duration_ms":7,"warnings":[]}`))
		case "/plugins/dynamic_rules/snapshot":
			_, _ = w.Write([]byte(`{"applied":true,"previous_version":21,"version":22,"checksum":"sha256:test","compile_duration_ms":7,"persist_duration_ms":3,"applied_at":"2026-08-03T19:33:36Z"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := New(server.URL, "test", time.Second)
	validated, err := client.Validate(context.Background(), Snapshot{})
	if err != nil || !validated.Valid || validated.CompileDurationMS != 7 {
		t.Fatalf("validated=%+v err=%v", validated, err)
	}
	applied, err := client.Apply(context.Background(), Snapshot{})
	if err != nil || !applied.Applied || applied.PersistDurationMS != 3 || applied.AppliedAt.IsZero() {
		t.Fatalf("applied=%+v err=%v", applied, err)
	}
}

func TestNegativeCacheGetAndPut(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/plugins/cache_local/negative-cache" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		settings := NegativeCacheSettings{Enabled: true, TTLSeconds: 30}
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
				t.Errorf("decode request: %v", err)
			}
		}
		_ = json.NewEncoder(w).Encode(settings)
	}))
	defer server.Close()

	client := New(server.URL, "test-token", time.Second)
	got, err := client.NegativeCache(context.Background(), "cache_local")
	if err != nil || !got.Enabled || got.TTLSeconds != 30 {
		t.Fatalf("get=%+v err=%v", got, err)
	}
	want := NegativeCacheSettings{Enabled: false, TTLSeconds: 45}
	got, err = client.SetNegativeCache(context.Background(), "cache_local", want)
	if err != nil || got != want {
		t.Fatalf("put=%+v err=%v", got, err)
	}
	if requests != 2 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestNegativeCacheRejectsUnsupportedTagAndMismatchedResponse(t *testing.T) {
	client := New("http://127.0.0.1", "test", time.Second)
	if _, err := client.NegativeCache(context.Background(), "other"); err == nil {
		t.Fatal("unsupported cache tag accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(NegativeCacheSettings{Enabled: true, TTLSeconds: 1})
	}))
	defer server.Close()
	client = New(server.URL, "test", time.Second)
	if _, err := client.SetNegativeCache(context.Background(), "cache_remote", NegativeCacheSettings{Enabled: false, TTLSeconds: 30}); err == nil {
		t.Fatal("mismatched negative cache response accepted")
	}
}
