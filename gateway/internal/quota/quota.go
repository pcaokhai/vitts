// Package quota enforces the monthly character allowance (US-07).
//
// Redis holds the counter because it is on the request path; Postgres remains the truth
// and the hourly reconcile corrects drift (docs/05-data-model.md § 4).
package quota

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	// GraceFactor is the 5% headroom in US-07 acceptance criterion 1: a request is
	// refused once it would take the tenant past limit x 1.05, so a request that merely
	// straddles the limit still completes.
	GraceFactor = 1.05
	// counterTTL outlives the month it counts so a late reconcile still finds the key
	// (docs/05-data-model.md § 2).
	counterTTL = 40 * 24 * time.Hour
)

// ErrExceeded means the request would take the tenant past its allowance.
var ErrExceeded = errors.New("quota exceeded")

// Counter is the storage port: a keyed integer with a TTL. Redis implements it; this
// package must not know that (.claude/rules/gateway-go.md).
type Counter interface {
	// Get returns 0 when the key is absent, which is a valid answer, not an error.
	Get(ctx context.Context, key string) (int64, error)
	Add(ctx context.Context, key string, delta int64, ttl time.Duration) error
	Set(ctx context.Context, key string, value int64, ttl time.Duration) error
}

// Service checks and records character usage.
type Service struct {
	counter Counter
	now     func() time.Time
}

// New wires the service.
func New(counter Counter) *Service {
	return &Service{counter: counter, now: time.Now}
}

// Decision is the outcome of a quota check.
type Decision struct {
	Allowed bool
	Used    int64
	Limit   int64
	// Ceiling is the enforced bound, Limit x GraceFactor.
	Ceiling int64
}

// Check reports whether `chars` more characters fit in the tenant's allowance.
//
// It does not consume anything: the counter moves only after a successful synthesis
// (US-07 acceptance criterion 2), because charging up front would bill work that a
// worker failure never delivered.
func (s *Service) Check(ctx context.Context, tenantID uuid.UUID, planLimit int64, chars int64) (Decision, error) {
	// A zero allowance is pay-as-you-go, not a blocked tenant: metering still happens,
	// there is simply nothing to exceed.
	if planLimit <= 0 {
		return Decision{Allowed: true, Limit: planLimit}, nil
	}

	used, err := s.used(ctx, tenantID)
	if err != nil {
		return Decision{}, err
	}

	ceiling := int64(float64(planLimit) * GraceFactor)
	return Decision{
		Allowed: used+chars <= ceiling,
		Used:    used,
		Limit:   planLimit,
		Ceiling: ceiling,
	}, nil
}

// Record adds consumed characters to the month's counter.
//
// Called after synthesis, with the characters actually produced: a cancelled stream
// records what it delivered, not what it was asked for (ADR-010).
func (s *Service) Record(ctx context.Context, tenantID uuid.UUID, chars int64) error {
	if chars <= 0 {
		return nil
	}

	if err := s.counter.Add(ctx, s.key(tenantID, s.now()), chars, counterTTL); err != nil {
		return fmt.Errorf("record usage: %w", err)
	}
	return nil
}

// Set overwrites the counter. Used only by the reconcile job, which has just read the
// authoritative total from Postgres.
func (s *Service) Set(ctx context.Context, tenantID uuid.UUID, month time.Time, chars int64) error {
	if err := s.counter.Set(ctx, s.key(tenantID, month), chars, counterTTL); err != nil {
		return fmt.Errorf("set usage: %w", err)
	}
	return nil
}

// Used returns the month-to-date character count.
func (s *Service) Used(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	return s.used(ctx, tenantID)
}

func (s *Service) used(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	value, err := s.counter.Get(ctx, s.key(tenantID, s.now()))
	if err != nil {
		return 0, fmt.Errorf("read usage: %w", err)
	}
	return value, nil
}

// key is usage:{tenant_id}:{yyyymm} in UTC. Months are UTC so a tenant does not get two
// resets or none when a deployment moves timezone (docs/05-data-model.md).
func (s *Service) key(tenantID uuid.UUID, at time.Time) string {
	return fmt.Sprintf("usage:%s:%s", tenantID, at.UTC().Format("200601"))
}
