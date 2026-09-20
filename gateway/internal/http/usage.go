package http

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/pcaokhai/vitts/gateway/internal/usage"
)

// UsageReporter is the use case port.
type UsageReporter interface {
	Build(ctx context.Context, tenantID uuid.UUID, from, to time.Time, planLimit int64) (usage.Report, error)
}

// UsageLimits answers the plan's monthly allowance, so a report can show usage against it.
type UsageLimits interface {
	CharsPerMonth(ctx context.Context, planID string) (int64, error)
}

// defaultUsageWindow is what a caller gets without explicit dates: the current month to
// date, which is the question almost every caller is actually asking.
const defaultUsageWindow = 30 * 24 * time.Hour

// GetUsage handles GET /v1/usage (US-16).
//
// `format=csv` returns RFC 4180 CSV; anything else returns JSON.
func GetUsage(reporter UsageReporter, limits UsageLimits) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := IdentityFrom(r.Context())
		if !ok {
			WriteProblem(w, r, NewError(CodeUnauthorized, "missing or malformed API key"))
			return
		}

		now := time.Now().UTC()
		from, to := now.Add(-defaultUsageWindow), now

		if raw := r.URL.Query().Get("from"); raw != "" {
			parsed, err := time.Parse(time.DateOnly, raw)
			if err != nil {
				WriteProblem(w, r, NewError(CodeInvalidRequest, "from must be YYYY-MM-DD"))
				return
			}
			from = parsed
		}
		if raw := r.URL.Query().Get("to"); raw != "" {
			parsed, err := time.Parse(time.DateOnly, raw)
			if err != nil {
				WriteProblem(w, r, NewError(CodeInvalidRequest, "to must be YYYY-MM-DD"))
				return
			}
			to = parsed
		}

		planLimit, err := limits.CharsPerMonth(r.Context(), identity.PlanID)
		if err != nil {
			WriteProblem(w, r, &Error{Code: CodeInternal, Cause: err})
			return
		}

		report, err := reporter.Build(r.Context(), identity.TenantID, from, to, planLimit)
		if err != nil {
			var invalid *usage.ErrInvalidRange
			if errors.As(err, &invalid) {
				WriteProblem(w, r, NewError(CodeInvalidRequest, invalid.Error()))
				return
			}
			WriteProblem(w, r, &Error{Code: CodeInternal, Cause: err})
			return
		}

		if r.URL.Query().Get("format") == "csv" {
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="usage.csv"`)
			w.WriteHeader(http.StatusOK)
			if err := usage.WriteCSV(w, report); err != nil {
				// The status is already sent; the access log carries the outcome.
				return
			}
			return
		}

		writeJSON(w, r, http.StatusOK, report)
	}
}
