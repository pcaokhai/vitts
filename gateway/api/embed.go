// Package api embeds the public contract so the running binary serves exactly the
// OpenAPI document this build was generated from (ADR-009).
//
// `make generate` copies docs/api/openapi.yaml here and CI fails on any diff, so the two
// cannot drift.
package api

import _ "embed"

// OpenAPIYAML is the contract as authored.
//
//go:embed openapi.yaml
var OpenAPIYAML []byte
