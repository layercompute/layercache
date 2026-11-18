package xcodecache

import (
	"sync"
	"sync/atomic"
)

// Stats reports protocol reuse, not estimated compilation time saved.
// Rejection counters report fixed validation stages, never cache keys, paths or
// payloads. They do not establish why a compiler chose to execute an action.
// Stages are not additive: an admission failure can also exceed its deadline.
type Stats struct {
	ActionLookupMisses     uint64 `json:"actionLookupMisses"`
	ActionValueRejects     uint64 `json:"actionValueRejects"`
	ActionReferenceRejects uint64 `json:"actionReferenceRejects"`
	GraphObjectMisses      uint64 `json:"graphObjectMisses"`
	GraphObjectCorrupt     uint64 `json:"graphObjectCorrupt"`
	GraphReferenceRejects  uint64 `json:"graphReferenceRejects"`
	GraphBudgetRejects     uint64 `json:"graphBudgetRejects"`
	GraphAdmissionRejects  uint64 `json:"graphAdmissionRejects"`
	GraphDeadlineRejects   uint64 `json:"graphDeadlineRejects"`

	// GraphRejectReasons breaks GraphReferenceRejects down by the specific rule
	// each refused node violated. GraphRejectSamples holds a bounded, structure
	// -only shape sample for the first rejection of each distinct reason. Both
	// are omitted entirely when no graph reference has ever been rejected.
	GraphRejectReasons map[string]uint64   `json:"graphRejectReasons,omitempty"`
	GraphRejectSamples []graphRejectSample `json:"graphRejectSamples,omitempty"`

	// ActionRejectReasons breaks ActionReferenceRejects down the same way, kept
	// entirely separate from GraphRejectReasons. It never carries samples: the
	// rejected action value's bytes are the lookup key's own kv.Value entries.
	ActionRejectReasons map[string]uint64 `json:"actionRejectReasons,omitempty"`

	CASReads             uint64 `json:"casReads"`
	CASHits              uint64 `json:"casHits"`
	CASWrites            uint64 `json:"casWrites"`
	ActionReads          uint64 `json:"actionReads"`
	ActionHits           uint64 `json:"actionHits"`
	ActionWrites         uint64 `json:"actionWrites"`
	CacheErrors          uint64 `json:"cacheErrors"`
	MinFreeSpaceRefusals uint64 `json:"minFreeSpaceRefusals"`
	MinFreeBytes         int64  `json:"minFreeBytes"`
	GraphChecks          uint64 `json:"graphChecks"`
	GraphObjectsChecked  uint64 `json:"graphObjectsChecked"`
	GraphLookupNanos     uint64 `json:"graphLookupNanos"`
	GraphLeaseMisses     uint64 `json:"graphLeaseMisses"`
}

const maxGraphRejectSamples = 8

type diagnosticCounters struct {
	actionLookupMisses     atomic.Uint64
	actionValueRejects     atomic.Uint64
	actionReferenceRejects atomic.Uint64
	graphObjectMisses      atomic.Uint64
	graphObjectCorrupt     atomic.Uint64
	graphReferenceRejects  atomic.Uint64
	graphBudgetRejects     atomic.Uint64
	graphAdmissionRejects  atomic.Uint64
	graphDeadlineRejects   atomic.Uint64

	rejectMu            sync.Mutex
	rejectReasons       map[graphRejectReason]uint64
	rejectSamples       []graphRejectSample
	actionRejectReasons map[graphRejectReason]uint64
}

// recordReject tallies one rejection by its specific reason and, for at most
// maxGraphRejectSamples distinct reasons, retains the first sample seen.
func (d *diagnosticCounters) recordReject(reason graphRejectReason, sample graphRejectSample) {
	d.rejectMu.Lock()
	defer d.rejectMu.Unlock()
	if d.rejectReasons == nil {
		d.rejectReasons = make(map[graphRejectReason]uint64)
	}
	d.rejectReasons[reason]++
	for _, existing := range d.rejectSamples {
		if existing.Reason == string(reason) {
			return
		}
	}
	if len(d.rejectSamples) < maxGraphRejectSamples {
		d.rejectSamples = append(d.rejectSamples, sample)
	}
}

// recordActionReject tallies one action-level rejection by its specific
// reason only. Action rejects never produce a sample.
func (d *diagnosticCounters) recordActionReject(reason graphRejectReason) {
	d.rejectMu.Lock()
	defer d.rejectMu.Unlock()
	if d.actionRejectReasons == nil {
		d.actionRejectReasons = make(map[graphRejectReason]uint64)
	}
	d.actionRejectReasons[reason]++
}

func (d *diagnosticCounters) snapshotActionRejectReasons() map[string]uint64 {
	d.rejectMu.Lock()
	defer d.rejectMu.Unlock()
	if len(d.actionRejectReasons) == 0 {
		return nil
	}
	reasons := make(map[string]uint64, len(d.actionRejectReasons))
	for reason, count := range d.actionRejectReasons {
		reasons[string(reason)] = count
	}
	return reasons
}

func (d *diagnosticCounters) snapshotRejects() (map[string]uint64, []graphRejectSample) {
	d.rejectMu.Lock()
	defer d.rejectMu.Unlock()
	var reasons map[string]uint64
	if len(d.rejectReasons) > 0 {
		reasons = make(map[string]uint64, len(d.rejectReasons))
		for reason, count := range d.rejectReasons {
			reasons[string(reason)] = count
		}
	}
	var samples []graphRejectSample
	if len(d.rejectSamples) > 0 {
		samples = append(samples, d.rejectSamples...)
	}
	return reasons, samples
}

func (s *Service) Stats() Stats {
	reasons, samples := s.diagnostics.snapshotRejects()
	return Stats{
		ActionLookupMisses:     s.diagnostics.actionLookupMisses.Load(),
		ActionValueRejects:     s.diagnostics.actionValueRejects.Load(),
		ActionReferenceRejects: s.diagnostics.actionReferenceRejects.Load(),
		GraphObjectMisses:      s.diagnostics.graphObjectMisses.Load(),
		GraphObjectCorrupt:     s.diagnostics.graphObjectCorrupt.Load(),
		GraphReferenceRejects:  s.diagnostics.graphReferenceRejects.Load(),
		GraphBudgetRejects:     s.diagnostics.graphBudgetRejects.Load(),
		GraphAdmissionRejects:  s.diagnostics.graphAdmissionRejects.Load(),
		GraphDeadlineRejects:   s.diagnostics.graphDeadlineRejects.Load(),
		GraphRejectReasons:     reasons,
		GraphRejectSamples:     samples,
		ActionRejectReasons:    s.diagnostics.snapshotActionRejectReasons(),

		CASReads: s.casReads.Load(), CASHits: s.casHits.Load(), CASWrites: s.casWrites.Load(),
		ActionReads: s.actionReads.Load(), ActionHits: s.actionHits.Load(), ActionWrites: s.actionWrites.Load(),
		CacheErrors: s.cacheErrors.Load(), GraphChecks: s.graphChecks.Load(),
		MinFreeSpaceRefusals: s.minFreeSpaceRefusals.Load(), MinFreeBytes: s.minFreeBytes,
		GraphObjectsChecked: s.graphObjectsChecked.Load(), GraphLookupNanos: s.graphLookupNanos.Load(),
		GraphLeaseMisses: s.graphLeaseMisses.Load(),
	}
}
