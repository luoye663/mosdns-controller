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

	"github.com/go-chi/chi/v5"
	"github.com/managed-dns/controller/internal/auth"
	"github.com/managed-dns/controller/internal/config"
	"github.com/managed-dns/controller/internal/mosdnsclient"
	"github.com/managed-dns/controller/internal/storage"
)

func testApp(t *testing.T) *App {
	return testAppWithClient(t, mosdnsclient.New("http://127.0.0.1", "test", time.Second))
}

func testAppWithClient(t *testing.T, client mosdnsclient.Client) *App {
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
	application := New(slog.Default(), cfg, store, client, "test")
	t.Cleanup(application.Close)
	return application
}

func TestSettingsAPIUsesRegistrySnapshot(t *testing.T) {
	var tags []string
	registry := mosdnsclient.RegistrySnapshot{SchemaVersion: 2, Version: 1, DefaultGroupID: "default_dns", Groups: []mosdnsclient.UpstreamGroup{{ID: "default_dns", Name: "Default", Enabled: true, Mode: "race", Concurrent: 1, Upstreams: []mosdnsclient.Upstream{{Tag: "default", Addr: "https://dns.example/dns-query"}}, ECS: mosdnsclient.ECSConfig{Mode: "off", Mask4: 24, Mask6: 48}, Cache: mosdnsclient.GroupCacheConfig{Enabled: true, Size: 1024}}}, Cache: mosdnsclient.RegistryCacheConfig{Enabled: true, Negative: mosdnsclient.NegativeCacheConfig{Enabled: true, TTL: 30}}, Protection: mosdnsclient.ProtectionConfig{GlobalMaxInFlight: 1000, DefaultGroupMaxInFlight: 100, DefaultGroupQueryTimeoutMS: 2000, OverloadAction: "servfail"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tags = append(tags, r.URL.Path)
		if r.Method == http.MethodPut {
			if err := json.NewDecoder(r.Body).Decode(&registry); err != nil {
				t.Errorf("decode registry request: %v", err)
			}
			registry.ExpectedCurrentVersion = 0
		}
		_ = json.NewEncoder(w).Encode(registry)
	}))
	defer server.Close()
	application := testAppWithClient(t, mosdnsclient.New(server.URL, "test", time.Second))
	if _, err := application.store.DB().Exec(`INSERT INTO admins(id,username,password_hash,created_at_ms,updated_at_ms) VALUES(1,'admin','x',1,1)`); err != nil {
		t.Fatal(err)
	}

	getRec := httptest.NewRecorder()
	application.settings(getRec, httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil))
	if getRec.Code != http.StatusOK || !bytes.Contains(getRec.Body.Bytes(), []byte(`"negative_cache_enabled":true`)) || !bytes.Contains(getRec.Body.Bytes(), []byte(`"negative_cache_ttl":30`)) || !bytes.Contains(getRec.Body.Bytes(), []byte(`"global_max_in_flight":1000`)) || !bytes.Contains(getRec.Body.Bytes(), []byte(`"default_group_max_in_flight":100`)) || !bytes.Contains(getRec.Body.Bytes(), []byte(`"default_group_query_timeout_ms":2000`)) || !bytes.Contains(getRec.Body.Bytes(), []byte(`"overload_action":"servfail"`)) {
		t.Fatalf("get settings=%d: %s", getRec.Code, getRec.Body.String())
	}

	body := `{"cache_enabled":true,"cache_ttl":0,"negative_cache_enabled":false,"negative_cache_ttl":60,"query_retention_days":7,"database_max_size_gib":2,"address_family_mode":"dual_stack","default_upstream_group_id":"default_dns","global_max_in_flight":2000,"default_group_max_in_flight":200,"default_group_query_timeout_ms":1500,"overload_action":"drop","upstream_registry_version":1}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewBufferString(body))
	req = req.WithContext(context.WithValue(req.Context(), adminKey, auth.Admin{ID: 1, Username: "admin"}))
	rec := httptest.NewRecorder()
	application.updateSettings(rec, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"negative_cache_enabled":false`)) || !bytes.Contains(rec.Body.Bytes(), []byte(`"negative_cache_ttl":60`)) || !bytes.Contains(rec.Body.Bytes(), []byte(`"global_max_in_flight":2000`)) || !bytes.Contains(rec.Body.Bytes(), []byte(`"default_group_max_in_flight":200`)) || !bytes.Contains(rec.Body.Bytes(), []byte(`"default_group_query_timeout_ms":1500`)) || !bytes.Contains(rec.Body.Bytes(), []byte(`"overload_action":"drop"`)) {
		t.Fatalf("update settings=%d: %s", rec.Code, rec.Body.String())
	}
	if registry.Protection != (mosdnsclient.ProtectionConfig{GlobalMaxInFlight: 2000, DefaultGroupMaxInFlight: 200, DefaultGroupQueryTimeoutMS: 1500, OverloadAction: "drop"}) || registry.Version != 2 {
		t.Fatalf("saved registry=%+v", registry)
	}
	want := []string{"/plugins/dynamic_upstreams/status", "/plugins/dynamic_upstreams/status", "/plugins/dynamic_upstreams/snapshot", "/plugins/dynamic_upstreams/status"}
	if len(tags) != len(want) || tags[0] != want[0] || tags[1] != want[1] {
		t.Fatalf("negative cache paths=%v", tags)
	}
}

func TestUpstreamGroupAPIMapsVersionConflictAndUnknownOutcome(t *testing.T) {
	registry := mosdnsclient.RegistrySnapshot{SchemaVersion: 1, Version: 1, DefaultGroupID: "default_dns", Groups: []mosdnsclient.UpstreamGroup{{ID: "default_dns", Name: "Default", Enabled: true, Mode: "race", Concurrent: 1, Upstreams: []mosdnsclient.Upstream{{Tag: "default", Addr: "https://dns.example/dns-query"}}, ECS: mosdnsclient.ECSConfig{Mode: "off", Mask4: 24, Mask6: 48}, Cache: mosdnsclient.GroupCacheConfig{Enabled: true, Size: 1024}}}, Cache: mosdnsclient.RegistryCacheConfig{Enabled: true, Negative: mosdnsclient.NegativeCacheConfig{Enabled: true, TTL: 30}}}
	puts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(registry)
	}))
	defer server.Close()
	application := testAppWithClient(t, mosdnsclient.New(server.URL, "test", time.Second))
	if _, err := application.store.DB().Exec(`INSERT INTO admins(id,username,password_hash,created_at_ms,updated_at_ms) VALUES(1,'admin','x',1,1)`); err != nil {
		t.Fatal(err)
	}
	group := registry.Groups[0]
	group.Name = "Changed"
	request := func(version uint64) *http.Request {
		body, _ := json.Marshal(map[string]any{"expected_current_version": version, "group": group})
		req := httptest.NewRequest(http.MethodPut, "/api/v1/upstream-groups/default_dns", bytes.NewReader(body))
		route := chi.NewRouteContext()
		route.URLParams.Add("id", "default_dns")
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, route)
		return req.WithContext(context.WithValue(ctx, adminKey, auth.Admin{ID: 1, Username: "admin"}))
	}
	conflict := httptest.NewRecorder()
	application.updateUpstreamGroup(conflict, request(99))
	if conflict.Code != http.StatusConflict || puts != 0 {
		t.Fatalf("conflict status=%d puts=%d body=%s", conflict.Code, puts, conflict.Body.String())
	}
	unknown := httptest.NewRecorder()
	application.updateUpstreamGroup(unknown, request(1))
	if unknown.Code != http.StatusBadGateway || puts != 1 {
		t.Fatalf("unknown status=%d puts=%d body=%s", unknown.Code, puts, unknown.Body.String())
	}
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
	refresh := httptest.NewRequest(http.MethodGet, "/api/v1/auth/csrf", nil)
	refresh.AddCookie(cookie)
	refreshRec := httptest.NewRecorder()
	application.PublicHandler().ServeHTTP(refreshRec, refresh)
	if refreshRec.Code != http.StatusOK {
		t.Fatalf("refresh csrf=%d: %s", refreshRec.Code, refreshRec.Body.String())
	}
	var refreshResponse struct {
		Data struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(refreshRec.Body).Decode(&refreshResponse); err != nil {
		t.Fatal(err)
	}
	if refreshResponse.Data.CSRFToken == "" || refreshResponse.Data.CSRFToken == response.Data.CSRFToken {
		t.Fatal("csrf refresh did not issue a new token")
	}
	logout.Header.Set("X-CSRF-Token", response.Data.CSRFToken)
	logoutRec = httptest.NewRecorder()
	application.PublicHandler().ServeHTTP(logoutRec, logout)
	if logoutRec.Code != http.StatusForbidden {
		t.Fatalf("logout with replaced csrf=%d", logoutRec.Code)
	}
	logout.Header.Set("X-CSRF-Token", refreshResponse.Data.CSRFToken)
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
	var setupResponse struct {
		Data struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(setupRec.Body).Decode(&setupResponse); err != nil {
		t.Fatal(err)
	}
	logout := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logout.AddCookie(setupRec.Result().Cookies()[0])
	logout.Header.Set("X-CSRF-Token", setupResponse.Data.CSRFToken)
	logoutRec := httptest.NewRecorder()
	handler.ServeHTTP(logoutRec, logout)
	if logoutRec.Code != http.StatusOK {
		t.Fatalf("logout=%d: %s", logoutRec.Code, logoutRec.Body.String())
	}
	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"admin","password":"password"}`))
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, login)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login after initial setup and logout=%d: %s", loginRec.Code, loginRec.Body.String())
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
func TestChangePasswordKeepsCurrentSessionAndRevokesOthers(t *testing.T) {
	application := testApp(t)
	if err := application.auth.CreateAdmin(context.Background(), "admin", "old-password"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for _, token := range []string{"current", "other"} {
		if _, err := application.store.DB().Exec(`INSERT INTO sessions(token_hash,admin_id,csrf_hash,created_at_ms,last_seen_at_ms,expires_at_ms) VALUES(?,?,?,?,?,?)`, authDigest(token), 1, authDigest("csrf"), now, now, now+60_000); err != nil {
			t.Fatal(err)
		}
	}
	if err := application.auth.ChangePasswordKeepingSession(context.Background(), 1, "old-password", "new-password", "current"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM sessions WHERE admin_id=1`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("sessions=%d err=%v", count, err)
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

func TestErrorMessagesAreChinese(t *testing.T) {
	tests := []struct {
		code, message, want string
	}{
		{"AUTH_REQUIRED", "authentication required", "需要登录"},
		{"VALIDATION_ERROR", "regexp exceeds 512 bytes", "正则表达式不能超过 512 字节"},
		{"VALIDATION_ERROR", "mosdns rejected request: group office_dns forward: upstream 1 address must be a valid [protocol://]host[:port][/path]", "上游地址格式无效，请输入 IP、主机名或带协议的完整地址"},
		{"VALIDATION_ERROR", "mosdns rejected request: group office_dns forward: upstream 1 uses an unsupported scheme", "上游地址协议不受支持，请使用 udp、tcp、tls、https 或 quic"},
		{"MOSDNS_UNAVAILABLE", "mosdns API GET /plugins/status: 503 Service Unavailable", "mosdns 服务不可用"},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			writeError(recorder, request, http.StatusBadRequest, test.code, test.message)
			var response struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
				t.Fatal(err)
			}
			if response.Error.Code != test.code || response.Error.Message != test.want {
				t.Fatalf("error = %#v, want code=%q message=%q", response.Error, test.code, test.want)
			}
		})
	}
}

func TestQueryStreamIsNotLimitedByHTTPTimeout(t *testing.T) {
	application := testApp(t)
	application.config.HTTP.Timeout = 10 * time.Millisecond
	req := httptest.NewRequest(http.MethodGet, "/api/v1/queries/stream", nil)
	rec := httptest.NewRecorder()
	application.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			t.Fatalf("query stream context expired: %v", r.Context().Err())
		case <-time.After(30 * time.Millisecond):
		}
	})).ServeHTTP(rec, req)
}

func TestInternalIngestRequiresSharedToken(t *testing.T) {
	application := testApp(t)
	body := `{"schema_version":2,"sender_id":"mosdns-test","sent_at_unix_ms":1,"events":[{"schema_version":2,"event_id":"ingest-1","timestamp_unix_ms":1,"process_started_at_unix_ms":1,"client_ip":"192.0.2.1","protocol":"udp","qname":"example.com","qtype":1,"qclass":1,"rcode":0,"route":"forward","route_source":"default","upstream_group":"default_dns","cache_hit":false,"snapshot_version":1,"access_rule_id":0,"route_rule_id":0,"answer_count":0,"latency_us":1,"error_code":"","error_text":""}]}`
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
func TestAuditPaginationValidationAndQueryHistoryClear(t *testing.T) {
	application := testApp(t)
	now := time.Now().UnixMilli()
	if _, err := application.store.DB().Exec(`INSERT INTO admins(id,username,password_hash,created_at_ms,updated_at_ms) VALUES(1,'admin','x',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := application.store.DB().Exec(`INSERT INTO sessions(token_hash,admin_id,csrf_hash,created_at_ms,last_seen_at_ms,expires_at_ms) VALUES(?,?,?,?,?,?)`, authDigest("session"), 1, authDigest("csrf"), now, now, now+60_000); err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: auth.CookieName, Value: "session"}
	invalidCursor := httptest.NewRequest(http.MethodGet, "/api/v1/audit-logs?cursor=invalid", nil)
	invalidCursor.AddCookie(cookie)
	invalidCursorRec := httptest.NewRecorder()
	application.PublicHandler().ServeHTTP(invalidCursorRec, invalidCursor)
	if invalidCursorRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status=%d: %s", invalidCursorRec.Code, invalidCursorRec.Body.String())
	}
	if _, err := application.store.DB().Exec(`INSERT INTO dns_queries(event_id,timestamp_unix_ms,client_ip,qname,qtype,qclass,route,route_source,cache_hit,snapshot_version,answer_count,latency_us,result_class,created_at_ms) VALUES('clear-endpoint',?,'192.0.2.10','example.com',1,1,'forward','default',0,1,0,1,'negative_answer',?)`, now, now); err != nil {
		t.Fatal(err)
	}
	clear := httptest.NewRequest(http.MethodPost, "/api/v1/settings/query-history/clear", bytes.NewBufferString(`{}`))
	clear.AddCookie(cookie)
	clear.Header.Set("X-CSRF-Token", "csrf")
	clearRec := httptest.NewRecorder()
	application.PublicHandler().ServeHTTP(clearRec, clear)
	if clearRec.Code != http.StatusOK {
		t.Fatalf("clear status=%d: %s", clearRec.Code, clearRec.Body.String())
	}
	var count int
	if err := application.store.DB().QueryRow(`SELECT COUNT(*) FROM dns_queries`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("query count=%d err=%v", count, err)
	}
}
func authDigest(value string) []byte { result := sha256.Sum256([]byte(value)); return result[:] }
