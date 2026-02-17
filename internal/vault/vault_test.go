package vault

import (
	"testing"
	"time"
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
	password := []byte("a]strong-password-for-testing!!")
	v1, err := New(true)
	if err != nil {
		t.Fatalf("New v1: %v", err)
	}
	if err := v1.Unlock(password); err != nil {
		t.Fatalf("Unlock v1: %v", err)
	}

	if err := v1.Set("key1", "value1"); err != nil {
		t.Fatalf("Set key1: %v", err)
	}
	if err := v1.Set("key2", "value2"); err != nil {
		t.Fatalf("Set key2: %v", err)
	}

	exported := v1.Export()
	salt := v1.Salt()

	// Create a second vault with the same password AND salt.
	v2, err := New(true)
	if err != nil {
		t.Fatalf("New v2: %v", err)
	}
	v2.SetSalt(salt) // must set before Unlock so same key is derived
	if err := v2.Unlock(password); err != nil {
		t.Fatalf("Unlock v2: %v", err)
	}
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

func TestVault_Argon2idDerivesDifferentKeys(t *testing.T) {
	// Two vaults with same password but different salts should produce different keys.
	v1 := unlocked(t)
	v2 := unlocked(t)

	salt1 := v1.Salt()
	salt2 := v2.Salt()

	if salt1 == nil || salt2 == nil {
		t.Fatal("expected non-nil salts")
	}

	// Salts should be different (random).
	same := true
	for i := range salt1 {
		if salt1[i] != salt2[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("expected different random salts for different vaults")
	}
}

func TestVault_SaltPersistence(t *testing.T) {
	v, err := New(true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// No salt before first unlock.
	if v.Salt() != nil {
		t.Error("expected nil salt before unlock")
	}

	if err := v.Unlock([]byte("a-strong-password!!")); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	salt := v.Salt()
	if salt == nil {
		t.Fatal("expected non-nil salt after unlock")
	}
	if len(salt) != 16 {
		t.Errorf("expected 16-byte salt, got %d", len(salt))
	}

	// Set salt and re-unlock should reuse it.
	v2, _ := New(true)
	v2.SetSalt(salt)
	if err := v2.Unlock([]byte("a-strong-password!!")); err != nil {
		t.Fatalf("Unlock v2: %v", err)
	}

	salt2 := v2.Salt()
	for i := range salt {
		if salt[i] != salt2[i] {
			t.Error("expected same salt after SetSalt")
			break
		}
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

func TestAutoLock(t *testing.T) {
	v, err := New(true, WithAutoLockDuration(100*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := v.Unlock([]byte("a]strong-password-for-testing!!")); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	if v.IsLocked() {
		t.Fatal("expected vault to be unlocked immediately after Unlock")
	}

	// Wait long enough for the auto-lock to trigger.
	time.Sleep(200 * time.Millisecond)

	if !v.IsLocked() {
		t.Error("expected vault to be auto-locked after inactivity timeout")
	}
}

func TestRotatePassword(t *testing.T) {
	v := unlocked(t)

	// Store some values.
	if err := v.Set("key1", "value1"); err != nil {
		t.Fatalf("Set key1: %v", err)
	}
	if err := v.Set("key2", "value2"); err != nil {
		t.Fatalf("Set key2: %v", err)
	}

	oldPassword := []byte("a]strong-password-for-testing!!")
	newPassword := []byte("new-strong-password!!")

	// Rotate password.
	if err := v.RotatePassword(oldPassword, newPassword); err != nil {
		t.Fatalf("RotatePassword: %v", err)
	}

	// Values should still be accessible after rotation.
	val1, err := v.Get("key1")
	if err != nil {
		t.Fatalf("Get key1 after rotate: %v", err)
	}
	if val1 != "value1" {
		t.Errorf("key1 = %q, want %q", val1, "value1")
	}

	val2, err := v.Get("key2")
	if err != nil {
		t.Fatalf("Get key2 after rotate: %v", err)
	}
	if val2 != "value2" {
		t.Errorf("key2 = %q, want %q", val2, "value2")
	}

	// Lock and re-unlock with new password to verify the new key works.
	newSalt := v.Salt()
	v.Lock()

	v2, err := New(true)
	if err != nil {
		t.Fatalf("New v2: %v", err)
	}
	v2.SetSalt(newSalt)
	if err := v2.Unlock(newPassword); err != nil {
		t.Fatalf("Unlock v2 with new password: %v", err)
	}

	// Import the exported data and verify.
	exported := v.Export()
	if err := v2.Import(exported); err != nil {
		t.Fatalf("Import: %v", err)
	}
	val1, err = v2.Get("key1")
	if err != nil {
		t.Fatalf("Get key1 from v2: %v", err)
	}
	if val1 != "value1" {
		t.Errorf("key1 from v2 = %q, want %q", val1, "value1")
	}
}

func TestRotatePasswordWrongOldPassword(t *testing.T) {
	v := unlocked(t)

	// Store a value so rotation has work to do.
	if err := v.Set("key1", "value1"); err != nil {
		t.Fatalf("Set key1: %v", err)
	}

	wrongOldPassword := []byte("wrong-password-definitely!!")
	newPassword := []byte("new-strong-password!!")

	err := v.RotatePassword(wrongOldPassword, newPassword)
	if err == nil {
		t.Fatal("expected error when old password is wrong")
	}
	if err.Error() != "old password does not match" {
		t.Errorf("unexpected error message: %v", err)
	}

	// Verify the vault is still usable with the original key (rotation was rejected).
	val, err := v.Get("key1")
	if err != nil {
		t.Fatalf("Get key1 after failed rotation: %v", err)
	}
	if val != "value1" {
		t.Errorf("key1 = %q, want %q", val, "value1")
	}
}

func TestRotatePasswordTooShort(t *testing.T) {
	v := unlocked(t)

	oldPassword := []byte("a]strong-password-for-testing!!")
	shortPassword := []byte("short")

	err := v.RotatePassword(oldPassword, shortPassword)
	if err == nil {
		t.Error("expected error for short new password")
	}
}

func TestRotatePasswordWhileLocked(t *testing.T) {
	v, err := New(true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Vault starts locked; rotation should fail.
	err = v.RotatePassword([]byte("old-password!!"), []byte("new-password!!"))
	if err == nil {
		t.Error("expected error when vault is locked")
	}
}

func TestAutoLockTouch(t *testing.T) {
	v, err := New(true, WithAutoLockDuration(150*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := v.Unlock([]byte("a]strong-password-for-testing!!")); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	// Touch the vault periodically to keep it alive past the auto-lock timeout.
	for i := 0; i < 4; i++ {
		time.Sleep(50 * time.Millisecond)
		v.Touch()
	}

	// At this point ~200ms have elapsed, but the vault should still be unlocked
	// because Touch() kept resetting the timer.
	if v.IsLocked() {
		t.Error("expected vault to remain unlocked because Touch() was called")
	}

	// Now stop touching and let the auto-lock kick in.
	time.Sleep(200 * time.Millisecond)

	if !v.IsLocked() {
		t.Error("expected vault to be auto-locked after Touch() stopped")
	}
}
