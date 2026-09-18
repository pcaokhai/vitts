// Package migrations embeds the SQL migrations into the gateway binary so the deploy's
// migrations job (docs/13-runbook.md) runs the exact schema that ships with the image,
// with no separate tool or image to keep in step.
package migrations

import "embed"

// FS holds every migration, applied in filename order.
//
//go:embed *.sql
var FS embed.FS
