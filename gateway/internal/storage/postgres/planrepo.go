package postgres

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pcaokhai/vitts/gateway/internal/plans"
	"github.com/pcaokhai/vitts/gateway/internal/storage/postgres/db"
)

// UpsertPlans writes the operator's plan tiers.
//
// One transaction, so a half-applied plan file cannot leave the fleet enforcing a mix of
// old and new limits.
func UpsertPlans(ctx context.Context, pool *Pool, tiers []plans.Plan) error {
	tx, err := pool.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := pool.Queries.WithTx(tx)
	for _, tier := range tiers {
		overage := pgtype.Numeric{}
		if tier.OverageUSDPerMillion != nil {
			if err := overage.Scan(fmt.Sprintf("%.2f", *tier.OverageUSDPerMillion)); err != nil {
				return fmt.Errorf("plan %q: overage: %w", tier.ID, err)
			}
		}

		if err := q.UpsertPlan(ctx, db.UpsertPlanParams{
			ID:                   tier.ID,
			CharsPerMonth:        tier.CharsPerMonth,
			ReqPerMinute:         tier.ReqPerMinute,
			MaxConcurrentStreams: tier.MaxConcurrentStreams,
			MaxJobChars:          tier.MaxJobChars,
			PriceUsdCents:        tier.PriceUSDCents,
			OverageUsdPerMillion: overage,
		}); err != nil {
			return fmt.Errorf("plan %q: %w", tier.ID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// PlanLimits answers plan questions on the request path. It implements
// http.PlanLimits.
//
// Plans change rarely and are read on every request, so the answer is cached in memory
// with a short TTL: a plan edit takes effect within one TTL without a query per request.
type PlanLimits struct {
	pool *Pool
	ttl  time.Duration

	mu     sync.RWMutex
	cached map[string]cachedPlan
}

type cachedPlan struct {
	reqPerMinute int32
	expiresAt    time.Time
}

// PlanCacheTTL is how long a plan's limits are trusted without re-reading them.
const PlanCacheTTL = 30 * time.Second

// NewPlanLimits wires the cache.
func NewPlanLimits(pool *Pool) *PlanLimits {
	return &PlanLimits{pool: pool, ttl: PlanCacheTTL, cached: make(map[string]cachedPlan)}
}

// CharsPerMonth returns the plan's monthly allowance.
func (p *PlanLimits) CharsPerMonth(ctx context.Context, planID string) (int64, error) {
	plan, err := p.pool.Queries.GetPlan(ctx, planID)
	if err != nil {
		return 0, fmt.Errorf("get plan %q: %w", planID, err)
	}
	return plan.CharsPerMonth, nil
}

// ReqPerMinute returns the plan's request rate.
func (p *PlanLimits) ReqPerMinute(ctx context.Context, planID string) (int32, error) {
	p.mu.RLock()
	entry, ok := p.cached[planID]
	p.mu.RUnlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.reqPerMinute, nil
	}

	plan, err := p.pool.Queries.GetPlan(ctx, planID)
	if err != nil {
		return 0, fmt.Errorf("get plan %q: %w", planID, err)
	}

	p.mu.Lock()
	p.cached[planID] = cachedPlan{reqPerMinute: plan.ReqPerMinute, expiresAt: time.Now().Add(p.ttl)}
	p.mu.Unlock()

	return plan.ReqPerMinute, nil
}
