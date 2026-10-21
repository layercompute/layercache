package server

import (
	"context"
	"net/http"
	"time"
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
			// Native reporting is an independent observation plane. Its failure must
			// not hide completed generic task graphs or turn missing native data into
			// zero outcomes. The existing graph estimate retains its generic scope.
			report.NativeReportsUnavailable = true
		} else {
			report.NativeReports = native
		}
	}
	if server.operationReports != nil {
		ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
		operations, operationErr := server.operationReports.OperationPeriod(ctx, from, to)
		cancel()
		if operationErr != nil {
			report.OperationReportsUnavailable = true
		} else {
			report.OperationReports = &operations
		}
	} else if server.config.Role != "public" {
		report.OperationReportsUnavailable = true
	}
	writeJSON(writer, http.StatusOK, report)
}
