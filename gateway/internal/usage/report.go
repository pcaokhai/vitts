package usage

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// MaxReportDays bounds a report window. A tenant asking for years of daily rows would
// scan more than the endpoint should ever return in one response.
const MaxReportDays = 366

// Day is one row of a usage report.
type Day struct {
	Day       time.Time `json:"day"`
	Chars     int64     `json:"chars"`
	AudioMS   int64     `json:"audio_ms"`
	Requests  int64     `json:"requests"`
	CacheHits int64     `json:"cache_hits"`
}

// Totals is a period's sums. It is deliberately not a Day: a total has no date, and
// reusing Day here put a zero timestamp in every response.
type Totals struct {
	Chars     int64 `json:"chars"`
	AudioMS   int64 `json:"audio_ms"`
	Requests  int64 `json:"requests"`
	CacheHits int64 `json:"cache_hits"`
}

// Report is a period's usage with its totals (US-16 acceptance criterion 1).
type Report struct {
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Days      []Day     `json:"days"`
	Totals    Totals    `json:"totals"`
	PlanLimit int64     `json:"plan_chars_per_month"`
}

// Reader reads the daily rollup.
type Reader interface {
	ByDay(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]Day, error)
}

// Reporter builds usage reports.
type Reporter struct {
	reader Reader
}

// NewReporter wires the use case.
func NewReporter(reader Reader) *Reporter { return &Reporter{reader: reader} }

// ErrInvalidRange means the requested window cannot be served.
type ErrInvalidRange struct{ Reason string }

func (e *ErrInvalidRange) Error() string { return "invalid range: " + e.Reason }

// Build returns the report for a window, inclusive of both ends.
func (r *Reporter) Build(ctx context.Context, tenantID uuid.UUID, from, to time.Time, planLimit int64) (Report, error) {
	from, to = from.UTC().Truncate(24*time.Hour), to.UTC().Truncate(24*time.Hour)
	if to.Before(from) {
		return Report{}, &ErrInvalidRange{Reason: "to is before from"}
	}
	if to.Sub(from) > MaxReportDays*24*time.Hour {
		return Report{}, &ErrInvalidRange{
			Reason: fmt.Sprintf("window is longer than %d days", MaxReportDays),
		}
	}

	days, err := r.reader.ByDay(ctx, tenantID, from, to)
	if err != nil {
		return Report{}, fmt.Errorf("read usage: %w", err)
	}

	report := Report{From: from, To: to, Days: days, PlanLimit: planLimit}
	for _, day := range days {
		report.Totals.Chars += day.Chars
		report.Totals.AudioMS += day.AudioMS
		report.Totals.Requests += day.Requests
		report.Totals.CacheHits += day.CacheHits
	}
	return report, nil
}

// WriteCSV renders a report as RFC 4180 CSV (US-16 acceptance criterion 3).
//
// encoding/csv quotes fields that need it and uses CRLF, which is what RFC 4180 asks
// for; hand-rolling this is how a comma in a future column becomes a corrupt export.
func WriteCSV(w io.Writer, report Report) error {
	writer := csv.NewWriter(w)
	writer.UseCRLF = true

	if err := writer.Write([]string{"day", "chars", "audio_ms", "requests", "cache_hits"}); err != nil {
		return fmt.Errorf("write csv header: %w", err)
	}
	for _, day := range report.Days {
		record := []string{
			day.Day.Format(time.DateOnly),
			strconv.FormatInt(day.Chars, 10),
			strconv.FormatInt(day.AudioMS, 10),
			strconv.FormatInt(day.Requests, 10),
			strconv.FormatInt(day.CacheHits, 10),
		}
		if err := writer.Write(record); err != nil {
			return fmt.Errorf("write csv row: %w", err)
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("flush csv: %w", err)
	}
	return nil
}
