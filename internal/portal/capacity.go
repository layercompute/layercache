package portal

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/layercache/layercache/internal/cloud"
	"github.com/layercache/layercache/internal/server"
)

func capacitySubject(session Session, project Project) string {
	if project.OperatorManaged {
		return "github:" + strings.ToLower(session.User.Login)
	}
	return "github-id:" + session.User.ID
}

func (p *Portal) projectCapacity(w http.ResponseWriter, r *http.Request, session Session) {
	project, err := p.dashboardProject(r, session)
	if err != nil {
		fail(w, err)
		return
	}
	if err := p.ensureProject(r.Context(), *project); err != nil {
		jsonResponse(w, 503, map[string]string{"error": "project capacity is unavailable"})
		return
	}
	result, err := p.gateway.Capacity(r.Context(), project.ID, capacitySubject(session, *project))
	if err != nil {
		capacityFailure(w, err)
		return
	}
	// A portal role is an additional bound on existing native authority.
	result.CanChange = result.CanChange && project.Role == "admin"
	if !project.OperatorManaged && project.StoragePoolID == legacyStorageID {
		result.LimitSource = "legacy"
	}
	jsonResponse(w, 200, result)
}

func (p *Portal) changeProjectCapacity(w http.ResponseWriter, r *http.Request, session Session) {
	project, err := p.dashboardProject(r, session)
	if err != nil {
		fail(w, err)
		return
	}
	if project.Role != "admin" {
		fail(w, ErrDenied)
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		fail(w, ErrInvalid)
		return
	}
	defer r.Body.Close()
	var input struct {
		MaxBytes         *int64 `json:"maxBytes"`
		ExpectedMaxBytes *int64 `json:"expectedMaxBytes"`
		Confirm          bool   `json:"confirm"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || input.MaxBytes == nil || input.ExpectedMaxBytes == nil || *input.MaxBytes <= 0 || *input.MaxBytes > 1<<53-1 || *input.ExpectedMaxBytes <= 0 || *input.ExpectedMaxBytes > 1<<53-1 || !input.Confirm {
		fail(w, ErrInvalid)
		return
	}
	if err := p.ensureProject(r.Context(), *project); err != nil {
		jsonResponse(w, 503, map[string]string{"error": "project capacity is unavailable"})
		return
	}
	result, err := p.gateway.SetCapacity(r.Context(), project.ID, capacitySubject(session, *project), *input.ExpectedMaxBytes, *input.MaxBytes)
	if err != nil {
		capacityFailure(w, err)
		return
	}
	if !project.OperatorManaged && project.StoragePoolID == legacyStorageID {
		result.LimitSource = "legacy"
	}
	jsonResponse(w, 200, result)
}

func capacityFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, server.ErrCapacityDenied):
		fail(w, ErrDenied)
	case errors.Is(err, cloud.ErrConflict):
		jsonResponse(w, 409, map[string]string{"error": "Capacity changed in another session. Refresh before saving again."})
	case errors.Is(err, cloud.ErrQuota):
		jsonResponse(w, 409, map[string]string{"error": "The limit cannot hold current cached bytes and retained obligations. Prune eligible cache entries separately, then refresh. No entries were removed."})
	case errors.Is(err, cloud.ErrCapacityBounds):
		jsonResponse(w, 400, map[string]string{"error": "Capacity must stay within the assigned allocation and above the retention target."})
	default:
		jsonResponse(w, 503, map[string]string{"error": "Capacity change could not be confirmed. Refresh capacity before retrying."})
	}
}
