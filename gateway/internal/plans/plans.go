// Package plans loads plan tiers from the operator-editable config file.
//
// Plan rows are configuration, not tenant data, and the figures are an open pricing
// decision: they live in config/plans.yaml so a change needs no migration and nothing
// invented ships inside one (docs/plans/1.2.md).
package plans

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Plan is one tier.
type Plan struct {
	ID                   string   `yaml:"id"`
	CharsPerMonth        int64    `yaml:"chars_per_month"`
	ReqPerMinute         int32    `yaml:"req_per_minute"`
	MaxConcurrentStreams int32    `yaml:"max_concurrent_streams"`
	MaxJobChars          int32    `yaml:"max_job_chars"`
	PriceUSDCents        int32    `yaml:"price_usd_cents"`
	OverageUSDPerMillion *float64 `yaml:"overage_usd_per_million"`
}

type file struct {
	Plans []Plan `yaml:"plans"`
}

// Load reads and validates the plan file.
//
// Validation is strict because these numbers are limits: a zero rate limit would let one
// tenant take the fleet, and a typo should stop a deploy rather than surface as an outage.
func Load(path string) ([]Plan, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // an operator-supplied config path
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var parsed file
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true) // a misspelled field is a mistake, not a default
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(parsed.Plans) == 0 {
		return nil, fmt.Errorf("%s defines no plans", path)
	}

	seen := make(map[string]struct{}, len(parsed.Plans))
	for _, plan := range parsed.Plans {
		if err := plan.validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if _, duplicate := seen[plan.ID]; duplicate {
			return nil, fmt.Errorf("%s: duplicate plan id %q", path, plan.ID)
		}
		seen[plan.ID] = struct{}{}
	}

	return parsed.Plans, nil
}

func (p Plan) validate() error {
	switch {
	case p.ID == "":
		return fmt.Errorf("plan id must not be empty")
	case p.CharsPerMonth < 0:
		return fmt.Errorf("plan %q: chars_per_month must not be negative", p.ID)
	case p.ReqPerMinute <= 0:
		return fmt.Errorf("plan %q: req_per_minute must be positive", p.ID)
	case p.MaxConcurrentStreams <= 0:
		return fmt.Errorf("plan %q: max_concurrent_streams must be positive", p.ID)
	case p.MaxJobChars <= 0:
		return fmt.Errorf("plan %q: max_job_chars must be positive", p.ID)
	case p.PriceUSDCents < 0:
		return fmt.Errorf("plan %q: price_usd_cents must not be negative", p.ID)
	case p.OverageUSDPerMillion != nil && *p.OverageUSDPerMillion < 0:
		return fmt.Errorf("plan %q: overage_usd_per_million must not be negative", p.ID)
	}
	return nil
}
