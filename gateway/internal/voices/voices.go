// Package voices is the voice catalogue: what a tenant may ask for, and what the
// deployed workers can actually produce.
package voices

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ErrUnknown means the voice does not exist for this caller.
var ErrUnknown = errors.New("unknown voice")

// Voice is one entry in the catalogue.
type Voice struct {
	ID           string
	DisplayName  string
	Gender       string
	Tags         []string
	ModelVersion string
	// TenantID is set for a tenant-owned voice and nil for a public preset (F-15).
	TenantID *uuid.UUID
	Enabled  bool
	// PreviewURL is generated at deploy from a fixed sentence (US-13 AC-3); empty until
	// the preview script has run.
	PreviewURL string
}

// Repository is the storage port.
type Repository interface {
	// List returns public presets plus the voices owned by tenantID, or only the public
	// presets when tenantID is nil (US-13 AC-2).
	List(ctx context.Context, tenantID *uuid.UUID) ([]Voice, error)
	Upsert(ctx context.Context, voice Voice) error
}

// Fleet reports what the deployed workers advertise.
type Fleet interface {
	AdvertisedVoices() map[string]string // voice id -> model version
}

// Service answers catalogue questions.
type Service struct {
	repo  Repository
	fleet Fleet

	mu      sync.RWMutex
	cached  []Voice
	fetched time.Time
	ttl     time.Duration
}

// CacheTTL is how long a catalogue listing is reused. Voices change at deploy time, so
// a short cache removes a query from a request that any tenant may make anonymously.
const CacheTTL = 30 * time.Second

// NewService wires the catalogue.
func NewService(repo Repository, fleet Fleet) *Service {
	return &Service{repo: repo, fleet: fleet, ttl: CacheTTL}
}

// List returns the voices visible to a caller. A nil tenantID is an anonymous caller,
// which US-13 acceptance criterion 1 allows.
func (s *Service) List(ctx context.Context, tenantID *uuid.UUID) ([]Voice, error) {
	if tenantID == nil {
		if cached, ok := s.cachedPublic(); ok {
			return cached, nil
		}
	}

	found, err := s.repo.List(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list voices: %w", err)
	}

	sort.Slice(found, func(i, j int) bool { return found[i].ID < found[j].ID })

	if tenantID == nil {
		s.mu.Lock()
		s.cached, s.fetched = found, time.Now()
		s.mu.Unlock()
	}
	return found, nil
}

// Resolve checks that a voice exists and is usable by this caller.
//
// A tenant asking for another tenant's voice gets ErrUnknown, not a permission error:
// the existence of another tenant's voice is not ours to disclose
// (docs/07-permissions.md).
func (s *Service) Resolve(ctx context.Context, tenantID *uuid.UUID, id string) (Voice, error) {
	if id == "" {
		return Voice{}, fmt.Errorf("%w: no voice requested", ErrUnknown)
	}

	available, err := s.List(ctx, tenantID)
	if err != nil {
		return Voice{}, err
	}
	for _, voice := range available {
		if voice.ID == id && voice.Enabled {
			return voice, nil
		}
	}
	return Voice{}, fmt.Errorf("%w: %s", ErrUnknown, id)
}

// Sync records what the deployed workers advertise.
//
// The fleet is the authority on which voices exist: a voice in the database that no
// worker can produce would be a 500 waiting to happen, and a voice the workers gained in
// a deploy should appear without a migration.
func (s *Service) Sync(ctx context.Context) error {
	for id, modelVersion := range s.fleet.AdvertisedVoices() {
		if err := s.repo.Upsert(ctx, Voice{
			ID:           id,
			DisplayName:  displayName(id),
			ModelVersion: modelVersion,
			Enabled:      true,
		}); err != nil {
			return fmt.Errorf("upsert voice %s: %w", id, err)
		}
	}

	s.mu.Lock()
	s.fetched = time.Time{} // the catalogue changed; drop the cached listing
	s.mu.Unlock()
	return nil
}

func (s *Service) cachedPublic() ([]Voice, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.fetched.IsZero() || time.Since(s.fetched) > s.ttl {
		return nil, false
	}
	return s.cached, true
}

// displayName is a readable default until an operator sets a real one: "quangminh"
// becomes "Quangminh" rather than being left blank in a customer-facing list.
func displayName(id string) string {
	if id == "" {
		return ""
	}
	return strings.ToUpper(id[:1]) + id[1:]
}
