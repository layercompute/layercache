package xcodecache

import "sync/atomic"

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

	CASReads            uint64 `json:"casReads"`
	CASHits             uint64 `json:"casHits"`
	CASWrites           uint64 `json:"casWrites"`
	ActionReads         uint64 `json:"actionReads"`
	ActionHits          uint64 `json:"actionHits"`
	ActionWrites        uint64 `json:"actionWrites"`
	CacheErrors         uint64 `json:"cacheErrors"`
	GraphChecks         uint64 `json:"graphChecks"`
	GraphObjectsChecked uint64 `json:"graphObjectsChecked"`
	GraphLookupNanos    uint64 `json:"graphLookupNanos"`
	GraphLeaseMisses    uint64 `json:"graphLeaseMisses"`
}

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
}

func (s *Service) Stats() Stats {
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

		CASReads: s.casReads.Load(), CASHits: s.casHits.Load(), CASWrites: s.casWrites.Load(),
		ActionReads: s.actionReads.Load(), ActionHits: s.actionHits.Load(), ActionWrites: s.actionWrites.Load(),
		CacheErrors: s.cacheErrors.Load(), GraphChecks: s.graphChecks.Load(),
		GraphObjectsChecked: s.graphObjectsChecked.Load(), GraphLookupNanos: s.graphLookupNanos.Load(),
		GraphLeaseMisses: s.graphLeaseMisses.Load(),
	}
}
