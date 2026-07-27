package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/managed-dns/controller/internal/storage"
)

func testService(t *testing.T) *Service {
	t.Helper()
	store, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return New(store, time.Hour)
}

func TestResetPasswordRevokesSessions(t *testing.T) {
	service := testService(t)
	ctx := context.Background()
	if err := service.CreateAdmin(ctx, "admin", "old-password"); err != nil {
		t.Fatal(err)
	}
	token, _, err := service.Login(ctx, "admin", "old-password", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ResetPassword(ctx, "admin", "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Session(ctx, token); err == nil {
		t.Fatal("pre-reset session remained valid")
	}
	if _, _, err := service.Login(ctx, "admin", "old-password", "", ""); err == nil {
		t.Fatal("previous password remained valid")
	}
	if _, _, err := service.Login(ctx, "admin", "new-password", "", ""); err != nil {
		t.Fatalf("new password login failed: %v", err)
	}
}

func TestResetPasswordRejectsUnknownAdministrator(t *testing.T) {
	if err := testService(t).ResetPassword(context.Background(), "admin", "new-password"); err != ErrAdminNotFound {
		t.Fatalf("error = %v, want %v", err, ErrAdminNotFound)
	}
}

func TestPasswordWorkHonorsContextWhileBusy(t *testing.T) {
	service := testService(t)
	service.passwordWork <- struct{}{}
	defer func() { <-service.passwordWork }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.hashPassword(ctx, "new-password"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
}

func TestLoginLimiterBoundsFailureEntries(t *testing.T) {
	limiter := NewLoginLimiter()
	for i := 0; i < maxLoginLimiterEntries+100; i++ {
		limiter.Failed(fmt.Sprintf("192.0.2.%d", i))
	}
	if got := len(limiter.failures); got != maxLoginLimiterEntries {
		t.Fatalf("entries = %d, want %d", got, maxLoginLimiterEntries)
	}
}

func TestLoginLimiterRemovesExpiredBlock(t *testing.T) {
	limiter := NewLoginLimiter()
	limiter.failures["192.0.2.1"] = attempt{until: time.Now().Add(-time.Second), lastSeen: time.Now().Add(-time.Minute)}
	if !limiter.Allowed("192.0.2.1") {
		t.Fatal("expired block remained active")
	}
	if _, exists := limiter.failures["192.0.2.1"]; exists {
		t.Fatal("expired block was not removed")
	}
}
