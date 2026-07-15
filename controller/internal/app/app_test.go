package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/managed-dns/controller/internal/auth"
	"github.com/managed-dns/controller/internal/config"
	"github.com/managed-dns/controller/internal/mosdnsclient"
	"github.com/managed-dns/controller/internal/storage"
)

func testApp(t *testing.T) *App {
	t.Helper()
	cfg := config.Default()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	application := New(slog.Default(), cfg, store, mosdnsclient.New("http://127.0.0.1", "test", time.Second), "test")
	t.Cleanup(application.Close)
	return application
}
func TestHealthEndpoints(t *testing.T) {
	application := testApp(t)
	for _, handler := range []http.Handler{application.PublicHandler(), application.InternalHandler()} {
		req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}
}
func TestLoginSessionAndCSRF(t *testing.T) {
	application := testApp(t)
	if err := application.auth.CreateAdmin(context.Background(), "admin", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"username":"admin","password":"correct-horse-battery"}`)
	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", body)
	login.RemoteAddr = "192.0.2.10:1234"
	loginRec := httptest.NewRecorder()
	application.PublicHandler().ServeHTTP(loginRec, login)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", loginRec.Code, loginRec.Body.String())
	}
	cookie := loginRec.Result().Cookies()[0]
	var response struct {
		Data struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(loginRec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	me := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	me.AddCookie(cookie)
	meRec := httptest.NewRecorder()
	application.PublicHandler().ServeHTTP(meRec, me)
	if meRec.Code != http.StatusOK {
		t.Fatalf("me=%d", meRec.Code)
	}
	logout := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logout.AddCookie(cookie)
	logoutRec := httptest.NewRecorder()
	application.PublicHandler().ServeHTTP(logoutRec, logout)
	if logoutRec.Code != http.StatusForbidden {
		t.Fatalf("logout without csrf=%d", logoutRec.Code)
	}
	logout.Header.Set("X-CSRF-Token", response.Data.CSRFToken)
	logoutRec = httptest.NewRecorder()
	application.PublicHandler().ServeHTTP(logoutRec, logout)
	if logoutRec.Code != http.StatusOK {
		t.Fatalf("logout=%d", logoutRec.Code)
	}
}

func TestBootstrapCreatesInitialAdminAndSession(t *testing.T) {
	application := testApp(t)
	handler := application.PublicHandler()
	status := httptest.NewRequest(http.MethodGet, "/api/v1/auth/bootstrap", nil)
	statusRec := httptest.NewRecorder()
	handler.ServeHTTP(statusRec, status)
	if statusRec.Code != http.StatusOK || !bytes.Contains(statusRec.Body.Bytes(), []byte(`"required":true`)) {
		t.Fatalf("initial status=%d: %s", statusRec.Code, statusRec.Body.String())
	}
	setup := httptest.NewRequest(http.MethodPost, "/api/v1/auth/bootstrap", bytes.NewBufferString(`{"username":"admin","password":"password"}`))
	setup.RemoteAddr = "192.0.2.10:1234"
	setupRec := httptest.NewRecorder()
	handler.ServeHTTP(setupRec, setup)
	if setupRec.Code != http.StatusCreated {
		t.Fatalf("setup=%d: %s", setupRec.Code, setupRec.Body.String())
	}
	if len(setupRec.Result().Cookies()) != 1 {
		t.Fatal("initial setup did not create a session cookie")
	}
	repeated := httptest.NewRequest(http.MethodPost, "/api/v1/auth/bootstrap", bytes.NewBufferString(`{"username":"other","password":"password"}`))
	repeatedRec := httptest.NewRecorder()
	handler.ServeHTTP(repeatedRec, repeated)
	if repeatedRec.Code != http.StatusConflict {
		t.Fatalf("repeated setup=%d: %s", repeatedRec.Code, repeatedRec.Body.String())
	}
}

func TestPasswordMinimumLengthIsEight(t *testing.T) {
	application := testApp(t)
	if err := application.auth.CreateAdmin(context.Background(), "admin", "password"); err != nil {
		t.Fatalf("8-character password rejected: %v", err)
	}
	if err := application.auth.ChangePassword(context.Background(), 1, "password", "short"); err == nil {
		t.Fatal("short password accepted")
	}
}
func TestExpiredSessionIsRejected(t *testing.T) {
	application := testApp(t)
	now := time.Now().UnixMilli()
	_, err := application.store.DB().Exec(`INSERT INTO admins(username,password_hash,created_at_ms,updated_at_ms) VALUES('old','x',?,?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	token := "token"
	_, err = application.store.DB().Exec(`INSERT INTO sessions(token_hash,admin_id,csrf_hash,created_at_ms,last_seen_at_ms,expires_at_ms) VALUES(?,?,?,?,?,?)`, authDigest(token), 1, authDigest("csrf"), now, now, now-1)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	rec := httptest.NewRecorder()
	application.PublicHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestRequestBodyLimit(t *testing.T) {
	application := testApp(t)
	application.config.HTTP.MaxBodyBytes = 8
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"admin"}`))
	rec := httptest.NewRecorder()
	application.PublicHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestInternalIngestRequiresSharedToken(t *testing.T) {
	application := testApp(t)
	body := `{"schema_version":1,"sender_id":"mosdns-test","sent_at_unix_ms":1,"events":[{"schema_version":1,"event_id":"ingest-1","timestamp_unix_ms":1,"process_started_at_unix_ms":1,"client_ip":"192.0.2.1","protocol":"udp","qname":"example.com","qtype":1,"qclass":1,"rcode":0,"route":"remote","route_source":"default","upstream_group":"","cache_hit":false,"snapshot_version":1,"access_rule_id":0,"route_rule_id":0,"answer_count":0,"latency_us":1,"error_code":"","error_text":""}]}`
	unauthorized := httptest.NewRequest(http.MethodPost, "/internal/v1/query-events/batch", bytes.NewBufferString(body))
	unauthorizedRec := httptest.NewRecorder()
	application.InternalHandler().ServeHTTP(unauthorizedRec, unauthorized)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized ingest=%d", unauthorizedRec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/query-events/batch", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	application.InternalHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("ingest=%d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeviceEndpointsRequireSessionAndCSRF(t *testing.T) {
	application := testApp(t)
	now := time.Now().UnixMilli()
	if _, err := application.store.DB().Exec(`INSERT INTO admins(id,username,password_hash,created_at_ms,updated_at_ms) VALUES(1,'admin','x',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := application.store.DB().Exec(`INSERT INTO sessions(token_hash,admin_id,csrf_hash,created_at_ms,last_seen_at_ms,expires_at_ms) VALUES(?,?,?,?,?,?)`, authDigest("session"), 1, authDigest("csrf"), now, now, now+60_000); err != nil {
		t.Fatal(err)
	}
	if _, err := application.store.DB().Exec(`INSERT INTO devices(ip,note,source,first_seen_at_ms,last_seen_at_ms,updated_at_ms) VALUES('192.0.2.5','','observed',?,?,?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	unauthorizedRec := httptest.NewRecorder()
	application.PublicHandler().ServeHTTP(unauthorizedRec, unauthorized)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("devices without session=%d", unauthorizedRec.Code)
	}
	list := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	list.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session"})
	listRec := httptest.NewRecorder()
	application.PublicHandler().ServeHTTP(listRec, list)
	if listRec.Code != http.StatusOK {
		t.Fatalf("devices=%d: %s", listRec.Code, listRec.Body.String())
	}
	patch := httptest.NewRequest(http.MethodPatch, "/api/v1/devices/1", bytes.NewBufferString(`{"display_name":"办公电脑"}`))
	patch.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session"})
	patchRec := httptest.NewRecorder()
	application.PublicHandler().ServeHTTP(patchRec, patch)
	if patchRec.Code != http.StatusForbidden {
		t.Fatalf("patch without csrf=%d", patchRec.Code)
	}
	patch.Header.Set("X-CSRF-Token", "csrf")
	patchRec = httptest.NewRecorder()
	application.PublicHandler().ServeHTTP(patchRec, patch)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch=%d: %s", patchRec.Code, patchRec.Body.String())
	}
	flush := httptest.NewRequest(http.MethodPost, "/api/v1/system/cache/flush", nil)
	flush.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session"})
	flushRec := httptest.NewRecorder()
	application.PublicHandler().ServeHTTP(flushRec, flush)
	if flushRec.Code != http.StatusForbidden {
		t.Fatalf("flush without csrf=%d", flushRec.Code)
	}
}
func authDigest(value string) []byte { result := sha256.Sum256([]byte(value)); return result[:] }
