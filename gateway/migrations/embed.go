// Package migrations embeds the SQL migrations into the gateway binary so the deploy's
// migrations job (docs/13-runbook.md) runs the exact schema that ships with the image,
// with no separate tool or image to keep in step.
package migrations

import (
	"embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// FS holds every migration, applied in filename order.
//
//go:embed *.sql
var FS embed.FS

// Latest is the highest version embedded in this binary, parsed from the goose filename
// convention (00002_jobs_and_usage.sql is version 2).
//
// The serving process compares this with the schema it finds, so an image deployed
// against a database the migrations job never upgraded fails at boot with the two
// versions named, rather than hours later on the first request that needs a new column
// (docs/reports/drill-m3.md, finding 3).
func Latest() (int64, error) {
	entries, err := FS.ReadDir(".")
	if err != nil {
		return 0, fmt.Errorf("read migrations: %w", err)
	}

	var latest int64
	for _, entry := range entries {
		name := entry.Name()
		digits, _, ok := strings.Cut(name, "_")
		if !ok {
			return 0, fmt.Errorf("migration %q is not named <version>_<name>.sql", name)
		}
		version, err := strconv.ParseInt(digits, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("migration %q has no numeric version: %w", name, err)
		}
		if version > latest {
			latest = version
		}
	}
	if latest == 0 {
		return 0, errors.New("no migrations are embedded in this binary")
	}
	return latest, nil
}
