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
	return New(slog.Default(), cfg, store, mosdnsclient.New("http://127.0.0.1", "test", time.Second))
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
func authDigest(value string) []byte { result := sha256.Sum256([]byte(value)); return result[:] }
