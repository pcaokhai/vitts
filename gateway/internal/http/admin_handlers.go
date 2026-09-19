package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"github.com/pcaokhai/vitts/gateway/internal/tenants"
)

// maxAdminBody bounds an admin request body. Public limits are larger and live with
// their endpoints (docs/14-engineering-standards.md § Security).
const maxAdminBody = 16 << 10

type createTenantRequest struct {
	Name   string `json:"name"`
	PlanID string `json:"plan_id"`
}

type createTenantResponse struct {
	Tenant tenantDTO `json:"tenant"`
	Key    keyDTO    `json:"key"`
}

type tenantDTO struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	PlanID string `json:"plan_id"`
	Status string `json:"status"`
}

type keyDTO struct {
	ID     string   `json:"id"`
	Prefix string   `json:"prefix"`
	Scopes []string `json:"scopes"`
	// Secret is returned exactly once, at creation, and is not recoverable afterwards.
	Secret string `json:"secret"`
}

// AdminRoutes mounts the operator surface. It is excluded from the public OpenAPI served
// to tenants (docs/07-permissions.md).
func AdminRoutes(guard *AdminGuard, service *tenants.Service) http.Handler {
	r := chi.NewRouter()
	r.Use(guard.Middleware)
	r.Post("/v1/tenants", createTenant(service))
	return r
}

func createTenant(service *tenants.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createTenantRequest
		if err := decodeJSON(r, &req); err != nil {
			WriteProblem(w, r, NewError(CodeInvalidRequest, "body must be a JSON object"))
			return
		}

		tenant, key, err := service.Create(r.Context(), req.Name, req.PlanID, adminActor)
		if err != nil {
			WriteProblem(w, r, mapTenantError(err))
			return
		}

		scopes := make([]string, 0, len(key.Scopes))
		for _, scope := range key.Scopes {
			scopes = append(scopes, string(scope))
		}

		writeJSON(w, r, http.StatusCreated, createTenantResponse{
			Tenant: tenantDTO{
				ID:     tenant.ID.String(),
				Name:   tenant.Name,
				PlanID: tenant.PlanID,
				Status: tenant.Status,
			},
			Key: keyDTO{
				ID:     key.ID.String(),
				Prefix: key.Prefix,
				Scopes: scopes,
				Secret: key.Secret,
			},
		})
	}
}

// adminActor is what the audit log records. There is one operator credential today
// (docs/07-permissions.md); per-operator identity arrives with a real admin console.
const adminActor = "admin"

func mapTenantError(err error) error {
	switch {
	case errors.Is(err, tenants.ErrInvalidInput):
		return NewError(CodeInvalidRequest, err.Error())
	case errors.Is(err, tenants.ErrUnknownPlan):
		return NewError(CodeInvalidRequest, "unknown plan_id")
	default:
		return &Error{Code: CodeInternal, Cause: err}
	}
}

func decodeJSON(r *http.Request, into any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxAdminBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err //nolint:wrapcheck // the caller turns any decode failure into one problem
	}
	return nil
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already on the wire, so this can only be logged.
		zerolog.Ctx(r.Context()).Error().Err(err).Msg("response body not written")
	}
}
