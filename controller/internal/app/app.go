package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/managed-dns/controller/internal/auth"
	"github.com/managed-dns/controller/internal/config"
	"github.com/managed-dns/controller/internal/coordination"
	"github.com/managed-dns/controller/internal/mosdnsclient"
	"github.com/managed-dns/controller/internal/operations"
	"github.com/managed-dns/controller/internal/queryingest"
	"github.com/managed-dns/controller/internal/rules"
	"github.com/managed-dns/controller/internal/storage"
	"github.com/managed-dns/controller/internal/version"
	"github.com/managed-dns/controller/internal/web"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type App struct {
	logger  *slog.Logger
	config  config.Config
	store   *storage.Store
	auth    *auth.Service
	limiter *auth.LoginLimiter
	rules   *rules.Service
	ingest  *queryingest.Service
	ops     *operations.Service
}
type contextKey string

const requestIDKey contextKey = "request_id"
const adminKey contextKey = "admin"

func New(logger *slog.Logger, cfg config.Config, store *storage.Store, client mosdnsclient.Client, ingestToken string) *App {
	service := auth.New(store, cfg.Web.SessionTTL)
	limiter := auth.NewLoginLimiter()
	ingest := queryingest.New(store.DB(), ingestToken, cfg.Storage.Path)
	bindings := &coordination.UpstreamBindings{}
	return &App{logger: logger, config: cfg, store: store, auth: service, limiter: limiter, rules: rules.New(store, client, bindings), ingest: ingest, ops: operations.New(store.DB(), cfg.Storage.Path, client, ingest, bindings)}
}

func (a *App) PublicHandler() http.Handler {
	r := chi.NewRouter()
	r.Use(a.middleware)
	r.Get("/health/live", health)
	r.Get("/health/ready", a.ready)
	r.Get("/version", func(w http.ResponseWriter, r *http.Request) { writeData(w, r, http.StatusOK, version.Info()) })
	r.Handle("/metrics", promhttp.Handler())
	r.Route("/api/v1", func(api chi.Router) {
		api.Post("/auth/login", a.login)
		api.Get("/auth/bootstrap", a.bootstrapStatus)
		api.Post("/auth/bootstrap", a.bootstrap)
		api.Group(func(protected chi.Router) {
			protected.Use(a.requireSession)
			protected.Get("/auth/me", a.me)
			protected.Get("/auth/csrf", a.refreshCSRF)
			protected.Post("/auth/logout", a.requireCSRF(a.logout))
			protected.Post("/auth/change-password", a.requireCSRF(a.changePassword))
			protected.Get("/rules", a.listRules)
			protected.Post("/rules", a.requireCSRF(a.createRule))
			protected.Patch("/rules/{id}", a.requireCSRF(a.updateRule))
			protected.Delete("/rules/{id}", a.requireCSRF(a.deleteRule))
			protected.Post("/rules/batch", a.requireCSRF(a.batchRules))
			protected.Post("/rules/import/preview", a.previewImport)
			protected.Post("/rules/import/apply", a.requireCSRF(a.importRules))
			protected.Get("/rules/export", a.exportRules)
			protected.Post("/rules/test", a.testRule)
			protected.Get("/rule-subscriptions", a.listSubscriptions)
			protected.Post("/rule-subscriptions", a.requireCSRF(a.createSubscription))
			protected.Post("/rule-subscriptions/upload", a.requireCSRF(a.uploadSubscription))
			protected.Patch("/rule-subscriptions/{id}", a.requireCSRF(a.updateSubscription))
			protected.Put("/rule-subscriptions/{id}/binding", a.requireCSRF(a.updateSubscriptionBinding))
			protected.Post("/rule-subscriptions/{id}/refresh", a.requireCSRF(a.refreshSubscription))
			protected.Delete("/rule-subscriptions/{id}", a.requireCSRF(a.deleteSubscription))
			protected.Get("/rule-versions", a.listVersions)
			protected.Get("/rule-versions/{version}", a.versionDetail)
			protected.Post("/rule-versions/{version}/rollback", a.requireCSRF(a.rollback))
			protected.Post("/rule-versions/reconcile", a.requireCSRF(a.reconcile))
			protected.Get("/queries", a.queries)
			protected.Get("/queries/stream", a.queryStream)
			protected.Get("/queries/{eventID}/answer-ips", a.answerIPs)
			protected.Get("/stats/summary", a.summary)
			protected.Get("/stats/top-domains", a.statistics("domains"))
			protected.Get("/stats/top-clients", a.statistics("clients"))
			protected.Get("/stats/routes", a.statistics("routes"))
			protected.Get("/stats/rcode", a.statistics("rcode"))
			protected.Get("/stats/upstream-groups", a.statistics("upstream_groups"))
			protected.Get("/stats/latency", a.latency)
			protected.Get("/stats/upstream-runtime", a.upstreamRuntimeStatus)
			protected.Get("/devices", a.devices)
			protected.Patch("/devices/{id}", a.requireCSRF(a.updateDevice))
			protected.Get("/system/status", a.systemStatus)
			protected.Post("/system/cache/flush", a.requireCSRF(a.flushCaches))
			protected.Get("/upstream-groups", a.upstreamGroups)
			protected.Post("/upstream-groups", a.requireCSRF(a.createUpstreamGroup))
			protected.Put("/upstream-groups/{id}", a.requireCSRF(a.updateUpstreamGroup))
			protected.Delete("/upstream-groups/{id}", a.requireCSRF(a.deleteUpstreamGroup))
			protected.Post("/upstream-groups/{id}/cache/flush", a.requireCSRF(a.flushUpstreamGroup))
			protected.Get("/settings", a.settings)
			protected.Put("/settings", a.requireCSRF(a.updateSettings))
			protected.Post("/settings/query-history/clear", a.requireCSRF(a.clearQueryHistory))
			protected.Get("/audit-logs", a.auditLogs)
		})
	})
	r.Handle("/*", web.Handler())
	return r
}
func (a *App) InternalHandler() http.Handler {
	r := chi.NewRouter()
	r.Use(a.middleware)
	r.Get("/health/ready", a.ready)
	r.Get("/internal/v1/health/ready", a.ready)
	r.Post("/internal/v1/query-events/batch", a.ingestEvents)
	return r
}
func (a *App) Close()                                        { a.ingest.Close() }
func (a *App) Reconcile(ctx context.Context) (string, error) { return a.rules.Reconcile(ctx) }
func (a *App) SyncSettings(ctx context.Context) error        { return a.ops.SyncSettings(ctx) }
func (a *App) StartMaintenance()                             { a.ingest.StartMaintenance() }
func (a *App) RefreshSubscriptions(ctx context.Context)      { a.rules.RefreshDue(ctx) }

func (a *App) ready(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DB().PingContext(r.Context()); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is unavailable")
		return
	}
	writeData(w, r, http.StatusOK, map[string]string{"status": "ok"})
}
func health(w http.ResponseWriter, r *http.Request) {
	writeData(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	clientIP := remoteIP(r)
	if !a.limiter.Allowed(clientIP) {
		writeError(w, r, http.StatusTooManyRequests, "LOGIN_RATE_LIMITED", "too many login attempts")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	token, csrf, err := a.auth.Login(r.Context(), input.Username, input.Password, clientIP, r.UserAgent())
	if err != nil {
		a.limiter.Failed(clientIP)
		writeError(w, r, http.StatusUnauthorized, "AUTH_REQUIRED", "invalid username or password")
		return
	}
	a.limiter.Succeeded(clientIP)
	http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: token, Path: "/", HttpOnly: true, Secure: a.config.Web.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: int(a.config.Web.SessionTTL.Seconds())})
	writeData(w, r, http.StatusOK, map[string]string{"csrf_token": csrf})
}
func (a *App) bootstrapStatus(w http.ResponseWriter, r *http.Request) {
	required, err := a.auth.NeedsInitialAdmin(r.Context())
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is unavailable")
		return
	}
	writeData(w, r, http.StatusOK, map[string]bool{"required": required})
}
func (a *App) bootstrap(w http.ResponseWriter, r *http.Request) {
	clientIP := remoteIP(r)
	if !a.limiter.Allowed(clientIP) {
		writeError(w, r, http.StatusTooManyRequests, "LOGIN_RATE_LIMITED", "too many setup attempts")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	// There is no authenticated session before initial setup, so CSRF cannot apply.
	if err := a.auth.CreateInitialAdmin(r.Context(), input.Username, input.Password); err != nil {
		if errors.Is(err, auth.ErrInitialAdminExists) {
			writeError(w, r, http.StatusConflict, "INITIAL_SETUP_COMPLETE", "an administrator already exists")
			return
		}
		a.limiter.Failed(clientIP)
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	token, csrf, err := a.auth.Login(r.Context(), input.Username, input.Password, clientIP, r.UserAgent())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "initial administrator was created but session creation failed")
		return
	}
	a.limiter.Succeeded(clientIP)
	http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: token, Path: "/", HttpOnly: true, Secure: a.config.Web.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: int(a.config.Web.SessionTTL.Seconds())})
	writeData(w, r, http.StatusCreated, map[string]string{"csrf_token": csrf})
}
func (a *App) me(w http.ResponseWriter, r *http.Request) {
	writeData(w, r, http.StatusOK, r.Context().Value(adminKey).(auth.Admin))
}
func (a *App) refreshCSRF(w http.ResponseWriter, r *http.Request) {
	csrf, err := a.auth.RefreshCSRF(r.Context(), sessionToken(r))
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeData(w, r, http.StatusOK, map[string]string{"csrf_token": csrf})
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	_ = a.auth.Logout(r.Context(), sessionToken(r))
	http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: "", Path: "/", HttpOnly: true, Secure: a.config.Web.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	writeData(w, r, http.StatusOK, map[string]bool{"logged_out": true})
}
func (a *App) changePassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	admin := r.Context().Value(adminKey).(auth.Admin)
	if err := a.auth.ChangePasswordKeepingSession(r.Context(), admin.ID, input.CurrentPassword, input.NewPassword, sessionToken(r)); err != nil {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	if err := a.ops.Audit(r.Context(), admin.ID, "update", "password", strconv.FormatInt(admin.ID, 10), requestID(r), remoteIP(r), "success", ""); err != nil {
		a.logger.Error("audit password change", "error", err)
	}
	writeData(w, r, http.StatusOK, map[string]bool{"changed": true})
}
func (a *App) listRules(w http.ResponseWriter, r *http.Request) {
	values, err := a.rules.List(r.Context())
	if err != nil {
		writeError(w, r, 500, "INTERNAL_ERROR", "list rules failed")
		return
	}
	writeData(w, r, 200, map[string]any{"items": values})
}
func (a *App) exportRules(w http.ResponseWriter, r *http.Request) { a.listRules(w, r) }
func (a *App) createRule(w http.ResponseWriter, r *http.Request) {
	var value rules.Rule
	if !decodeJSON(w, r, &value) {
		return
	}
	a.publishRule(w, r, func(admin auth.Admin) (rules.Version, error) {
		return a.rules.Create(r.Context(), value, admin.ID, requestID(r), remoteIP(r))
	})
}
func (a *App) updateRule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var value rules.Rule
	if !decodeJSON(w, r, &value) {
		return
	}
	a.publishRule(w, r, func(admin auth.Admin) (rules.Version, error) {
		return a.rules.Update(r.Context(), id, value, admin.ID, requestID(r), remoteIP(r))
	})
}
func (a *App) deleteRule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	a.publishRule(w, r, func(admin auth.Admin) (rules.Version, error) {
		return a.rules.Delete(r.Context(), id, admin.ID, requestID(r), remoteIP(r))
	})
}
func (a *App) batchRules(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Operation                       string  `json:"operation"`
		IDs                             []int64 `json:"ids"`
		ExpectedUpstreamRegistryVersion *uint64 `json:"expected_upstream_registry_version,omitempty"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	a.publishRule(w, r, func(admin auth.Admin) (rules.Version, error) {
		return a.rules.Batch(r.Context(), input.Operation, input.IDs, input.ExpectedUpstreamRegistryVersion, admin.ID, requestID(r), remoteIP(r))
	})
}
func (a *App) previewImport(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Rules []rules.Rule `json:"rules"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.Rules) > 200000 {
		writeError(w, r, 413, "LIMIT_EXCEEDED", "rule limit exceeded")
		return
	}
	writeData(w, r, 200, map[string]any{"rules": input.Rules, "count": len(input.Rules)})
}
func (a *App) importRules(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Rules []rules.Rule `json:"rules"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	a.publishRule(w, r, func(admin auth.Admin) (rules.Version, error) {
		return a.rules.Import(r.Context(), input.Rules, admin.ID, requestID(r), remoteIP(r))
	})
}
func (a *App) rollback(w http.ResponseWriter, r *http.Request) {
	version, ok := pathID(w, r, "version")
	if !ok {
		return
	}
	var input struct {
		ExpectedUpstreamRegistryVersion *uint64 `json:"expected_upstream_registry_version"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	a.publishRule(w, r, func(admin auth.Admin) (rules.Version, error) {
		return a.rules.Rollback(r.Context(), uint64(version), input.ExpectedUpstreamRegistryVersion, admin.ID, requestID(r), remoteIP(r))
	})
}
func (a *App) listVersions(w http.ResponseWriter, r *http.Request) {
	values, err := a.rules.Versions(r.Context())
	if err != nil {
		writeError(w, r, 500, "INTERNAL_ERROR", "list versions failed")
		return
	}
	writeData(w, r, 200, map[string]any{"items": values})
}
func (a *App) versionDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "version")
	if !ok {
		return
	}
	value, err := a.rules.Version(r.Context(), uint64(id))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, 404, "NOT_FOUND", "version not found")
		return
	}
	if err != nil {
		writeError(w, r, 500, "INTERNAL_ERROR", "read version failed")
		return
	}
	writeData(w, r, 200, value)
}
func (a *App) reconcile(w http.ResponseWriter, r *http.Request) {
	state, err := a.rules.Reconcile(r.Context())
	if err != nil {
		writeError(w, r, 503, "MOSDNS_UNAVAILABLE", err.Error())
		return
	}
	writeData(w, r, 200, map[string]string{"state": state})
}
func (a *App) testRule(w http.ResponseWriter, r *http.Request) {
	var input struct {
		QName string `json:"qname"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	result, err := a.rules.Test(r.Context(), input.QName)
	if err != nil {
		writeError(w, r, 502, "MOSDNS_UNAVAILABLE", err.Error())
		return
	}
	writeData(w, r, 200, result)
}

// ingestEvents 仅由 Docker 内部的 mosdns 调用，使用共享 token 而不是管理员 session。
func (a *App) ingestEvents(w http.ResponseWriter, r *http.Request) {
	if !a.ingest.Authorized(r.Header.Get("Authorization")) {
		writeError(w, r, http.StatusUnauthorized, "AUTH_REQUIRED", "invalid internal token")
		return
	}
	var batch queryingest.Batch
	if !decodeJSON(w, r, &batch) {
		return
	}
	if err := a.ingest.Enqueue(batch); err != nil {
		if errors.Is(err, queryingest.ErrOverloaded) {
			writeError(w, r, http.StatusServiceUnavailable, "INGEST_OVERLOADED", "query ingestion is overloaded")
			return
		}
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
	}
	writeData(w, r, http.StatusAccepted, map[string]int{"accepted": len(batch.Events), "rejected": 0})
}
func (a *App) queries(w http.ResponseWriter, r *http.Request) {
	query, err := queryFromRequest(r)
	if err != nil {
		writeError(w, r, 400, "VALIDATION_ERROR", err.Error())
		return
	}
	page, err := a.ingest.Queries(r.Context(), query)
	if err != nil {
		writeError(w, r, 400, "VALIDATION_ERROR", err.Error())
		return
	}
	writeData(w, r, 200, page)
}
func (a *App) answerIPs(w http.ResponseWriter, r *http.Request) {
	eventID := chi.URLParam(r, "eventID")
	if len(eventID) == 0 || len(eventID) > 128 {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid event identifier")
		return
	}
	diagnostics, ok := a.ingest.AnswerDiagnostics(eventID)
	if !ok {
		writeError(w, r, http.StatusNotFound, "ANSWER_DIAGNOSTICS_UNAVAILABLE", "answer diagnostics are no longer available in memory")
		return
	}
	writeData(w, r, http.StatusOK, diagnostics)
}

// queryStream 的写入在 HTTP goroutine 内完成；broadcaster 从不等待该 goroutine。
func (a *App) queryStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, 500, "INTERNAL_ERROR", "streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	query, err := queryFromRequest(r)
	if err != nil {
		writeError(w, r, 400, "VALIDATION_ERROR", err.Error())
		return
	}
	sub, err := a.ingest.Subscribe(query, r.Header.Get("Last-Event-ID"))
	if err != nil {
		writeError(w, r, 400, "VALIDATION_ERROR", err.Error())
		return
	}
	defer sub.Close()
	_, _ = w.Write([]byte("event: connected\ndata: {}\n\n"))
	writeEvent := func(event queryingest.StoredEvent) bool {
		data, err := json.Marshal(event)
		if err != nil {
			return false
		}
		_, _ = w.Write([]byte("event: query\nid: " + event.EventID + "\ndata: " + string(data) + "\n\n"))
		flusher.Flush()
		return true
	}
	flusher.Flush()
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case event, ok := <-sub.C:
			if !ok {
				return
			}
			if !writeEvent(event) {
				return
			}
		case now := <-heartbeat.C:
			_, _ = w.Write([]byte("event: heartbeat\ndata: {\"time\":\"" + now.UTC().Format(time.RFC3339) + "\"}\n\n"))
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func queryFromRequest(r *http.Request) (queryingest.Query, error) {
	values := r.URL.Query()
	parseInt := func(name string) (int, error) {
		if values.Get(name) == "" {
			return 0, nil
		}
		value, err := strconv.Atoi(values.Get(name))
		if err != nil {
			return 0, errors.New("invalid " + name)
		}
		return value, nil
	}
	parseMS := func(name string) (int64, error) {
		if values.Get(name) == "" {
			return 0, nil
		}
		value, err := strconv.ParseInt(values.Get(name), 10, 64)
		if err != nil {
			return 0, errors.New("invalid " + name)
		}
		return value, nil
	}
	parseBool := func(name string) (*bool, error) {
		if values.Get(name) == "" {
			return nil, nil
		}
		value, err := strconv.ParseBool(values.Get(name))
		if err != nil {
			return nil, errors.New("invalid " + name)
		}
		return &value, nil
	}
	limit, err := parseInt("limit")
	if err != nil {
		return queryingest.Query{}, err
	}
	qtype, err := parseInt("qtype")
	if err != nil {
		return queryingest.Query{}, err
	}
	rcode, err := parseInt("rcode")
	if err != nil {
		return queryingest.Query{}, err
	}
	from, err := parseMS("from")
	if err != nil {
		return queryingest.Query{}, err
	}
	to, err := parseMS("to")
	if err != nil {
		return queryingest.Query{}, err
	}
	cacheHit, err := parseBool("cache_hit")
	if err != nil {
		return queryingest.Query{}, err
	}
	hasError, err := parseBool("has_error")
	if err != nil {
		return queryingest.Query{}, err
	}
	var rcodePtr *int
	if values.Get("rcode") != "" {
		rcodePtr = &rcode
	}
	qname := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(values.Get("qname"))), ".")
	return queryingest.Query{Limit: limit, Cursor: values.Get("cursor"), FromMS: from, ToMS: to, ClientIP: values.Get("client_ip"), Route: values.Get("route"), RouteSource: values.Get("route_source"), UpstreamGroup: values.Get("upstream_group"), UpstreamTag: values.Get("upstream_tag"), Protocol: values.Get("protocol"), QName: qname, QNameMatch: values.Get("qname_match"), QType: qtype, RCode: rcodePtr, CacheHit: cacheHit, HasError: hasError, ResultClass: values.Get("result_class")}, nil
}
func (a *App) summary(w http.ResponseWriter, r *http.Request) {
	result, err := a.ingest.Summary(r.Context())
	if err != nil {
		writeError(w, r, 500, "INTERNAL_ERROR", "read summary failed")
		return
	}
	writeData(w, r, 200, result)
}
func (a *App) statistics(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		result, err := a.ingest.Top(r.Context(), kind, limit)
		if err != nil {
			writeError(w, r, 500, "INTERNAL_ERROR", "read statistics failed")
			return
		}
		writeData(w, r, 200, map[string]any{"items": result})
	}
}
func (a *App) latency(w http.ResponseWriter, r *http.Request) {
	result, err := a.ingest.Latency(r.Context())
	if err != nil {
		writeError(w, r, 500, "INTERNAL_ERROR", "read latency failed")
		return
	}
	writeData(w, r, 200, map[string]any{"items": result})
}
func (a *App) devices(w http.ResponseWriter, r *http.Request) {
	items, err := a.ops.Devices(r.Context())
	if err != nil {
		writeError(w, r, 500, "INTERNAL_ERROR", "list devices failed")
		return
	}
	writeData(w, r, 200, map[string]any{"items": items})
}
func (a *App) upstreamRuntimeStatus(w http.ResponseWriter, r *http.Request) {
	status, err := a.ops.UpstreamRuntimeStatus(r.Context())
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "MOSDNS_UNAVAILABLE", "upstream runtime status is unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeData(w, r, http.StatusOK, status)
}
func (a *App) updateDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var patch operations.DevicePatch
	if !decodeJSON(w, r, &patch) {
		return
	}
	admin := r.Context().Value(adminKey).(auth.Admin)
	device, err := a.ops.UpdateDevice(r.Context(), id, patch, admin.ID, requestID(r), remoteIP(r))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, 404, "NOT_FOUND", "device not found")
		return
	}
	if err != nil {
		writeError(w, r, 400, "VALIDATION_ERROR", err.Error())
		return
	}
	writeData(w, r, 200, device)
}
func (a *App) systemStatus(w http.ResponseWriter, r *http.Request) {
	status := a.ops.SystemStatus(r.Context())
	// 日志队列只提供瞬时深度，不能影响 ingest worker 或 SQLite 写入。
	writeData(w, r, 200, map[string]any{"controller": version.Info(), "controller_memory_rss_bytes": status.ControllerMemoryRSSBytes, "database": status.Database, "mosdns": status.Mosdns, "mosdns_error": status.MosdnsError, "audit": status.Audit, "audit_error": status.AuditError, "ingest_queue_depth": a.ingest.QueueDepth(), "last_successful_ingest_at": status.LastSuccessfulIngest, "last_retention_at": status.LastRetention})
}
func (a *App) flushCaches(w http.ResponseWriter, r *http.Request) {
	admin := r.Context().Value(adminKey).(auth.Admin)
	if err := a.ops.FlushCaches(r.Context(), admin.ID, requestID(r), remoteIP(r)); err != nil {
		writeError(w, r, 502, "MOSDNS_UNAVAILABLE", "cache flush failed")
		return
	}
	writeData(w, r, 200, map[string]bool{"flushed": true})
}
func (a *App) upstreamGroups(w http.ResponseWriter, r *http.Request) {
	snapshot, err := a.ops.UpstreamGroups(r.Context())
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "MOSDNS_UNAVAILABLE", "upstream registry runtime configuration is unavailable")
		return
	}
	writeData(w, r, http.StatusOK, snapshot)
}
func (a *App) createUpstreamGroup(w http.ResponseWriter, r *http.Request) {
	var input operations.UpstreamGroupWrite
	if !decodeJSON(w, r, &input) {
		return
	}
	a.writeUpstreamGroupMutation(w, r, http.StatusCreated, func(admin auth.Admin) (mosdnsclient.RegistrySnapshot, error) {
		return a.ops.CreateUpstreamGroup(r.Context(), input, admin.ID, requestID(r), remoteIP(r))
	})
}
func (a *App) updateUpstreamGroup(w http.ResponseWriter, r *http.Request) {
	var input operations.UpstreamGroupWrite
	if !decodeJSON(w, r, &input) {
		return
	}
	a.writeUpstreamGroupMutation(w, r, http.StatusOK, func(admin auth.Admin) (mosdnsclient.RegistrySnapshot, error) {
		return a.ops.UpdateUpstreamGroup(r.Context(), chi.URLParam(r, "id"), input, admin.ID, requestID(r), remoteIP(r))
	})
}
func (a *App) deleteUpstreamGroup(w http.ResponseWriter, r *http.Request) {
	var input operations.VersionPrecondition
	if !decodeJSON(w, r, &input) {
		return
	}
	a.writeUpstreamGroupMutation(w, r, http.StatusOK, func(admin auth.Admin) (mosdnsclient.RegistrySnapshot, error) {
		return a.ops.DeleteUpstreamGroup(r.Context(), chi.URLParam(r, "id"), input.ExpectedCurrentVersion, admin.ID, requestID(r), remoteIP(r))
	})
}
func (a *App) writeUpstreamGroupMutation(w http.ResponseWriter, r *http.Request, status int, operation func(auth.Admin) (mosdnsclient.RegistrySnapshot, error)) {
	value, err := operation(r.Context().Value(adminKey).(auth.Admin))
	switch {
	case errors.Is(err, mosdnsclient.ErrConflict):
		writeError(w, r, http.StatusConflict, "VERSION_CONFLICT", "upstream registry changed; refresh and retry")
	case errors.Is(err, operations.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "upstream group not found")
	case errors.Is(err, operations.ErrProtectedGroup), errors.Is(err, operations.ErrGroupReferenced):
		writeError(w, r, http.StatusConflict, "UPSTREAM_GROUP_PROTECTED", err.Error())
	case errors.Is(err, mosdnsclient.ErrUnknown):
		writeError(w, r, http.StatusBadGateway, "MOSDNS_UNAVAILABLE", "upstream registry update outcome could not be reconciled")
	case errors.Is(err, operations.ErrValidation), errors.Is(err, mosdnsclient.ErrRejected):
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
	case err != nil:
		writeError(w, r, http.StatusBadGateway, "MOSDNS_UNAVAILABLE", "upstream registry runtime configuration is unavailable")
	default:
		writeData(w, r, status, value)
	}
}
func (a *App) flushUpstreamGroup(w http.ResponseWriter, r *http.Request) {
	var input operations.VersionPrecondition
	if !decodeJSON(w, r, &input) {
		return
	}
	admin := r.Context().Value(adminKey).(auth.Admin)
	err := a.ops.FlushUpstreamGroup(r.Context(), chi.URLParam(r, "id"), input.ExpectedCurrentVersion, admin.ID, requestID(r), remoteIP(r))
	if errors.Is(err, mosdnsclient.ErrConflict) {
		writeError(w, r, http.StatusConflict, "VERSION_CONFLICT", "upstream registry changed; refresh and retry")
		return
	}
	if errors.Is(err, operations.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "upstream group not found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "MOSDNS_UNAVAILABLE", "cache flush failed")
		return
	}
	writeData(w, r, http.StatusOK, map[string]bool{"flushed": true})
}
func (a *App) settings(w http.ResponseWriter, r *http.Request) {
	settings, err := a.ops.Settings(r.Context())
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "MOSDNS_UNAVAILABLE", "read settings failed")
		return
	}
	writeData(w, r, 200, settings)
}
func (a *App) updateSettings(w http.ResponseWriter, r *http.Request) {
	var settings operations.Settings
	if !decodeJSON(w, r, &settings) {
		return
	}
	admin := r.Context().Value(adminKey).(auth.Admin)
	if err := a.ops.UpdateSettings(r.Context(), settings, admin.ID, requestID(r), remoteIP(r)); err != nil {
		if errors.Is(err, mosdnsclient.ErrConflict) {
			writeError(w, r, http.StatusConflict, "VERSION_CONFLICT", "upstream registry changed; refresh and retry")
			return
		}
		writeError(w, r, 502, "SETTINGS_APPLY_FAILED", err.Error())
		return
	}
	applied, err := a.ops.Settings(r.Context())
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "SETTINGS_APPLY_FAILED", "settings were applied but could not be read back")
		return
	}
	writeData(w, r, 200, applied)
}
func (a *App) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	items, err := a.rules.Subscriptions(r.Context(), r.URL.Query().Get("category"), r.URL.Query().Get("action"))
	if err != nil {
		writeError(w, r, 400, "VALIDATION_ERROR", err.Error())
		return
	}
	writeData(w, r, 200, map[string]any{"items": items})
}
func (a *App) createSubscription(w http.ResponseWriter, r *http.Request) {
	var input rules.SubscriptionInput
	if !decodeJSON(w, r, &input) {
		return
	}
	admin := r.Context().Value(adminKey).(auth.Admin)
	item, published, err := a.rules.CreateURLSubscription(r.Context(), input, admin.ID, requestID(r), remoteIP(r))
	if errors.Is(err, mosdnsclient.ErrUnknown) {
		writeData(w, r, http.StatusAccepted, map[string]any{"subscription": item, "version": published})
		return
	}
	if errors.Is(err, mosdnsclient.ErrConflict) {
		writeError(w, r, http.StatusConflict, "VERSION_CONFLICT", "upstream registry changed; refresh and retry")
		return
	}
	if err != nil {
		writeError(w, r, 400, "SUBSCRIPTION_CREATE_FAILED", err.Error())
		return
	}
	writeData(w, r, 200, map[string]any{"subscription": item, "version": published})
}
func (a *App) uploadSubscription(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(20 << 20); err != nil {
		writeError(w, r, 400, "VALIDATION_ERROR", "invalid subscription upload")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, r, 400, "VALIDATION_ERROR", "subscription file is required")
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, (20<<20)+1))
	if err != nil || len(content) > 20<<20 {
		writeError(w, r, 413, "LIMIT_EXCEEDED", "subscription file exceeds 20 MiB")
		return
	}
	interval, err := strconv.Atoi(r.FormValue("refresh_interval_seconds"))
	if err != nil && r.FormValue("refresh_interval_seconds") != "" {
		writeError(w, r, 400, "VALIDATION_ERROR", "invalid refresh interval")
		return
	}
	admin := r.Context().Value(adminKey).(auth.Admin)
	input := rules.SubscriptionInput{Category: r.FormValue("category"), Action: r.FormValue("action"), Name: r.FormValue("name"), RefreshIntervalSeconds: interval, Enabled: r.FormValue("enabled") != "false", UpstreamGroupID: r.FormValue("upstream_group_id")}
	if value := r.FormValue("priority"); value != "" {
		priority, parseErr := strconv.Atoi(value)
		if parseErr != nil {
			writeError(w, r, 400, "VALIDATION_ERROR", "invalid priority")
			return
		}
		input.Priority = &priority
	}
	if value := r.FormValue("expected_upstream_registry_version"); value != "" {
		registryVersion, parseErr := strconv.ParseUint(value, 10, 64)
		if parseErr != nil {
			writeError(w, r, 400, "VALIDATION_ERROR", "invalid upstream registry version")
			return
		}
		input.ExpectedUpstreamRegistryVersion = &registryVersion
	}
	item, published, err := a.rules.CreateUploadSubscription(r.Context(), input, header.Filename, content, admin.ID, requestID(r), remoteIP(r))
	if errors.Is(err, mosdnsclient.ErrUnknown) {
		writeData(w, r, http.StatusAccepted, map[string]any{"subscription": item, "version": published})
		return
	}
	if errors.Is(err, mosdnsclient.ErrConflict) {
		writeError(w, r, http.StatusConflict, "VERSION_CONFLICT", "upstream registry changed; refresh and retry")
		return
	}
	if err != nil {
		writeError(w, r, 400, "SUBSCRIPTION_UPLOAD_FAILED", err.Error())
		return
	}
	writeData(w, r, 200, map[string]any{"subscription": item, "version": published})
}
func (a *App) updateSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	admin := r.Context().Value(adminKey).(auth.Admin)
	item, published, err := a.rules.SetSubscriptionEnabled(r.Context(), id, input.Enabled, admin.ID, requestID(r), remoteIP(r))
	if errors.Is(err, mosdnsclient.ErrUnknown) {
		writeData(w, r, http.StatusAccepted, map[string]any{"subscription": item, "version": published})
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, 404, "NOT_FOUND", "subscription not found")
		return
	}
	if err != nil {
		writeError(w, r, 400, "SUBSCRIPTION_UPDATE_FAILED", err.Error())
		return
	}
	writeData(w, r, 200, map[string]any{"subscription": item, "version": published})
}
func (a *App) refreshSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	admin := r.Context().Value(adminKey).(auth.Admin)
	item, published, err := a.rules.RefreshSubscription(r.Context(), id, admin.ID, requestID(r), remoteIP(r))
	if errors.Is(err, mosdnsclient.ErrUnknown) {
		writeData(w, r, http.StatusAccepted, map[string]any{"subscription": item, "version": published})
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, 404, "NOT_FOUND", "subscription not found")
		return
	}
	if errors.Is(err, mosdnsclient.ErrConflict) {
		writeError(w, r, http.StatusConflict, "VERSION_CONFLICT", "subscription changed while refreshing; refresh and retry")
		return
	}
	if err != nil {
		writeError(w, r, 400, "SUBSCRIPTION_REFRESH_FAILED", err.Error())
		return
	}
	writeData(w, r, 200, map[string]any{"subscription": item, "version": published})
}
func (a *App) updateSubscriptionBinding(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var input rules.BindingInput
	if !decodeJSON(w, r, &input) {
		return
	}
	admin := r.Context().Value(adminKey).(auth.Admin)
	item, published, err := a.rules.UpdateSubscriptionBinding(r.Context(), id, input, admin.ID, requestID(r), remoteIP(r))
	if errors.Is(err, mosdnsclient.ErrUnknown) {
		writeData(w, r, http.StatusAccepted, map[string]any{"subscription": item, "version": published})
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "subscription not found")
		return
	}
	if errors.Is(err, mosdnsclient.ErrConflict) {
		writeError(w, r, http.StatusConflict, "VERSION_CONFLICT", "upstream registry changed; refresh and retry")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "SUBSCRIPTION_UPDATE_FAILED", err.Error())
		return
	}
	writeData(w, r, http.StatusOK, map[string]any{"subscription": item, "version": published})
}
func (a *App) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	admin := r.Context().Value(adminKey).(auth.Admin)
	published, err := a.rules.DeleteSubscription(r.Context(), id, admin.ID, requestID(r), remoteIP(r))
	if errors.Is(err, mosdnsclient.ErrUnknown) {
		writeData(w, r, http.StatusAccepted, published)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, 404, "NOT_FOUND", "subscription not found")
		return
	}
	if err != nil {
		writeError(w, r, 400, "SUBSCRIPTION_DELETE_FAILED", err.Error())
		return
	}
	writeData(w, r, 200, published)
}
func (a *App) auditLogs(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil && r.URL.Query().Get("limit") != "" {
		writeError(w, r, 400, "VALIDATION_ERROR", "invalid limit")
		return
	}
	page, err := a.ops.AuditLogs(r.Context(), operations.AuditLogQuery{Limit: limit, Cursor: r.URL.Query().Get("cursor")})
	if errors.Is(err, operations.ErrInvalidCursor) {
		writeError(w, r, 400, "VALIDATION_ERROR", "invalid cursor")
		return
	}
	if err != nil {
		writeError(w, r, 500, "INTERNAL_ERROR", "list audit logs failed")
		return
	}
	writeData(w, r, 200, page)
}
func (a *App) clearQueryHistory(w http.ResponseWriter, r *http.Request) {
	admin := r.Context().Value(adminKey).(auth.Admin)
	if err := a.ops.ClearQueryHistory(r.Context(), admin.ID, requestID(r), remoteIP(r)); err != nil {
		writeError(w, r, 500, "INTERNAL_ERROR", "clear query history failed")
		return
	}
	writeData(w, r, http.StatusOK, map[string]bool{"cleared": true})
}
func (a *App) publishRule(w http.ResponseWriter, r *http.Request, operation func(auth.Admin) (rules.Version, error)) {
	value, err := operation(r.Context().Value(adminKey).(auth.Admin))
	if errors.Is(err, mosdnsclient.ErrUnknown) {
		writeData(w, r, http.StatusAccepted, value)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, 404, "NOT_FOUND", "rule or version not found")
		return
	}
	if errors.Is(err, rules.ErrRuleConflict) {
		writeError(w, r, http.StatusConflict, "RULE_CONFLICT", "rule changed; refresh and retry")
		return
	}
	if errors.Is(err, mosdnsclient.ErrConflict) {
		writeError(w, r, http.StatusConflict, "VERSION_CONFLICT", "upstream registry changed; refresh and retry")
		return
	}
	if err != nil {
		writeError(w, r, 400, "VALIDATION_ERROR", err.Error())
		return
	}
	writeData(w, r, 200, value)
}
func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	value, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || value <= 0 {
		writeError(w, r, 400, "VALIDATION_ERROR", "invalid identifier")
		return 0, false
	}
	return value, true
}
func requestID(r *http.Request) string {
	value, _ := r.Context().Value(requestIDKey).(string)
	return value
}

func (a *App) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, err := a.auth.Session(r.Context(), sessionToken(r))
		if err != nil {
			writeError(w, r, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), adminKey, session.Admin)))
	})
}
func (a *App) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.auth.VerifyCSRF(r.Context(), sessionToken(r), r.Header.Get("X-CSRF-Token")) {
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "csrf token is invalid")
			return
		}
		next(w, r)
	}
}
func sessionToken(r *http.Request) string {
	cookie, err := r.Cookie(auth.CookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}
func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "LIMIT_EXCEEDED", "request body is too large")
			return false
		}
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON request body")
		return false
	}
	return true
}

func (a *App) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		id := newRequestID()
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey, id))
		w.Header().Set("X-Request-ID", id)
		r.Body = http.MaxBytesReader(w, r.Body, a.config.HTTP.MaxBodyBytes)
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.Error("panic recovered", "request_id", id, "panic", recovered, "stack", string(debug.Stack()))
				writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
			}
		}()
		if r.URL.Path == "/api/v1/queries/stream" {
			next.ServeHTTP(w, r)
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), a.config.HTTP.Timeout)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		}
		a.logger.Info("http request", "request_id", id, "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds())
	})
}
func newRequestID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(bytes)
}
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}
func writeData(w http.ResponseWriter, r *http.Request, status int, body any) {
	writeJSON(w, status, map[string]any{"data": body, "request_id": r.Context().Value(requestIDKey)})
}
func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": chineseErrorMessage(code, message)}, "request_id": r.Context().Value(requestIDKey)})
}

// chineseErrorMessage keeps error codes stable for API clients while ensuring
// internal service and upstream errors are never exposed as English UI text.
func chineseErrorMessage(code, message string) string {
	if localized, ok := map[string]string{
		"database is unavailable":                                       "数据库暂不可用",
		"too many login attempts":                                       "登录尝试次数过多，请稍后再试",
		"invalid username or password":                                  "用户名或密码错误",
		"too many setup attempts":                                       "初始化尝试次数过多，请稍后再试",
		"an administrator already exists":                               "管理员已存在",
		"initial administrator was created but session creation failed": "初始管理员已创建，但会话创建失败",
		"list rules failed":                                             "读取规则失败",
		"rule limit exceeded":                                           "规则数量超出限制",
		"list versions failed":                                          "读取规则版本失败",
		"version not found":                                             "规则版本不存在",
		"read version failed":                                           "读取规则版本失败",
		"invalid internal token":                                        "内部令牌无效",
		"query ingestion is overloaded":                                 "查询事件接收服务繁忙",
		"invalid event identifier":                                      "事件标识无效",
		"answer diagnostics are no longer available in memory":          "应答诊断数据已不在内存中",
		"streaming is unavailable":                                      "流式传输不可用",
		"read summary failed":                                           "读取汇总统计失败",
		"read statistics failed":                                        "读取统计数据失败",
		"read latency failed":                                           "读取延迟统计失败",
		"list devices failed":                                           "读取设备列表失败",
		"device not found":                                              "设备不存在",
		"cache flush failed":                                            "缓存清理失败",
		"upstream or ECS runtime configuration is unavailable; deploy matching mosdns binary and configuration": "上游或 ECS 运行时配置不可用，请部署匹配的 mosdns 二进制文件和配置",
		"upstream configuration changed; refresh and retry":                                                     "上游配置已变更，请刷新后重试",
		"ECS configuration changed; refresh and retry":                                                          "ECS 配置已变更，请刷新后重试",
		"read settings failed":                                   "读取设置失败",
		"invalid subscription upload":                            "订阅上传请求无效",
		"subscription file is required":                          "必须提供订阅文件",
		"subscription file exceeds 20 MiB":                       "订阅文件超过 20 MiB 限制",
		"invalid limit":                                          "limit 参数无效",
		"list audit logs failed":                                 "读取审计日志失败",
		"rule or version not found":                              "规则或规则版本不存在",
		"invalid identifier":                                     "标识符无效",
		"authentication required":                                "需要登录",
		"csrf token is invalid":                                  "CSRF 令牌无效",
		"request body is too large":                              "请求体超过大小限制",
		"invalid JSON request body":                              "JSON 请求体无效",
		"internal server error":                                  "服务器内部错误",
		"ids must not be empty":                                  "规则 ID 列表不能为空",
		"unsupported batch operation":                            "不支持的批量操作",
		"priority must be between 0 and 1000":                    "优先级必须在 0 到 1000 之间",
		"comment exceeds 500 characters":                         "备注不能超过 500 个字符",
		"invalid category/action":                                "规则类别或动作无效",
		"regexp exceeds 512 bytes":                               "正则表达式不能超过 512 字节",
		"duplicate rule pattern in category":                     "同一类别中存在重复的规则模式",
		"regexp rule limit exceeded":                             "正则表达式规则数量超出限制",
		"regexp is empty":                                        "正则表达式不能为空",
		"invalid match type":                                     "匹配类型无效",
		"invalid domain pattern":                                 "域名模式无效",
		"invalid domain length":                                  "域名长度无效",
		"invalid domain label":                                   "域名标签无效",
		"at least one device field is required":                  "至少需要提供一个设备字段",
		"display_name exceeds 128 characters":                    "设备显示名称不能超过 128 个字符",
		"note exceeds 500 characters":                            "设备备注不能超过 500 个字符",
		"query retention days must be within 1..365":             "查询保留天数必须在 1 到 365 之间",
		"cache TTL must be within 0..604800":                     "缓存 TTL 必须在 0 到 604800 之间",
		"invalid upstream group":                                 "上游组无效",
		"subscription source_url must be an HTTP(S) URL":         "订阅地址必须是 HTTP 或 HTTPS URL",
		"subscription file must contain 1..20971520 bytes":       "订阅文件大小必须在 1 到 20971520 字节之间",
		"subscription upload must be a .txt file":                "订阅上传文件必须是 .txt 文件",
		"invalid event batch":                                    "查询事件批次无效",
		"invalid event":                                          "查询事件无效",
		"invalid event fields":                                   "查询事件字段无效",
		"invalid event values":                                   "查询事件字段值无效",
		"invalid event route":                                    "查询事件路由无效",
		"event field exceeds limit":                              "查询事件字段超过长度限制",
		"too many answer IPs":                                    "应答 IP 数量超过限制",
		"invalid answer IP":                                      "应答 IP 无效",
		"too many answer records":                                "应答记录数量超过限制",
		"invalid answer record":                                  "应答记录无效",
		"answer records exceed size limit":                       "应答记录超过大小限制",
		"invalid cursor":                                         "分页游标无效",
		"invalid client_ip":                                      "client_ip 参数无效",
		"invalid route":                                          "route 参数无效",
		"invalid qname_match":                                    "qname_match 参数无效",
		"invalid qtype":                                          "qtype 参数无效",
		"invalid rcode":                                          "rcode 参数无效",
		"invalid time range":                                     "时间范围无效",
		"qname contains requires a time range of at most 7 days": "qname contains 筛选的时间范围不能超过 7 天",
		"invalid statistic":                                      "统计类型无效",
		"invalid credentials":                                    "凭据无效",
		"password must be at least 8 characters":                 "密码至少需要 8 个字符",
		"username must be 3-64 characters and password at least 8 characters": "用户名长度必须为 3 到 64 个字符，密码至少需要 8 个字符",
		"runtime checksum does not match active version":                      "运行时校验和与当前生效版本不匹配",
		"mosdns rejected snapshot":                                            "mosdns 拒绝了规则快照",
		"mosdns returned a different checksum":                                "mosdns 返回的校验和不一致",
		"mosdns retained previous snapshot":                                   "mosdns 保留了先前的规则快照",
		"invalid apply response":                                              "mosdns 返回的发布结果无效",
	}[message]; ok {
		return localized
	}
	if code == "VALIDATION_ERROR" {
		switch {
		case strings.Contains(message, "address must be a valid"):
			return "上游地址格式无效，请输入 IP、主机名或带协议的完整地址"
		case strings.Contains(message, "uses an unsupported scheme"):
			return "上游地址协议不受支持，请使用 udp、tcp、tls、https 或 quic"
		}
	}

	switch code {
	case "VALIDATION_ERROR":
		return "请求参数校验失败"
	case "MOSDNS_UNAVAILABLE":
		return "mosdns 服务不可用"
	case "SETTINGS_APPLY_FAILED":
		return "设置应用失败"
	case "SUBSCRIPTION_CREATE_FAILED", "SUBSCRIPTION_UPLOAD_FAILED", "SUBSCRIPTION_UPDATE_FAILED", "SUBSCRIPTION_REFRESH_FAILED", "SUBSCRIPTION_DELETE_FAILED":
		return "订阅源操作失败"
	default:
		return "请求处理失败"
	}
}
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
