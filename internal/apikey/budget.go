package apikey

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jordanhubbard/tokenhub/internal/store"
)

const budgetCacheTTL = 30 * time.Second

// BudgetExceededError is returned when an API key has exceeded its monthly budget.
type BudgetExceededError struct {
	BudgetUSD float64
	SpentUSD  float64
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("monthly budget exceeded: budget=$%.2f, spent=$%.4f", e.BudgetUSD, e.SpentUSD)
}

type cachedSpend struct {
	amount    float64
	expiresAt time.Time
}

// BudgetChecker validates per-API-key monthly spending limits.
// It uses a short in-memory cache (30s TTL) to avoid hitting the DB on every request.
type BudgetChecker struct {
	store store.Store

	mu    sync.RWMutex
	cache map[string]cachedSpend // api_key_id -> cached spend
}

// NewBudgetChecker creates a new BudgetChecker.
func NewBudgetChecker(s store.Store) *BudgetChecker {
	return &BudgetChecker{
		store: s,
		cache: make(map[string]cachedSpend),
	}
}

// CheckBudget verifies whether the API key is within its monthly spending limit.
// Returns nil if the budget is unlimited (0) or not exceeded.
// Returns a *BudgetExceededError if the monthly spend exceeds the budget.
func (bc *BudgetChecker) CheckBudget(ctx context.Context, keyRecord *store.APIKeyRecord) error {
	if keyRecord == nil || keyRecord.MonthlyBudgetUSD <= 0 {
		return nil // unlimited
	}

	spent, err := bc.getSpend(ctx, keyRecord.ID)
	if err != nil {
		return fmt.Errorf("budget check: %w", err)
	}

	if spent >= keyRecord.MonthlyBudgetUSD {
		return &BudgetExceededError{
			BudgetUSD: keyRecord.MonthlyBudgetUSD,
			SpentUSD:  spent,
		}
	}
	return nil
}

// getSpend returns the monthly spend for an API key, using cache when available.
func (bc *BudgetChecker) getSpend(ctx context.Context, apiKeyID string) (float64, error) {
	bc.mu.RLock()
	if cached, ok := bc.cache[apiKeyID]; ok && time.Now().Before(cached.expiresAt) {
		bc.mu.RUnlock()
		return cached.amount, nil
	}
	bc.mu.RUnlock()

	spent, err := bc.store.GetMonthlySpend(ctx, apiKeyID)
	if err != nil {
		return 0, err
	}

	bc.mu.Lock()
	bc.cache[apiKeyID] = cachedSpend{
		amount:    spent,
		expiresAt: time.Now().Add(budgetCacheTTL),
	}
	bc.mu.Unlock()

	return spent, nil
}

// InvalidateCache removes the cached spend for a specific API key.
// Call this after logging a request to ensure the next budget check is fresh.
func (bc *BudgetChecker) InvalidateCache(apiKeyID string) {
	bc.mu.Lock()
	delete(bc.cache, apiKeyID)
	bc.mu.Unlock()
}
