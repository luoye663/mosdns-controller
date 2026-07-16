package mosdnsclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGeositeStatusReportsMissingPluginEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("Invalid request GET " + r.URL.Path))
	}))
	defer server.Close()
	client := New(server.URL, "test", time.Second)
	_, err := client.GeositeStatus(context.Background())
	if err == nil || !strings.Contains(err.Error(), "required plugin endpoint may be unavailable") || !strings.Contains(err.Error(), "/plugins/geosite_cn/status") {
		t.Fatalf("err=%v", err)
	}
}
