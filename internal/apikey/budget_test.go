package apikey

import (
	"context"
	"testing"
	"time"

	"github.com/jordanhubbard/tokenhub/internal/store"
)

func newTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	s, err := store.NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCheckBudget_Unlimited(t *testing.T) {
	s := newTestStore(t)
	bc := NewBudgetChecker(s)

	// Budget of 0 means unlimited.
	rec := &store.APIKeyRecord{
		ID:               "key1",
		MonthlyBudgetUSD: 0,
	}
	if err := bc.CheckBudget(context.Background(), rec); err != nil {
		t.Errorf("expected nil error for unlimited budget, got %v", err)
	}
}

func TestCheckBudget_NilRecord(t *testing.T) {
	s := newTestStore(t)
	bc := NewBudgetChecker(s)

	if err := bc.CheckBudget(context.Background(), nil); err != nil {
		t.Errorf("expected nil error for nil record, got %v", err)
	}
}

func TestCheckBudget_UnderBudget(t *testing.T) {
	s := newTestStore(t)
	bc := NewBudgetChecker(s)
	ctx := context.Background()

	// Create a key with a $10 monthly budget.
	rec := &store.APIKeyRecord{
		ID:               "key-under",
		MonthlyBudgetUSD: 10.0,
	}

	// Log some spending below the budget.
	_ = s.LogRequest(ctx, store.RequestLog{
		Timestamp:        time.Now().UTC(),
		ModelID:          "gpt-4",
		ProviderID:       "openai",
		EstimatedCostUSD: 2.50,
		StatusCode:       200,
		APIKeyID:         "key-under",
	})
	_ = s.LogRequest(ctx, store.RequestLog{
		Timestamp:        time.Now().UTC(),
		ModelID:          "gpt-4",
		ProviderID:       "openai",
		EstimatedCostUSD: 3.00,
		StatusCode:       200,
		APIKeyID:         "key-under",
	})

	err := bc.CheckBudget(ctx, rec)
	if err != nil {
		t.Errorf("expected nil error for under-budget key, got %v", err)
	}
}

func TestCheckBudget_ExceedsBudget(t *testing.T) {
	s := newTestStore(t)
	bc := NewBudgetChecker(s)
	ctx := context.Background()

	rec := &store.APIKeyRecord{
		ID:               "key-over",
		MonthlyBudgetUSD: 5.0,
	}

	// Log spending that exceeds the budget.
	_ = s.LogRequest(ctx, store.RequestLog{
		Timestamp:        time.Now().UTC(),
		ModelID:          "gpt-4",
		ProviderID:       "openai",
		EstimatedCostUSD: 3.00,
		StatusCode:       200,
		APIKeyID:         "key-over",
	})
	_ = s.LogRequest(ctx, store.RequestLog{
		Timestamp:        time.Now().UTC(),
		ModelID:          "gpt-4",
		ProviderID:       "openai",
		EstimatedCostUSD: 3.00,
		StatusCode:       200,
		APIKeyID:         "key-over",
	})

	err := bc.CheckBudget(ctx, rec)
	if err == nil {
		t.Fatal("expected error for over-budget key, got nil")
	}

	budgetErr, ok := err.(*BudgetExceededError)
	if !ok {
		t.Fatalf("expected *BudgetExceededError, got %T", err)
	}
	if budgetErr.BudgetUSD != 5.0 {
		t.Errorf("expected budget $5.00, got $%.2f", budgetErr.BudgetUSD)
	}
	if budgetErr.SpentUSD != 6.0 {
		t.Errorf("expected spent $6.00, got $%.2f", budgetErr.SpentUSD)
	}
}

func TestCheckBudget_ExactBudget(t *testing.T) {
	s := newTestStore(t)
	bc := NewBudgetChecker(s)
	ctx := context.Background()

	rec := &store.APIKeyRecord{
		ID:               "key-exact",
		MonthlyBudgetUSD: 5.0,
	}

	// Log spending that exactly meets the budget.
	_ = s.LogRequest(ctx, store.RequestLog{
		Timestamp:        time.Now().UTC(),
		ModelID:          "gpt-4",
		ProviderID:       "openai",
		EstimatedCostUSD: 5.00,
		StatusCode:       200,
		APIKeyID:         "key-exact",
	})

	err := bc.CheckBudget(ctx, rec)
	if err == nil {
		t.Fatal("expected error when spend equals budget, got nil")
	}

	budgetErr, ok := err.(*BudgetExceededError)
	if !ok {
		t.Fatalf("expected *BudgetExceededError, got %T", err)
	}
	if budgetErr.SpentUSD != 5.0 {
		t.Errorf("expected spent $5.00, got $%.2f", budgetErr.SpentUSD)
	}
}

func TestCheckBudget_DifferentKeys(t *testing.T) {
	s := newTestStore(t)
	bc := NewBudgetChecker(s)
	ctx := context.Background()

	// Key A has a $5 budget with $4 spent.
	recA := &store.APIKeyRecord{
		ID:               "key-a",
		MonthlyBudgetUSD: 5.0,
	}
	_ = s.LogRequest(ctx, store.RequestLog{
		Timestamp:        time.Now().UTC(),
		ModelID:          "gpt-4",
		ProviderID:       "openai",
		EstimatedCostUSD: 4.00,
		StatusCode:       200,
		APIKeyID:         "key-a",
	})

	// Key B has a $5 budget with $6 spent.
	recB := &store.APIKeyRecord{
		ID:               "key-b",
		MonthlyBudgetUSD: 5.0,
	}
	_ = s.LogRequest(ctx, store.RequestLog{
		Timestamp:        time.Now().UTC(),
		ModelID:          "gpt-4",
		ProviderID:       "openai",
		EstimatedCostUSD: 6.00,
		StatusCode:       200,
		APIKeyID:         "key-b",
	})

	// Key A should pass.
	if err := bc.CheckBudget(ctx, recA); err != nil {
		t.Errorf("expected key-a to pass, got %v", err)
	}

	// Key B should fail.
	if err := bc.CheckBudget(ctx, recB); err == nil {
		t.Error("expected key-b to fail budget check")
	}
}

func TestCheckBudget_CacheBehavior(t *testing.T) {
	s := newTestStore(t)
	bc := NewBudgetChecker(s)
	ctx := context.Background()

	rec := &store.APIKeyRecord{
		ID:               "key-cache",
		MonthlyBudgetUSD: 10.0,
	}

	// Log $3 of spending.
	_ = s.LogRequest(ctx, store.RequestLog{
		Timestamp:        time.Now().UTC(),
		ModelID:          "gpt-4",
		ProviderID:       "openai",
		EstimatedCostUSD: 3.00,
		StatusCode:       200,
		APIKeyID:         "key-cache",
	})

	// First check populates cache.
	if err := bc.CheckBudget(ctx, rec); err != nil {
		t.Fatalf("first check failed: %v", err)
	}

	// Verify cache is populated.
	bc.mu.RLock()
	_, cached := bc.cache["key-cache"]
	bc.mu.RUnlock()
	if !cached {
		t.Error("expected cache to be populated after first check")
	}

	// Add more spending that would exceed budget.
	_ = s.LogRequest(ctx, store.RequestLog{
		Timestamp:        time.Now().UTC(),
		ModelID:          "gpt-4",
		ProviderID:       "openai",
		EstimatedCostUSD: 8.00,
		StatusCode:       200,
		APIKeyID:         "key-cache",
	})

	// Second check should use cached value ($3) and still pass.
	if err := bc.CheckBudget(ctx, rec); err != nil {
		t.Errorf("second check should use cache: %v", err)
	}

	// Invalidate cache and check again -- should now fail.
	bc.InvalidateCache("key-cache")

	bc.mu.RLock()
	_, cached = bc.cache["key-cache"]
	bc.mu.RUnlock()
	if cached {
		t.Error("expected cache to be cleared after invalidation")
	}

	err := bc.CheckBudget(ctx, rec)
	if err == nil {
		t.Fatal("expected failure after cache invalidation with over-budget spend")
	}

	budgetErr, ok := err.(*BudgetExceededError)
	if !ok {
		t.Fatalf("expected *BudgetExceededError, got %T", err)
	}
	if budgetErr.SpentUSD != 11.0 {
		t.Errorf("expected spent $11.00, got $%.2f", budgetErr.SpentUSD)
	}
}

func TestBudgetExceededError_Error(t *testing.T) {
	err := &BudgetExceededError{
		BudgetUSD: 10.00,
		SpentUSD:  12.50,
	}
	expected := "monthly budget exceeded: budget=$10.00, spent=$12.5000"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}
