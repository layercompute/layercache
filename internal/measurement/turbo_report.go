package measurement

import (
	"encoding/json"
	"errors"
	"fmt"
)

const maximumTurboReportBytes = 8 << 20

// TurboReportPayload projects native summaries into the report protocol's
// allowlist. Complete input maps, environment metadata and commands stay on the
// machine. The original documents remain available for native publication.
func TurboReportPayload(documents [][]byte) ([]byte, error) {
	if len(documents) == 0 || len(documents) > maximumTurboSummaries {
		return nil, errors.New("bounded Turbo report summaries are required")
	}
	rawBytes := 0
	for _, document := range documents {
		if len(document) > maximumTurboSummaryBytes-rawBytes {
			return nil, errors.New("raw Turbo report summaries exceed 64 MiB")
		}
		rawBytes += len(document)
	}
	input := struct {
		Summaries []turboRunSummary `json:"summaries"`
	}{Summaries: make([]turboRunSummary, 0, len(documents))}
	seen := make(map[string]bool, len(documents))
	tasks := 0
	for index, document := range documents {
		summary, err := decodeTurboRunSummary(document)
		if err != nil {
			return nil, fmt.Errorf("Turbo report summary %d is invalid", index+1)
		}
		if seen[summary.ID] {
			return nil, errors.New("Turbo report summary IDs must be unique")
		}
		seen[summary.ID] = true
		tasks += len(summary.Tasks)
		if tasks > maximumTurboSummaryTasks {
			return nil, errors.New("too many Turbo report tasks")
		}
		for _, task := range summary.Tasks {
			// Match the report endpoint's five-second clock tolerance. Zero
			// timestamps describe an unexecuted task and remain unexecuted.
			if task.Execution.StartTime == 0 && task.Execution.EndTime == 0 {
				continue
			}
			startsTooEarly := task.Execution.StartTime < summary.Execution.StartTime && summary.Execution.StartTime-task.Execution.StartTime > 5000
			finishesTooLate := task.Execution.EndTime > summary.Execution.EndTime && task.Execution.EndTime-summary.Execution.EndTime > 5000
			if startsTooEarly || finishesTooLate {
				return nil, errors.New("Turbo report task falls outside its summary window")
			}
		}
		input.Summaries = append(input.Summaries, summary)
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, errors.New("Turbo report projection could not be encoded")
	}
	// Count the exact encoded envelope, commas and escaped strings sent to the
	// existing report endpoint, independently of the larger native input limit.
	if len(body) > maximumTurboReportBytes {
		return nil, errors.New("hosted Turbo report request exceeds 8 MiB")
	}
	return body, nil
}
