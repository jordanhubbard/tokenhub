package vault

import (
	"crypto/rand"
	"testing"
)

func TestVault_SetAndGet(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	vault, err := NewVault(key)
	if err != nil {
		t.Fatalf("Failed to create vault: %v", err)
	}

	testKey := "test_key"
	testValue := "secret_value"

	if err := vault.Set(testKey, testValue); err != nil {
		t.Fatalf("Failed to set value: %v", err)
	}

	retrieved, err := vault.Get(testKey)
	if err != nil {
		t.Fatalf("Failed to get value: %v", err)
	}

	if retrieved != testValue {
		t.Errorf("Expected %s, got %s", testValue, retrieved)
	}
}

func TestVault_InvalidKey(t *testing.T) {
	key := make([]byte, 16) // Wrong size
	_, err := NewVault(key)
	if err == nil {
		t.Error("Expected error for invalid key size")
	}
}

func TestVault_GetNonExistent(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	vault, err := NewVault(key)
	if err != nil {
		t.Fatalf("Failed to create vault: %v", err)
	}

	_, err = vault.Get("nonexistent")
	if err == nil {
		t.Error("Expected error for nonexistent key")
	}
}

func TestVault_Delete(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	vault, err := NewVault(key)
	if err != nil {
		t.Fatalf("Failed to create vault: %v", err)
	}

	testKey := "test_key"
	testValue := "secret_value"

	if err := vault.Set(testKey, testValue); err != nil {
		t.Fatalf("Failed to set value: %v", err)
	}
	
	vault.Delete(testKey)

	_, err = vault.Get(testKey)
	if err == nil {
		t.Error("Expected error after deletion")
	}
}

func TestVault_ExportImport(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	vault1, err := NewVault(key)
	if err != nil {
		t.Fatalf("Failed to create vault: %v", err)
	}

	if err := vault1.Set("key1", "value1"); err != nil {
		t.Fatalf("Failed to set key1: %v", err)
	}
	if err := vault1.Set("key2", "value2"); err != nil {
		t.Fatalf("Failed to set key2: %v", err)
	}

	exported := vault1.Export()

	vault2, err := NewVault(key)
	if err != nil {
		t.Fatalf("Failed to create second vault: %v", err)
	}

	if err := vault2.Import(exported); err != nil {
		t.Fatalf("Failed to import: %v", err)
	}

	val1, err := vault2.Get("key1")
	if err != nil || val1 != "value1" {
		t.Errorf("Failed to retrieve key1 after import")
	}

	val2, err := vault2.Get("key2")
	if err != nil || val2 != "value2" {
		t.Errorf("Failed to retrieve key2 after import")
	}
}
