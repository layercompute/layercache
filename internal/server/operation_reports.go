package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/measurement"
)

func (server *Server) operationReportCapability() any {
	if server.operationReports == nil || server.config.Role == "public" {
		return nil
	}
	return map[string]any{"version": measurement.OperationVersion, "path": measurement.OperationPath}
}

// Operation receipts use the existing signed native-job authority, but remain
// separate from task/compiler outcomes and optional artifact retention.
func (server *Server) recordOperationReport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if server.operationReports == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "operation reports unavailable"})
		return
	}
	defer r.Body.Close()
	report, err := measurement.DecodeOperationReport(r.Body)
	if err != nil || measurement.ValidateOperationReportShape(report) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid bounded operation report"})
		return
	}
	claims, _ := access.ClaimsFromContext(r.Context())
	authority, valid := server.nativeReportAuthority(claims)
	if !valid {
		server.writeAuthorizationFailure(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	receipt, err := server.operationReports.RecordOperation(ctx, authority, report, time.Now().UTC())
	switch {
	case errors.Is(err, measurement.ErrOperationInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid bounded operation report"})
	case errors.Is(err, measurement.ErrOperationConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "operation already has a different final report"})
	case errors.Is(err, measurement.ErrOperationQuota):
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "operation reporting capacity reached"})
	case err != nil:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "operation could not be confirmed; retry the exact input"})
	default:
		writeJSON(w, http.StatusOK, receipt)
	}
}
