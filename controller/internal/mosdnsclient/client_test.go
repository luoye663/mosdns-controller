package mosdnsclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
