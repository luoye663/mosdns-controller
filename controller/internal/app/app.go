package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/managed-dns/controller/internal/mosdnsclient"
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
}
type contextKey string

const requestIDKey contextKey = "request_id"
const adminKey contextKey = "admin"

func New(logger *slog.Logger, cfg config.Config, store *storage.Store, client mosdnsclient.Client) *App {
	service := auth.New(store, cfg.Web.SessionTTL)
	limiter := auth.NewLoginLimiter()
	return &App{logger: logger, config: cfg, store: store, auth: service, limiter: limiter, rules: rules.New(store, client)}
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
		api.Group(func(protected chi.Router) {
			protected.Use(a.requireSession)
			protected.Get("/auth/me", a.me)
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
			protected.Get("/rule-versions", a.listVersions)
			protected.Get("/rule-versions/{version}", a.versionDetail)
			protected.Post("/rule-versions/{version}/rollback", a.requireCSRF(a.rollback))
			protected.Post("/rule-versions/reconcile", a.requireCSRF(a.reconcile))
		})
	})
	r.Handle("/*", web.Handler())
	return r
}
func (a *App) InternalHandler() http.Handler {
	r := chi.NewRouter()
	r.Use(a.middleware)
	r.Get("/health/ready", a.ready)
	return r
}
func (a *App) Reconcile(ctx context.Context) (string, error) { return a.rules.Reconcile(ctx) }

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
func (a *App) me(w http.ResponseWriter, r *http.Request) {
	writeData(w, r, http.StatusOK, r.Context().Value(adminKey).(auth.Admin))
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
	if err := a.auth.ChangePassword(r.Context(), admin.ID, input.CurrentPassword, input.NewPassword); err != nil {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return
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
		Operation string  `json:"operation"`
		IDs       []int64 `json:"ids"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	a.publishRule(w, r, func(admin auth.Admin) (rules.Version, error) {
		return a.rules.Batch(r.Context(), input.Operation, input.IDs, admin.ID, requestID(r), remoteIP(r))
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
	a.publishRule(w, r, func(admin auth.Admin) (rules.Version, error) {
		return a.rules.Rollback(r.Context(), uint64(version), admin.ID, requestID(r), remoteIP(r))
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
		ctx, cancel := context.WithTimeout(r.Context(), a.config.HTTP.Timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
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
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}, "request_id": r.Context().Value(requestIDKey)})
}
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
