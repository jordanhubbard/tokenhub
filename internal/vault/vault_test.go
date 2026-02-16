package vault

import (
	"testing"
)

func unlocked(t *testing.T) *Vault {
	t.Helper()
	v, err := New(true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := v.Unlock([]byte("a]strong-password-for-testing!!")); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	return v
}

func TestVault_SetAndGet(t *testing.T) {
	v := unlocked(t)

	if err := v.Set("test_key", "secret_value"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := v.Get("test_key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "secret_value" {
		t.Errorf("Get = %q, want %q", got, "secret_value")
	}
}

func TestVault_GetNonExistent(t *testing.T) {
	v := unlocked(t)

	_, err := v.Get("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent key")
	}
}

func TestVault_Delete(t *testing.T) {
	v := unlocked(t)

	if err := v.Set("test_key", "secret_value"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	v.Delete("test_key")

	_, err := v.Get("test_key")
	if err == nil {
		t.Error("expected error after deletion")
	}
}

func TestVault_ExportImport(t *testing.T) {
	v1 := unlocked(t)

	if err := v1.Set("key1", "value1"); err != nil {
		t.Fatalf("Set key1: %v", err)
	}
	if err := v1.Set("key2", "value2"); err != nil {
		t.Fatalf("Set key2: %v", err)
	}

	exported := v1.Export()

	// Create a second vault with the same password.
	v2 := unlocked(t)
	if err := v2.Import(exported); err != nil {
		t.Fatalf("Import: %v", err)
	}

	val1, err := v2.Get("key1")
	if err != nil || val1 != "value1" {
		t.Errorf("key1: got %q err=%v, want %q", val1, err, "value1")
	}

	val2, err := v2.Get("key2")
	if err != nil || val2 != "value2" {
		t.Errorf("key2: got %q err=%v, want %q", val2, err, "value2")
	}
}

func TestVault_LockedOperationsFail(t *testing.T) {
	v, err := New(true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Vault starts locked; operations should fail.
	_, err = v.Encrypt([]byte("test"))
	if err == nil {
		t.Error("expected Encrypt to fail when locked")
	}

	_, err = v.Decrypt([]byte("test"))
	if err == nil {
		t.Error("expected Decrypt to fail when locked")
	}

	err = v.Set("k", "v")
	if err == nil {
		t.Error("expected Set to fail when locked")
	}
}

func TestVault_UnlockPasswordTooShort(t *testing.T) {
	v, err := New(true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = v.Unlock([]byte("short"))
	if err == nil {
		t.Error("expected error for short password")
	}
}

func TestVault_LockClearsKey(t *testing.T) {
	v := unlocked(t)

	if err := v.Set("k", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	v.Lock()

	if !v.IsLocked() {
		t.Error("expected vault to be locked after Lock()")
	}

	_, err := v.Get("k")
	if err == nil {
		t.Error("expected Get to fail after Lock()")
	}
}
