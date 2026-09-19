package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"gopkg.in/yaml.v3"

	"github.com/pcaokhai/vitts/gateway/api"
)

// openAPIJSON is the contract converted once at boot. Converting per request would burn
// CPU on a document that cannot change while the process runs.
var openAPIJSON = mustConvertOpenAPI()

// OpenAPI serves the contract as JSON, which is what most tooling expects.
func OpenAPI() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(openAPIJSON)
	}
}

// OpenAPIYAML serves the contract as authored.
func OpenAPIYAML() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(api.OpenAPIYAML)
	}
}

// mustConvertOpenAPI panics on a malformed contract, which is a build-time mistake: a
// binary that cannot describe its own API should not start serving.
func mustConvertOpenAPI() []byte {
	var document any
	if err := yaml.Unmarshal(api.OpenAPIYAML, &document); err != nil {
		panic("embedded openapi.yaml is not valid YAML: " + err.Error())
	}

	encoded, err := jsonMarshal(document)
	if err != nil {
		panic("embedded openapi.yaml cannot be rendered as JSON: " + err.Error())
	}
	return encoded
}

// jsonMarshal is encoding/json with HTML escaping off, so a `type` URI in the contract is
// served as written rather than with escaped ampersands.
func jsonMarshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(v); err != nil {
		return nil, fmt.Errorf("encode openapi: %w", err)
	}
	return buf.Bytes(), nil
}
