package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// unlockVaultForSecrets unlocks the test vault so secret endpoints work.
func unlockVaultForSecrets(t *testing.T, baseURL string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"admin_password": "supersecretpassword"})
	resp, err := http.Post(baseURL+"/admin/v1/vault/unlock", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("vault unlock request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("vault unlock returned %d", resp.StatusCode)
	}
}

func TestVaultSecretsCRUD(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()
	unlockVaultForSecrets(t, ts.URL)

	base := ts.URL + "/admin/v1/vault/secrets"

	// PUT — store two secrets
	for _, tc := range []struct{ key, value string }{
		{"do-token", "dop_v1_abc123"},
		{"github-token", "ghp_xyz789"},
	} {
		body, _ := json.Marshal(map[string]string{"value": tc.value})
		req, _ := http.NewRequest(http.MethodPut, base+"/"+tc.key, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT secret %q: %v", tc.key, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT secret %q: got %d", tc.key, resp.StatusCode)
		}
		var result map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&result)
		if result["ok"] != true {
			t.Fatalf("PUT secret %q: ok!=true", tc.key)
		}
	}

	// GET /admin/v1/vault/secrets — list should return both keys
	resp, err := http.Get(base)
	if err != nil {
		t.Fatalf("GET secrets list: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET secrets list: got %d", resp.StatusCode)
	}
	var listResult map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&listResult)
	secrets, ok := listResult["secrets"].([]any)
	if !ok {
		t.Fatalf("GET secrets list: missing 'secrets' field")
	}
	keySet := map[string]bool{}
	for _, s := range secrets {
		keySet[fmt.Sprint(s)] = true
	}
	for _, want := range []string{"do-token", "github-token"} {
		if !keySet[want] {
			t.Errorf("GET secrets list: missing key %q; got %v", want, secrets)
		}
	}

	// GET /admin/v1/vault/secrets/{key} — retrieve value
	resp2, err := http.Get(base + "/do-token")
	if err != nil {
		t.Fatalf("GET secret: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET secret: got %d", resp2.StatusCode)
	}
	var getResult map[string]any
	_ = json.NewDecoder(resp2.Body).Decode(&getResult)
	if getResult["value"] != "dop_v1_abc123" {
		t.Errorf("GET secret: got value %q, want %q", getResult["value"], "dop_v1_abc123")
	}
	if getResult["key"] != "do-token" {
		t.Errorf("GET secret: got key %q, want %q", getResult["key"], "do-token")
	}

	// GET non-existent key → 404
	resp3, err := http.Get(base + "/nonexistent")
	if err != nil {
		t.Fatalf("GET missing secret: %v", err)
	}
	defer func() { _ = resp3.Body.Close() }()
	if resp3.StatusCode != http.StatusNotFound {
		t.Errorf("GET missing secret: got %d, want 404", resp3.StatusCode)
	}

	// DELETE /admin/v1/vault/secrets/{key}
	req, _ := http.NewRequest(http.MethodDelete, base+"/do-token", nil)
	resp4, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE secret: %v", err)
	}
	defer func() { _ = resp4.Body.Close() }()
	if resp4.StatusCode != http.StatusOK {
		t.Fatalf("DELETE secret: got %d", resp4.StatusCode)
	}
	var delResult map[string]any
	_ = json.NewDecoder(resp4.Body).Decode(&delResult)
	if delResult["ok"] != true {
		t.Fatalf("DELETE secret: ok!=true")
	}

	// GET deleted key → 404
	resp5, err := http.Get(base + "/do-token")
	if err != nil {
		t.Fatalf("GET deleted secret: %v", err)
	}
	defer func() { _ = resp5.Body.Close() }()
	if resp5.StatusCode != http.StatusNotFound {
		t.Errorf("GET deleted secret: got %d, want 404", resp5.StatusCode)
	}

	// DELETE non-existent key → 404
	req2, _ := http.NewRequest(http.MethodDelete, base+"/nonexistent", nil)
	resp6, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("DELETE missing secret: %v", err)
	}
	defer func() { _ = resp6.Body.Close() }()
	if resp6.StatusCode != http.StatusNotFound {
		t.Errorf("DELETE missing secret: got %d, want 404", resp6.StatusCode)
	}
}

func TestVaultSecretsLockedVault(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()
	// Do NOT unlock — vault is locked.

	base := ts.URL + "/admin/v1/vault/secrets"

	// All endpoints should return 503 when vault is locked.
	t.Run("list_locked", func(t *testing.T) {
		resp, err := http.Get(base)
		if err != nil {
			t.Fatalf("GET secrets list: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("got %d, want 503", resp.StatusCode)
		}
	})

	t.Run("get_locked", func(t *testing.T) {
		resp, err := http.Get(base + "/some-key")
		if err != nil {
			t.Fatalf("GET secret: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("got %d, want 503", resp.StatusCode)
		}
	})

	t.Run("put_locked", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{"value": "test"})
		req, _ := http.NewRequest(http.MethodPut, base+"/some-key", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT secret: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("got %d, want 503", resp.StatusCode)
		}
	})

	t.Run("delete_locked", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodDelete, base+"/some-key", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE secret: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("got %d, want 503", resp.StatusCode)
		}
	})
}

func TestVaultSecretsListEmpty(t *testing.T) {
	ts, _, _ := setupTestServer(t)
	defer ts.Close()
	unlockVaultForSecrets(t, ts.URL)

	resp, err := http.Get(ts.URL + "/admin/v1/vault/secrets")
	if err != nil {
		t.Fatalf("GET secrets list: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET secrets list: got %d", resp.StatusCode)
	}
	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	secrets := result["secrets"]
	// Should be empty array (or nil), not nil map
	switch v := secrets.(type) {
	case nil:
		// ok — json null
	case []any:
		if len(v) != 0 {
			t.Errorf("expected empty secrets list, got %v", v)
		}
	default:
		t.Errorf("unexpected secrets type %T: %v", secrets, secrets)
	}
}
