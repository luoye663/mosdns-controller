// Package auth keeps credentials and session state on the controller, never in clients.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/managed-dns/controller/internal/storage"
	"golang.org/x/crypto/argon2"
)

const CookieName = "mosdns_session"

var ErrInitialAdminExists = errors.New("an administrator already exists")

type Service struct {
	store *storage.Store
	ttl   time.Duration
}
type Admin struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}
type Session struct {
	Admin     Admin
	CSRFToken string
}

func New(store *storage.Store, ttl time.Duration) *Service { return &Service{store: store, ttl: ttl} }

func (s *Service) CreateAdmin(ctx context.Context, username, password string) error {
	username, err := validateCredentials(username, password)
	if err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	_, err = s.store.DB().ExecContext(ctx, `INSERT INTO admins(username,password_hash,created_at_ms,updated_at_ms) VALUES(?,?,?,?)`, username, hash, now, now)
	if err != nil {
		return fmt.Errorf("create admin: %w", err)
	}
	return nil
}

// CreateInitialAdmin succeeds only while the controller has no administrators.
func (s *Service) CreateInitialAdmin(ctx context.Context, username, password string) error {
	required, err := s.NeedsInitialAdmin(ctx)
	if err != nil {
		return err
	}
	if !required {
		return ErrInitialAdminExists
	}
	username, err = validateCredentials(username, password)
	if err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	result, err := s.store.DB().ExecContext(ctx, `INSERT INTO admins(username,password_hash,created_at_ms,updated_at_ms) SELECT ?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM admins)`, username, hash, now, now)
	if err != nil {
		return fmt.Errorf("create initial admin: %w", err)
	}
	created, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check initial admin creation: %w", err)
	}
	if created == 0 {
		return ErrInitialAdminExists
	}
	return nil
}

func (s *Service) NeedsInitialAdmin(ctx context.Context) (bool, error) {
	var exists bool
	if err := s.store.DB().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admins)`).Scan(&exists); err != nil {
		return false, err
	}
	return !exists, nil
}

func (s *Service) Login(ctx context.Context, username, password, clientIP, userAgent string) (string, string, error) {
	var id int64
	var hash string
	var disabled bool
	err := s.store.DB().QueryRowContext(ctx, `SELECT id,password_hash,disabled FROM admins WHERE username = ? COLLATE NOCASE`, strings.TrimSpace(username)).Scan(&id, &hash, &disabled)
	if err != nil || disabled || !verifyPassword(password, hash) {
		return "", "", errors.New("invalid credentials")
	}
	token, err := randomToken()
	if err != nil {
		return "", "", err
	}
	csrf, err := randomToken()
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	_, err = s.store.DB().ExecContext(ctx, `INSERT INTO sessions(token_hash,admin_id,csrf_hash,client_ip,user_agent,created_at_ms,last_seen_at_ms,expires_at_ms) VALUES(?,?,?,?,?,?,?,?)`, digest(token), id, digest(csrf), clientIP, userAgent, now.UnixMilli(), now.UnixMilli(), now.Add(s.ttl).UnixMilli())
	if err != nil {
		return "", "", fmt.Errorf("create session: %w", err)
	}
	return token, csrf, nil
}

func (s *Service) Session(ctx context.Context, token string) (Session, error) {
	var session Session
	var expires int64
	err := s.store.DB().QueryRowContext(ctx, `SELECT a.id,a.username,s.expires_at_ms FROM sessions s JOIN admins a ON a.id=s.admin_id WHERE s.token_hash=? AND a.disabled=0`, digest(token)).Scan(&session.Admin.ID, &session.Admin.Username, &expires)
	if errors.Is(err, sql.ErrNoRows) || err == nil && time.Now().UnixMilli() >= expires {
		if err == nil {
			_ = s.Logout(ctx, token)
		}
		return Session{}, errors.New("session expired")
	}
	if err != nil {
		return Session{}, err
	}
	_, _ = s.store.DB().ExecContext(ctx, `UPDATE sessions SET last_seen_at_ms=? WHERE token_hash=?`, time.Now().UnixMilli(), digest(token))
	return session, nil
}
func (s *Service) VerifyCSRF(ctx context.Context, token, csrf string) bool {
	var expected []byte
	if err := s.store.DB().QueryRowContext(ctx, `SELECT csrf_hash FROM sessions WHERE token_hash=? AND expires_at_ms>?`, digest(token), time.Now().UnixMilli()).Scan(&expected); err != nil {
		return false
	}
	got := digest(csrf)
	return subtle.ConstantTimeCompare(expected, got) == 1
}
func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.store.DB().ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, digest(token))
	return err
}
func (s *Service) ChangePassword(ctx context.Context, adminID int64, currentPassword, nextPassword string) error {
	if len(nextPassword) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	var currentHash string
	if err := s.store.DB().QueryRowContext(ctx, `SELECT password_hash FROM admins WHERE id=? AND disabled=0`, adminID).Scan(&currentHash); err != nil || !verifyPassword(currentPassword, currentHash) {
		return errors.New("invalid credentials")
	}
	nextHash, err := hashPassword(nextPassword)
	if err != nil {
		return err
	}
	_, err = s.store.DB().ExecContext(ctx, `UPDATE admins SET password_hash=?,updated_at_ms=? WHERE id=?`, nextHash, time.Now().UnixMilli(), adminID)
	return err
}
func validateCredentials(username, password string) (string, error) {
	username = strings.TrimSpace(username)
	if len(username) < 3 || len(username) > 64 || len(password) < 8 {
		return "", errors.New("username must be 3-64 characters and password at least 8 characters")
	}
	return username, nil
}
func digest(value string) []byte { result := sha256.Sum256([]byte(value)); return result[:] }
func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// 密码格式携带 Argon2id 参数，后续升级参数时仍可验证已有管理员。
func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return "argon2id$v=19$m=65536,t=3,p=2$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(hash), nil
}
func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" || parts[2] != "m=65536,t=3,p=2" {
		return false
	}
	salt, saltErr := base64.RawStdEncoding.DecodeString(parts[3])
	expected, hashErr := base64.RawStdEncoding.DecodeString(parts[4])
	if saltErr != nil || hashErr != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, uint32(len(expected)))
	return subtle.ConstantTimeCompare(expected, got) == 1
}
