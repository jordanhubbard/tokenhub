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
	t.Cleanup(func() { _ = s.Close() })
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

func TestGetRewardSummary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Log several rewards for different models/buckets.
	entries := []RewardEntry{
		{Timestamp: time.Now(), ModelID: "gpt-4", ProviderID: "openai", Mode: "normal", TokenBucket: "small", Success: true, Reward: 0.8},
		{Timestamp: time.Now(), ModelID: "gpt-4", ProviderID: "openai", Mode: "normal", TokenBucket: "small", Success: true, Reward: 0.9},
		{Timestamp: time.Now(), ModelID: "gpt-4", ProviderID: "openai", Mode: "normal", TokenBucket: "small", Success: false, Reward: 0.0},
		{Timestamp: time.Now(), ModelID: "claude", ProviderID: "anthropic", Mode: "normal", TokenBucket: "small", Success: true, Reward: 0.7},
		{Timestamp: time.Now(), ModelID: "gpt-4", ProviderID: "openai", Mode: "normal", TokenBucket: "large", Success: true, Reward: 0.5},
	}
	for _, e := range entries {
		if err := s.LogReward(ctx, e); err != nil {
			t.Fatalf("log reward failed: %v", err)
		}
	}

	summaries, err := s.GetRewardSummary(ctx)
	if err != nil {
		t.Fatalf("get reward summary failed: %v", err)
	}

	// Expect 3 groups: (gpt-4, small), (claude, small), (gpt-4, large).
	if len(summaries) != 3 {
		t.Fatalf("expected 3 summaries, got %d", len(summaries))
	}

	// Find gpt-4/small summary.
	var gpt4Small *RewardSummary
	for i := range summaries {
		if summaries[i].ModelID == "gpt-4" && summaries[i].TokenBucket == "small" {
			gpt4Small = &summaries[i]
		}
	}
	if gpt4Small == nil {
		t.Fatal("gpt-4/small summary not found")
	}
	if gpt4Small.Count != 3 {
		t.Errorf("expected count 3, got %d", gpt4Small.Count)
	}
	if gpt4Small.Successes != 2 {
		t.Errorf("expected 2 successes, got %d", gpt4Small.Successes)
	}
	if gpt4Small.SumReward < 1.69 || gpt4Small.SumReward > 1.71 {
		t.Errorf("expected sum_reward ~1.7, got %f", gpt4Small.SumReward)
	}
}

func TestGetRewardSummaryEmpty(t *testing.T) {
	s := newTestStore(t)
	summaries, err := s.GetRewardSummary(context.Background())
	if err != nil {
		t.Fatalf("get reward summary failed: %v", err)
	}
	if summaries != nil {
		t.Errorf("expected nil for empty db, got %d", len(summaries))
	}
}

func TestAPIKeysCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Create
	key := APIKeyRecord{
		ID:           "key-1",
		KeyHash:      "$2a$10$fakehashvalue",
		KeyPrefix:    "tokenhub_abcd1234",
		Name:         "test-key",
		Scopes:       `["chat","plan"]`,
		CreatedAt:    time.Now().UTC(),
		RotationDays: 30,
		Enabled:      true,
	}
	if err := s.CreateAPIKey(ctx, key); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Get
	got, err := s.GetAPIKey(ctx, "key-1")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected key, got nil")
	}
	if got.Name != "test-key" {
		t.Errorf("expected name test-key, got %s", got.Name)
	}
	if got.KeyHash != "$2a$10$fakehashvalue" {
		t.Errorf("expected hash stored, got %s", got.KeyHash)
	}
	if !got.Enabled {
		t.Error("expected enabled")
	}
	if got.RotationDays != 30 {
		t.Errorf("expected rotation_days 30, got %d", got.RotationDays)
	}

	// List
	all, err := s.ListAPIKeys(ctx)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 key, got %d", len(all))
	}

	// Update
	got.Name = "updated-key"
	got.Enabled = false
	now := time.Now().UTC()
	got.LastUsedAt = &now
	if err := s.UpdateAPIKey(ctx, *got); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	got, _ = s.GetAPIKey(ctx, "key-1")
	if got.Name != "updated-key" {
		t.Errorf("expected updated name, got %s", got.Name)
	}
	if got.Enabled {
		t.Error("expected disabled after update")
	}
	if got.LastUsedAt == nil {
		t.Error("expected last_used_at to be set")
	}

	// Delete
	if err := s.DeleteAPIKey(ctx, "key-1"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	got, _ = s.GetAPIKey(ctx, "key-1")
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestAPIKeyNotFound(t *testing.T) {
	s := newTestStore(t)
	got, err := s.GetAPIKey(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent key")
	}
}

func TestAPIKeyWithExpiry(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	expires := time.Now().UTC().Add(24 * time.Hour)
	key := APIKeyRecord{
		ID:        "key-exp",
		KeyHash:   "$2a$10$hash",
		KeyPrefix: "tokenhub_prefix",
		Name:      "expiring-key",
		Scopes:    `["chat"]`,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: &expires,
		Enabled:   true,
	}
	if err := s.CreateAPIKey(ctx, key); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	got, err := s.GetAPIKey(ctx, "key-exp")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if got.ExpiresAt == nil {
		t.Fatal("expected expires_at to be set")
	}
	if got.ExpiresAt.Before(time.Now()) {
		t.Error("expected expires_at to be in the future")
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

func TestListExpiredRotationKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Key 1: rotation_days=1, created 2 days ago — should be expired.
	expired := APIKeyRecord{
		ID:           "key-expired",
		KeyHash:      "$2a$10$hash1",
		KeyPrefix:    "tokenhub_aaaaaaaa",
		Name:         "expired-key",
		Scopes:       `["chat"]`,
		CreatedAt:    time.Now().UTC().Add(-48 * time.Hour),
		RotationDays: 1,
		Enabled:      true,
	}
	if err := s.CreateAPIKey(ctx, expired); err != nil {
		t.Fatalf("create expired key failed: %v", err)
	}

	// Key 2: rotation_days=90, created 1 day ago — should NOT be expired.
	fresh := APIKeyRecord{
		ID:           "key-fresh",
		KeyHash:      "$2a$10$hash2",
		KeyPrefix:    "tokenhub_bbbbbbbb",
		Name:         "fresh-key",
		Scopes:       `["chat"]`,
		CreatedAt:    time.Now().UTC().Add(-24 * time.Hour),
		RotationDays: 90,
		Enabled:      true,
	}
	if err := s.CreateAPIKey(ctx, fresh); err != nil {
		t.Fatalf("create fresh key failed: %v", err)
	}

	// Key 3: rotation_days=0 (manual rotation), created 100 days ago — should NOT be expired.
	manual := APIKeyRecord{
		ID:           "key-manual",
		KeyHash:      "$2a$10$hash3",
		KeyPrefix:    "tokenhub_cccccccc",
		Name:         "manual-key",
		Scopes:       `["chat"]`,
		CreatedAt:    time.Now().UTC().Add(-100 * 24 * time.Hour),
		RotationDays: 0,
		Enabled:      true,
	}
	if err := s.CreateAPIKey(ctx, manual); err != nil {
		t.Fatalf("create manual key failed: %v", err)
	}

	// Key 4: rotation_days=1, created 2 days ago, but DISABLED — should NOT appear.
	disabledExpired := APIKeyRecord{
		ID:           "key-disabled-expired",
		KeyHash:      "$2a$10$hash4",
		KeyPrefix:    "tokenhub_dddddddd",
		Name:         "disabled-expired-key",
		Scopes:       `["chat"]`,
		CreatedAt:    time.Now().UTC().Add(-48 * time.Hour),
		RotationDays: 1,
		Enabled:      false,
	}
	if err := s.CreateAPIKey(ctx, disabledExpired); err != nil {
		t.Fatalf("create disabled expired key failed: %v", err)
	}

	keys, err := s.ListExpiredRotationKeys(ctx)
	if err != nil {
		t.Fatalf("list expired rotation keys failed: %v", err)
	}

	if len(keys) != 1 {
		t.Fatalf("expected 1 expired key, got %d", len(keys))
	}
	if keys[0].ID != "key-expired" {
		t.Errorf("expected key-expired, got %s", keys[0].ID)
	}
	if !keys[0].Enabled {
		t.Error("expected key to still be enabled (query returns enabled keys)")
	}
}

func TestListExpiredRotationKeysEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	keys, err := s.ListExpiredRotationKeys(ctx)
	if err != nil {
		t.Fatalf("list expired rotation keys failed: %v", err)
	}
	if keys != nil {
		t.Errorf("expected nil for empty db, got %d keys", len(keys))
	}
}

func TestListExpiredRotationKeysBoundary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Key with rotation_days=1 created exactly 1 day ago — should be expired
	// (the rotation period has elapsed).
	boundary := APIKeyRecord{
		ID:           "key-boundary",
		KeyHash:      "$2a$10$hash5",
		KeyPrefix:    "tokenhub_eeeeeeee",
		Name:         "boundary-key",
		Scopes:       `["chat"]`,
		CreatedAt:    time.Now().UTC().Add(-24 * time.Hour),
		RotationDays: 1,
		Enabled:      true,
	}
	if err := s.CreateAPIKey(ctx, boundary); err != nil {
		t.Fatalf("create boundary key failed: %v", err)
	}

	keys, err := s.ListExpiredRotationKeys(ctx)
	if err != nil {
		t.Fatalf("list expired rotation keys failed: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 expired key at boundary, got %d", len(keys))
	}
	if keys[0].ID != "key-boundary" {
		t.Errorf("expected key-boundary, got %s", keys[0].ID)
	}
}
