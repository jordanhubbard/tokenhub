package store

import (
	"context"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrate(t *testing.T) {
	s := newTestStore(t)
	// Running migrate twice should be idempotent.
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate failed: %v", err)
	}
}

func TestModelsCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Insert
	m := ModelRecord{
		ID: "gpt-4", ProviderID: "openai", Weight: 8,
		MaxContextTokens: 128000, InputPer1K: 0.01, OutputPer1K: 0.03, Enabled: true,
	}
	if err := s.UpsertModel(ctx, m); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}

	// Get
	got, err := s.GetModel(ctx, "gpt-4")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected model, got nil")
	}
	if got.Weight != 8 {
		t.Errorf("expected weight 8, got %d", got.Weight)
	}
	if got.MaxContextTokens != 128000 {
		t.Errorf("expected 128000 tokens, got %d", got.MaxContextTokens)
	}

	// Update
	m.Weight = 10
	if err := s.UpsertModel(ctx, m); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	got, _ = s.GetModel(ctx, "gpt-4")
	if got.Weight != 10 {
		t.Errorf("expected updated weight 10, got %d", got.Weight)
	}

	// List
	all, err := s.ListModels(ctx)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 model, got %d", len(all))
	}

	// Delete
	if err := s.DeleteModel(ctx, "gpt-4"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	got, _ = s.GetModel(ctx, "gpt-4")
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestGetModelNotFound(t *testing.T) {
	s := newTestStore(t)
	got, err := s.GetModel(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent model")
	}
}

func TestProvidersCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	p := ProviderRecord{
		ID: "openai", Type: "openai", Enabled: true,
		BaseURL: "https://api.openai.com", CredStore: "env",
	}
	if err := s.UpsertProvider(ctx, p); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}

	all, err := s.ListProviders(ctx)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 provider, got %d", len(all))
	}
	if all[0].BaseURL != "https://api.openai.com" {
		t.Errorf("unexpected base_url: %s", all[0].BaseURL)
	}

	// Update
	p.Enabled = false
	if err := s.UpsertProvider(ctx, p); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	all, _ = s.ListProviders(ctx)
	if all[0].Enabled {
		t.Error("expected disabled after update")
	}

	// Delete
	if err := s.DeleteProvider(ctx, "openai"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	all, _ = s.ListProviders(ctx)
	if len(all) != 0 {
		t.Errorf("expected 0 providers after delete, got %d", len(all))
	}
}

func TestRequestLogs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	entry := RequestLog{
		Timestamp:        time.Now().UTC(),
		ModelID:          "gpt-4",
		ProviderID:       "openai",
		Mode:             "normal",
		EstimatedCostUSD: 0.025,
		LatencyMs:        350,
		StatusCode:       200,
		RequestID:        "req-123",
	}
	if err := s.LogRequest(ctx, entry); err != nil {
		t.Fatalf("log request failed: %v", err)
	}

	// Log a second entry
	entry.ModelID = "claude-opus"
	entry.ProviderID = "anthropic"
	entry.LatencyMs = 500
	if err := s.LogRequest(ctx, entry); err != nil {
		t.Fatalf("log request 2 failed: %v", err)
	}

	logs, err := s.ListRequestLogs(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list logs failed: %v", err)
	}
	if len(logs) != 2 {
		t.Errorf("expected 2 logs, got %d", len(logs))
	}
	// Most recent first
	if logs[0].ModelID != "claude-opus" {
		t.Errorf("expected claude-opus first (most recent), got %s", logs[0].ModelID)
	}
}

func TestRequestLogsLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		entry := RequestLog{
			Timestamp:  time.Now().UTC(),
			ModelID:    "gpt-4",
			ProviderID: "openai",
			StatusCode: 200,
		}
		if err := s.LogRequest(ctx, entry); err != nil {
			t.Fatalf("log request failed: %v", err)
		}
	}

	logs, err := s.ListRequestLogs(ctx, 3, 0)
	if err != nil {
		t.Fatalf("list logs failed: %v", err)
	}
	if len(logs) != 3 {
		t.Errorf("expected 3 logs with limit, got %d", len(logs))
	}
}

func TestRequestLogsDefaultLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	logs, err := s.ListRequestLogs(ctx, 0, 0)
	if err != nil {
		t.Fatalf("list logs failed: %v", err)
	}
	if logs != nil {
		t.Errorf("expected nil logs for empty db, got %d", len(logs))
	}
}

func TestVaultBlobPersistence(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	salt := []byte("test-salt-16byte")
	data := map[string]string{
		"openai_key":    "enc-aes-gcm-openai",
		"anthropic_key": "enc-aes-gcm-anthropic",
	}

	if err := s.SaveVaultBlob(ctx, salt, data); err != nil {
		t.Fatalf("save vault blob failed: %v", err)
	}

	gotSalt, gotData, err := s.LoadVaultBlob(ctx)
	if err != nil {
		t.Fatalf("load vault blob failed: %v", err)
	}
	if string(gotSalt) != string(salt) {
		t.Errorf("expected salt %q, got %q", salt, gotSalt)
	}
	if len(gotData) != 2 {
		t.Errorf("expected 2 keys, got %d", len(gotData))
	}
	if gotData["openai_key"] != "enc-aes-gcm-openai" {
		t.Errorf("unexpected value: %s", gotData["openai_key"])
	}
}

func TestVaultBlobUpsert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Save initial blob.
	if err := s.SaveVaultBlob(ctx, []byte("salt1"), map[string]string{"k": "v1"}); err != nil {
		t.Fatalf("save 1 failed: %v", err)
	}

	// Upsert with new data.
	if err := s.SaveVaultBlob(ctx, []byte("salt2"), map[string]string{"k": "v2"}); err != nil {
		t.Fatalf("save 2 failed: %v", err)
	}

	gotSalt, gotData, err := s.LoadVaultBlob(ctx)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if string(gotSalt) != "salt2" {
		t.Errorf("expected salt2, got %s", gotSalt)
	}
	if gotData["k"] != "v2" {
		t.Errorf("expected v2, got %s", gotData["k"])
	}
}

func TestAuditLog(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	entry := AuditEntry{
		Timestamp: time.Now().UTC(),
		Action:    "model.upsert",
		Resource:  "gpt-4",
		Detail:    `{"weight":8}`,
		RequestID: "req-123",
	}
	if err := s.LogAudit(ctx, entry); err != nil {
		t.Fatalf("log audit failed: %v", err)
	}

	logs, err := s.ListAuditLogs(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list audit logs failed: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 audit log, got %d", len(logs))
	}
	if logs[0].Action != "model.upsert" {
		t.Errorf("expected action model.upsert, got %s", logs[0].Action)
	}
	if logs[0].Resource != "gpt-4" {
		t.Errorf("expected resource gpt-4, got %s", logs[0].Resource)
	}
	if logs[0].Detail != `{"weight":8}` {
		t.Errorf("expected detail {\"weight\":8}, got %s", logs[0].Detail)
	}
	if logs[0].RequestID != "req-123" {
		t.Errorf("expected request_id req-123, got %s", logs[0].RequestID)
	}
}

func TestRewardLog(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	entry := RewardEntry{
		Timestamp:       time.Now().UTC(),
		RequestID:       "req-reward-1",
		ModelID:         "gpt-4",
		ProviderID:      "openai",
		Mode:            "normal",
		EstimatedTokens: 500,
		TokenBucket:     "small",
		LatencyBudgetMs: 5000,
		LatencyMs:       250.5,
		CostUSD:         0.025,
		Success:         true,
		Reward:          0.85,
	}
	if err := s.LogReward(ctx, entry); err != nil {
		t.Fatalf("log reward failed: %v", err)
	}

	// Log a second entry (failure).
	entry2 := RewardEntry{
		Timestamp:       time.Now().UTC(),
		RequestID:       "req-reward-2",
		ModelID:         "claude-opus",
		ProviderID:      "anthropic",
		Mode:            "cheap",
		EstimatedTokens: 15000,
		TokenBucket:     "large",
		LatencyBudgetMs: 10000,
		LatencyMs:       0,
		CostUSD:         0,
		Success:         false,
		ErrorClass:      "routing_failure",
		Reward:          0,
	}
	if err := s.LogReward(ctx, entry2); err != nil {
		t.Fatalf("log reward 2 failed: %v", err)
	}

	logs, err := s.ListRewards(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list rewards failed: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 reward logs, got %d", len(logs))
	}
	// Most recent first.
	if logs[0].ModelID != "claude-opus" {
		t.Errorf("expected claude-opus first (most recent), got %s", logs[0].ModelID)
	}
	if logs[0].Success {
		t.Error("expected first log to be failure")
	}
	if logs[0].ErrorClass != "routing_failure" {
		t.Errorf("expected error_class routing_failure, got %s", logs[0].ErrorClass)
	}
	if logs[1].ModelID != "gpt-4" {
		t.Errorf("expected gpt-4 second, got %s", logs[1].ModelID)
	}
	if !logs[1].Success {
		t.Error("expected second log to be success")
	}
	if logs[1].Reward != 0.85 {
		t.Errorf("expected reward 0.85, got %f", logs[1].Reward)
	}
	if logs[1].TokenBucket != "small" {
		t.Errorf("expected token_bucket small, got %s", logs[1].TokenBucket)
	}
}

func TestRewardLogLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		entry := RewardEntry{
			Timestamp:  time.Now().UTC(),
			ModelID:    "gpt-4",
			ProviderID: "openai",
			Success:    true,
			Reward:     0.5,
		}
		if err := s.LogReward(ctx, entry); err != nil {
			t.Fatalf("log reward failed: %v", err)
		}
	}

	logs, err := s.ListRewards(ctx, 3, 0)
	if err != nil {
		t.Fatalf("list rewards failed: %v", err)
	}
	if len(logs) != 3 {
		t.Errorf("expected 3 rewards with limit, got %d", len(logs))
	}
}

func TestRewardLogDefaultLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	logs, err := s.ListRewards(ctx, 0, 0)
	if err != nil {
		t.Fatalf("list rewards failed: %v", err)
	}
	if logs != nil {
		t.Errorf("expected nil rewards for empty db, got %d", len(logs))
	}
}

func TestVaultBlobEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	salt, data, err := s.LoadVaultBlob(ctx)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if salt != nil {
		t.Errorf("expected nil salt, got %v", salt)
	}
	if data != nil {
		t.Errorf("expected nil data, got %v", data)
	}
}
