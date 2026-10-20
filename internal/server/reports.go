package server

import (
	"context"
	"net/http"
	"time"

	"github.com/layercache/layercache/internal/measurement"
)

func (server *Server) periodReport(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	fromValues, toValues := query["from"], query["to"]
	if len(fromValues) != 1 || len(toValues) != 1 {
		writeJSON(writer, http.StatusBadRequest, map[string]string{
			"error": "from and to must each be one RFC3339 timestamp",
		})
		return
	}
	from, fromErr := time.Parse(time.RFC3339, fromValues[0])
	to, toErr := time.Parse(time.RFC3339, toValues[0])
	if fromErr != nil || toErr != nil || !to.After(from) {
		writeJSON(writer, http.StatusBadRequest, map[string]string{
			"error": "from and to must be ordered RFC3339 timestamps",
		})
		return
	}
	report, err := server.measurements.PeriodReport(from, to)
	if err != nil {
		if request.Context().Err() != nil {
			return
		}
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "read period report"})
		return
	}
	if server.nativeReports != nil {
		ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
		native, nativeErr := server.nativeReports.PeriodReport(ctx, from, to)
		cancel()
		if nativeErr != nil {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "read native period report"})
			return
		}
		report.NativeReports = native
		if len(native) > 0 {
			// Native reports currently establish task outcomes, not a complete
			// combined native/Turbo dependency graph or its critical path.
			report.NetEstimatedBuildTimeSaved.Milliseconds = nil
			report.NetEstimatedBuildTimeSaved.Confidence = measurement.ConfidenceUnknown
		}
	}
	writeJSON(writer, http.StatusOK, report)
}
