package httpapi

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordanhubbard/tokenhub/internal/apikey"
	"github.com/jordanhubbard/tokenhub/internal/store"
)

func newAdminTokenTestStore(t *testing.T) (string, *store.SQLiteStore, *apikey.Manager, *slog.Logger) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tokenhub.db")
	db, err := store.NewSQLite(dbPath)
	if err != nil {
		t.Fatalf("new sqlite: %v", err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return dbPath, db, apikey.NewManager(db), logger
}

func TestProvisionHostAPIKeyReusesValidPersistedKey(t *testing.T) {
	dbPath, _, mgr, logger := newAdminTokenTestStore(t)
	dir := filepath.Dir(dbPath)
	valid, _, err := mgr.Generate(context.Background(), "existing-host-local", "", 0, nil)
	if err != nil {
		t.Fatalf("generate existing key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".host-api-key"), []byte(valid+"\n"), 0o600); err != nil {
		t.Fatalf("write host key: %v", err)
	}

	holder, err := NewAdminTokenHolder("admin-token", dbPath, logger)
	if err != nil {
		t.Fatalf("new holder: %v", err)
	}
	got, err := holder.ProvisionHostAPIKey(context.Background(), mgr, logger)
	if err != nil {
		t.Fatalf("provision host key: %v", err)
	}
	if got != valid {
		t.Fatalf("expected persisted valid key to be reused")
	}
}

func TestProvisionHostAPIKeyReplacesInvalidPersistedKey(t *testing.T) {
	dbPath, _, mgr, logger := newAdminTokenTestStore(t)
	dir := filepath.Dir(dbPath)
	stale := "tokenhub_" + strings.Repeat("a", 64)
	if err := os.WriteFile(filepath.Join(dir, ".host-api-key"), []byte(stale+"\n"), 0o600); err != nil {
		t.Fatalf("write stale host key: %v", err)
	}

	holder, err := NewAdminTokenHolder("admin-token", dbPath, logger)
	if err != nil {
		t.Fatalf("new holder: %v", err)
	}
	got, err := holder.ProvisionHostAPIKey(context.Background(), mgr, logger)
	if err != nil {
		t.Fatalf("provision host key: %v", err)
	}
	if got == stale {
		t.Fatalf("expected invalid persisted key to be replaced")
	}
	if _, err := mgr.Validate(context.Background(), got); err != nil {
		t.Fatalf("replacement key should validate: %v", err)
	}
	envBytes, err := os.ReadFile(filepath.Join(dir, "env"))
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	if !strings.Contains(string(envBytes), "TOKENHUB_API_KEY="+got) {
		t.Fatalf("env file did not receive replacement host key")
	}
	backups, err := filepath.Glob(filepath.Join(dir, ".host-api-key.invalid-*"))
	if err != nil {
		t.Fatalf("glob backup: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected one invalid key backup, got %d", len(backups))
	}
}
