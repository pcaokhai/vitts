package synth

import (
	"context"

	"github.com/pcaokhai/vitts/gateway/internal/usage"
)

// MeterAdapter lets the usage meter satisfy this package's Meter port without either
// side importing the other's types.
type MeterAdapter struct {
	meter *usage.Meter
}

// NewMeterAdapter wires the adapter.
func NewMeterAdapter(meter *usage.Meter) *MeterAdapter { return &MeterAdapter{meter: meter} }

// Record forwards one metered request.
func (a *MeterAdapter) Record(ctx context.Context, u Usage) {
	a.meter.Record(ctx, usage.Record{
		TenantID: u.TenantID, KeyID: u.KeyID, VoiceID: u.VoiceID,
		CacheKey: u.CacheKey, Mode: u.Mode, Chars: u.Chars,
		DurationMS: u.DurationMS, TTFAMS: u.TTFAMS, Cached: u.Cached, Status: u.Status,
	})
}
