package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// AdminTokenHolder provides thread-safe access to the admin token with
// persistence to the data directory. The token survives container restarts
// and can be rotated at runtime via the admin API.
type AdminTokenHolder struct {
	mu    sync.RWMutex
	token string
	dbDSN string // used to derive the data directory for persistence
}

// NewAdminTokenHolder creates a holder and resolves the initial token using
// the following precedence:
//
//  1. Explicit env/config value (operator-provided, source of truth)
//  2. Previously persisted token from the data directory
//  3. Newly generated random token
//
// The resolved token is always persisted so that future restarts without the
// env var pick up the same token.
func NewAdminTokenHolder(configToken, dbDSN string, logger *slog.Logger) (*AdminTokenHolder, error) {
	h := &AdminTokenHolder{dbDSN: dbDSN}

	switch {
	case configToken != "":
		h.token = configToken
	default:
		if persisted := h.readPersisted(); persisted != "" {
			h.token = persisted
		}
	}

	if h.token == "" {
		tokenBytes := make([]byte, 32)
		if _, err := rand.Read(tokenBytes); err != nil {
			return nil, fmt.Errorf("generate admin token: %w", err)
		}
		h.token = hex.EncodeToString(tokenBytes)
		logger.Warn("TOKENHUB_ADMIN_TOKEN not set — auto-generated token (retrieve with: tokenhubctl admin-token)")
	}

	h.persist(logger)
	return h, nil
}

// Get returns the current admin token.
func (h *AdminTokenHolder) Get() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.token
}

// ConstantTimeEqual returns true if the provided token matches the current
// admin token using constant-time comparison.
func (h *AdminTokenHolder) ConstantTimeEqual(provided string) bool {
	h.mu.RLock()
	current := h.token
	h.mu.RUnlock()
	return subtle.ConstantTimeCompare([]byte(provided), []byte(current)) == 1
}

// Rotate generates a new random token, persists it, and returns the new token.
func (h *AdminTokenHolder) Rotate(logger *slog.Logger) (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	newToken := hex.EncodeToString(tokenBytes)

	h.mu.Lock()
	h.token = newToken
	h.mu.Unlock()

	h.persist(logger)
	return newToken, nil
}

// Replace sets an explicit token (e.g. from an API call), persists it, and
// returns the old token for audit purposes.
func (h *AdminTokenHolder) Replace(newToken string, logger *slog.Logger) string {
	h.mu.Lock()
	old := h.token
	h.token = newToken
	h.mu.Unlock()

	h.persist(logger)
	return old
}

// dataDir returns the directory derived from the DB DSN, or "" if not applicable.
func (h *AdminTokenHolder) dataDir() string {
	dsn := strings.TrimPrefix(h.dbDSN, "file:")
	if i := strings.IndexByte(dsn, '?'); i >= 0 {
		dsn = dsn[:i]
	}
	if dsn == "" || dsn == ":memory:" {
		return ""
	}
	return filepath.Dir(dsn)
}

func (h *AdminTokenHolder) readPersisted() string {
	dir := h.dataDir()
	if dir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, ".admin-token"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (h *AdminTokenHolder) persist(logger *slog.Logger) {
	dir := h.dataDir()
	if dir == "" {
		return
	}
	h.mu.RLock()
	token := h.token
	h.mu.RUnlock()

	envContent := []byte("TOKENHUB_ADMIN_TOKEN=" + token + "\n")
	if err := os.WriteFile(filepath.Join(dir, "env"), envContent, 0600); err != nil {
		logger.Warn("failed to write state env file", slog.String("error", err.Error()))
	}
	tokenContent := []byte(token + "\n")
	if err := os.WriteFile(filepath.Join(dir, ".admin-token"), tokenContent, 0600); err != nil {
		logger.Warn("failed to write admin token file", slog.String("error", err.Error()))
	}
}
