package http

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/pcaokhai/vitts/gateway/internal/voices"
)

// VoiceCatalogue is the use case port.
type VoiceCatalogue interface {
	List(ctx context.Context, tenantID *uuid.UUID) ([]voices.Voice, error)
}

type voiceDTO struct {
	ID           string   `json:"id"`
	DisplayName  string   `json:"display_name"`
	Gender       string   `json:"gender,omitempty"`
	Tags         []string `json:"tags"`
	ModelVersion string   `json:"model_version"`
	PreviewURL   string   `json:"preview_url,omitempty"`
}

// ListVoices handles GET /v1/voices (US-13).
//
// The endpoint is readable without a key, and a key narrows the listing to that tenant's
// own voices plus the public presets — never another tenant's.
func ListVoices(catalogue VoiceCatalogue) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var tenantID *uuid.UUID
		if identity, ok := IdentityFrom(r.Context()); ok {
			tenantID = &identity.TenantID
		}

		found, err := catalogue.List(r.Context(), tenantID)
		if err != nil {
			WriteProblem(w, r, &Error{Code: CodeInternal, Cause: err})
			return
		}

		out := make([]voiceDTO, 0, len(found))
		for _, voice := range found {
			tags := voice.Tags
			if tags == nil {
				tags = []string{}
			}
			out = append(out, voiceDTO{
				ID: voice.ID, DisplayName: voice.DisplayName, Gender: voice.Gender,
				Tags: tags, ModelVersion: voice.ModelVersion, PreviewURL: voice.PreviewURL,
			})
		}

		writeJSON(w, r, http.StatusOK, out)
	}
}
