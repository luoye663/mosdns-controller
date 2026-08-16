package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

type retryingControlPlane struct {
	reconciles int
	syncs      int
}

func (f *retryingControlPlane) Reconcile(context.Context) (string, error) {
	f.reconciles++
	return "in_sync", nil
}

func (f *retryingControlPlane) SyncSettings(context.Context) error {
	f.syncs++
	if f.syncs == 1 {
		return errors.New("temporarily unavailable")
	}
	return nil
}

func TestPeriodicReconcileRetriesSettingsSync(t *testing.T) {
	controlPlane := &retryingControlPlane{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reconcileControlPlane(context.Background(), controlPlane, logger)
	reconcileControlPlane(context.Background(), controlPlane, logger)
	if controlPlane.reconciles != 2 || controlPlane.syncs != 2 {
		t.Fatalf("reconciles=%d syncs=%d", controlPlane.reconciles, controlPlane.syncs)
	}
}
