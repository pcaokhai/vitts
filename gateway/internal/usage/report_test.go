package usage_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/usage"
)

type stubReader struct {
	days []usage.Day
	from time.Time
	to   time.Time
}

func (s *stubReader) ByDay(_ context.Context, _ uuid.UUID, from, to time.Time) ([]usage.Day, error) {
	s.from, s.to = from, to
	return s.days, nil
}

func day(date string, chars, audioMS, requests, hits int64) usage.Day {
	parsed, _ := time.Parse(time.DateOnly, date)
	return usage.Day{Day: parsed, Chars: chars, AudioMS: audioMS, Requests: requests, CacheHits: hits}
}

func TestReportTotalsTheWindow(t *testing.T) {
	t.Parallel()

	reader := &stubReader{days: []usage.Day{
		day("2026-09-01", 1000, 60_000, 12, 3),
		day("2026-09-02", 2500, 150_000, 30, 11),
	}}
	from, _ := time.Parse(time.DateOnly, "2026-09-01")
	to, _ := time.Parse(time.DateOnly, "2026-09-30")

	report, err := usage.NewReporter(reader).Build(context.Background(), uuid.New(), from, to, 100_000)

	require.NoError(t, err)
	require.Len(t, report.Days, 2)
	require.Equal(t, usage.Totals{Chars: 3500, AudioMS: 210_000, Requests: 42, CacheHits: 14},
		report.Totals, "a total has no date of its own")
	require.Equal(t, int64(3500), report.Totals.Chars)
	require.Equal(t, int64(210_000), report.Totals.AudioMS)
	require.Equal(t, int64(42), report.Totals.Requests)
	require.Equal(t, int64(14), report.Totals.CacheHits)
	require.Equal(t, int64(100_000), report.PlanLimit, "usage is shown against the plan")
}

func TestReportRejectsABackwardsWindow(t *testing.T) {
	t.Parallel()

	from, _ := time.Parse(time.DateOnly, "2026-09-30")
	to, _ := time.Parse(time.DateOnly, "2026-09-01")

	_, err := usage.NewReporter(&stubReader{}).Build(context.Background(), uuid.New(), from, to, 0)

	var invalid *usage.ErrInvalidRange
	require.ErrorAs(t, err, &invalid)
}

func TestReportRejectsAnOversizedWindow(t *testing.T) {
	t.Parallel()

	from := time.Now().AddDate(-3, 0, 0)

	_, err := usage.NewReporter(&stubReader{}).Build(context.Background(), uuid.New(), from, time.Now(), 0)

	var invalid *usage.ErrInvalidRange
	require.ErrorAs(t, err, &invalid)
	require.Contains(t, err.Error(), "longer than")
}

func TestReportNormalisesToUTCDays(t *testing.T) {
	t.Parallel()

	reader := &stubReader{}
	ict := time.FixedZone("ICT", 7*60*60)
	from := time.Date(2026, 9, 1, 23, 30, 0, 0, ict)

	_, err := usage.NewReporter(reader).Build(context.Background(), uuid.New(), from, from.Add(48*time.Hour), 0)

	require.NoError(t, err)
	require.Equal(t, time.UTC, reader.from.Location(), "storage and reporting agree on UTC")
	require.Equal(t, 0, reader.from.Hour())
}

// US-16 acceptance criterion 3.
func TestCSVHasAHeaderAndParsesAsRFC4180(t *testing.T) {
	t.Parallel()

	report := usage.Report{Days: []usage.Day{
		day("2026-09-01", 1000, 60_000, 12, 3),
		day("2026-09-02", 2500, 150_000, 30, 11),
	}}

	var out bytes.Buffer
	require.NoError(t, usage.WriteCSV(&out, report))

	require.True(t, strings.HasSuffix(strings.SplitN(out.String(), "\n", 2)[0], "\r"),
		"RFC 4180 uses CRLF line endings")

	records, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	require.NoError(t, err)
	require.Equal(t, []string{"day", "chars", "audio_ms", "requests", "cache_hits"}, records[0])
	require.Equal(t, []string{"2026-09-01", "1000", "60000", "12", "3"}, records[1])
	require.Len(t, records, 3)
}

func TestCSVOfAnEmptyPeriodIsStillValid(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	require.NoError(t, usage.WriteCSV(&out, usage.Report{}))

	records, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 1, "a header row, so a consumer can still parse it")
}
