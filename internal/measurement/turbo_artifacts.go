package measurement

import (
	"errors"
	"sort"
	"time"
)

// SuccessfulTurboArtifactHashes selects publishable results from the current
// command's summaries using the same eligibility rules as measurement. Turbo
// owns these hashes; they are not commit-level pass attestations.
func SuccessfulTurboArtifactHashes(documents [][]byte, startedAt, finishedAt time.Time) ([]string, error) {
	if len(documents) == 0 || len(documents) > 32 {
		return nil, errors.New("bounded Turbo summaries are required")
	}
	hashes := make(map[string]struct{})
	bytes, tasks := 0, 0
	for _, document := range documents {
		bytes += len(document)
		if bytes > 64<<20 {
			return nil, errors.New("Turbo summaries exceed the publication limit")
		}
		summary, err := decodeTurboRunSummary(document)
		if err != nil {
			return nil, err
		}
		if err := summary.withinRun(startedAt, finishedAt); err != nil {
			return nil, err
		}
		tasks += len(summary.Tasks)
		if tasks > 4096 {
			return nil, errors.New("too many Turbo tasks to confirm")
		}
		for _, task := range summary.Tasks {
			if turboTaskEligible(task) {
				hashes[task.Hash] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(hashes))
	for hash := range hashes {
		result = append(result, hash)
	}
	sort.Strings(result)
	return result, nil
}
