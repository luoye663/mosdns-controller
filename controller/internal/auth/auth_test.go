package auth

import (
	"context"
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
