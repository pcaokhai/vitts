package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/pcaokhai/vitts/gateway/internal/storage/postgres/db"
	"github.com/pcaokhai/vitts/gateway/internal/voices"
)

// VoiceRepository implements voices.Repository.
type VoiceRepository struct {
	pool *Pool
}

// NewVoiceRepository wires the repository.
func NewVoiceRepository(pool *Pool) *VoiceRepository { return &VoiceRepository{pool: pool} }

// List returns the voices visible to a caller.
func (r *VoiceRepository) List(ctx context.Context, tenantID *uuid.UUID) ([]voices.Voice, error) {
	if tenantID == nil {
		rows, err := r.pool.Queries.ListPublicVoices(ctx)
		if err != nil {
			return nil, fmt.Errorf("list public voices: %w", err)
		}
		return toVoices(rows), nil
	}

	rows, err := r.pool.Queries.ListVoicesForTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list voices for tenant: %w", err)
	}
	return toVoices(rows), nil
}

// Upsert records a voice the fleet advertises.
func (r *VoiceRepository) Upsert(ctx context.Context, voice voices.Voice) error {
	tags := voice.Tags
	if tags == nil {
		tags = []string{}
	}

	if err := r.pool.Queries.UpsertVoice(ctx, db.UpsertVoiceParams{
		ID:           voice.ID,
		DisplayName:  voice.DisplayName,
		Gender:       nullableString(voice.Gender),
		Tags:         tags,
		ModelVersion: voice.ModelVersion,
		TenantID:     voice.TenantID,
		Enabled:      voice.Enabled,
	}); err != nil {
		return fmt.Errorf("upsert voice: %w", err)
	}
	return nil
}

func toVoices(rows []db.Voice) []voices.Voice {
	out := make([]voices.Voice, 0, len(rows))
	for _, row := range rows {
		voice := voices.Voice{
			ID:           row.ID,
			DisplayName:  row.DisplayName,
			Tags:         row.Tags,
			ModelVersion: row.ModelVersion,
			TenantID:     row.TenantID,
			Enabled:      row.Enabled,
		}
		if row.Gender != nil {
			voice.Gender = *row.Gender
		}
		out = append(out, voice)
	}
	return out
}

func nullableString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
