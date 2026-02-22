package apikey

import (
	"context"
	"fmt"

	"github.com/jordanhubbard/tokenhub/internal/store"
)

// BudgetExceededError is returned when an API key has exceeded its monthly budget.
type BudgetExceededError struct {
	BudgetUSD float64
	SpentUSD  float64
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("monthly budget exceeded: budget=$%.2f, spent=$%.4f", e.BudgetUSD, e.SpentUSD)
}

// BudgetChecker validates per-API-key monthly spending limits.
// Each check reads directly from the store to avoid stale-cache over-spend windows.
type BudgetChecker struct {
	store store.Store
}

// NewBudgetChecker creates a new BudgetChecker.
func NewBudgetChecker(s store.Store) *BudgetChecker {
	return &BudgetChecker{store: s}
}

// CheckBudget verifies whether the API key is within its monthly spending limit.
// Returns nil if the budget is unlimited (0) or not exceeded.
// Returns a *BudgetExceededError if the monthly spend exceeds the budget.
func (bc *BudgetChecker) CheckBudget(ctx context.Context, keyRecord *store.APIKeyRecord) error {
	if keyRecord == nil || keyRecord.MonthlyBudgetUSD <= 0 {
		return nil // unlimited
	}

	spent, err := bc.store.GetMonthlySpend(ctx, keyRecord.ID)
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

// InvalidateCache is a no-op retained for API compatibility.
// Budget checks always read directly from the store, so there is no cache to invalidate.
func (bc *BudgetChecker) InvalidateCache(_ string) {}
