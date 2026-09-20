package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/pcaokhai/vitts/gateway/internal/auth"
)

// maxKeyBody bounds a key request; they are tiny.
const maxKeyBody = 8 << 10

// KeyManager is the use case port.
type KeyManager interface {
	Create(ctx context.Context, caller auth.Identity, name string, scopes []auth.Scope) (auth.Created, error)
	List(ctx context.Context, tenantID uuid.UUID, includeRevoked bool) ([]auth.KeyRecord, error)
	Revoke(ctx context.Context, caller auth.Identity, keyID uuid.UUID) error
}

type createKeyRequest struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

type keyRecordDTO struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	// Secret is present only in a creation response, and never again.
	Secret string `json:"secret,omitempty"`
}

// CreateKey handles POST /v1/keys (US-17).
func CreateKey(keys KeyManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := IdentityFrom(r.Context())
		if !ok {
			WriteProblem(w, r, NewError(CodeUnauthorized, "missing or malformed API key"))
			return
		}

		var body createKeyRequest
		decoder := json.NewDecoder(io.LimitReader(r.Body, maxKeyBody))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			WriteProblem(w, r, NewError(CodeInvalidRequest, "body must be {name, scopes}"))
			return
		}

		scopes := make([]auth.Scope, 0, len(body.Scopes))
		for _, scope := range body.Scopes {
			scopes = append(scopes, auth.Scope(scope))
		}

		created, err := keys.Create(r.Context(), identity, body.Name, scopes)
		if err != nil {
			WriteProblem(w, r, mapKeyError(err))
			return
		}

		dto := toKeyDTO(created.Record)
		dto.Secret = created.Secret
		writeJSON(w, r, http.StatusCreated, dto)
	}
}

// ListKeys handles GET /v1/keys.
func ListKeys(keys KeyManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := IdentityFrom(r.Context())
		if !ok {
			WriteProblem(w, r, NewError(CodeUnauthorized, "missing or malformed API key"))
			return
		}

		includeRevoked := r.URL.Query().Get("include_revoked") == "true"

		records, err := keys.List(r.Context(), identity.TenantID, includeRevoked)
		if err != nil {
			WriteProblem(w, r, mapKeyError(err))
			return
		}

		out := make([]keyRecordDTO, 0, len(records))
		for _, record := range records {
			out = append(out, toKeyDTO(record))
		}
		writeJSON(w, r, http.StatusOK, out)
	}
}

// RevokeKey handles DELETE /v1/keys/{keyId} (US-17, T-18).
func RevokeKey(keys KeyManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := IdentityFrom(r.Context())
		if !ok {
			WriteProblem(w, r, NewError(CodeUnauthorized, "missing or malformed API key"))
			return
		}

		keyID, err := uuid.Parse(chi.URLParam(r, "keyId"))
		if err != nil {
			WriteProblem(w, r, NewError(CodeJobNotFound, "no such key"))
			return
		}

		if err := keys.Revoke(r.Context(), identity, keyID); err != nil {
			WriteProblem(w, r, mapKeyError(err))
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func toKeyDTO(record auth.KeyRecord) keyRecordDTO {
	scopes := make([]string, 0, len(record.Scopes))
	for _, scope := range record.Scopes {
		scopes = append(scopes, string(scope))
	}

	return keyRecordDTO{
		ID: record.ID.String(), Name: record.Name, Prefix: record.Prefix,
		Scopes: scopes, CreatedAt: record.CreatedAt,
		LastUsedAt: record.LastUsedAt, RevokedAt: record.RevokedAt,
	}
}

func mapKeyError(err error) error {
	switch {
	case errors.Is(err, auth.ErrKeyNotFound):
		// The same answer as a key that belongs to another tenant: which ids exist is
		// not something we disclose (docs/07-permissions.md).
		return NewError(CodeJobNotFound, "no such key")
	case errors.Is(err, auth.ErrInvalidKeyRequest):
		return NewError(CodeInvalidRequest, err.Error())
	default:
		return &Error{Code: CodeInternal, Cause: err}
	}
}
