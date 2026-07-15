package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/managed-dns/controller/internal/auth"
	"github.com/managed-dns/controller/internal/config"
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
}
type contextKey string

const requestIDKey contextKey = "request_id"
const adminKey contextKey = "admin"

func New(logger *slog.Logger, cfg config.Config, store *storage.Store) *App {
	service := auth.New(store, cfg.Web.SessionTTL)
	limiter := auth.NewLoginLimiter()
	return &App{logger: logger, config: cfg, store: store, auth: service, limiter: limiter}
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
