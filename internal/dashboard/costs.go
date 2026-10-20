package dashboard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maximumCostLedgerBytes = 1 << 20
	maximumCostEntries     = 512
	maximumCostItems       = 64
	maximumCostStringBytes = 256
	maximumSafeAmountMinor = int64(1<<53 - 1)
)

type CostItem struct {
	Name        string `json:"name"`
	AmountMinor int64  `json:"amountMinor"`
}

// CostReport contains operator-reported costs, not a provider-verified invoice.
// A partial ledger exposes its known subtotal while keeping the total unknown.
type CostReport struct {
	State              string     `json:"state"`
	Currency           string     `json:"currency,omitempty"`
	PeriodStart        time.Time  `json:"periodStart,omitempty"`
	PeriodEnd          time.Time  `json:"periodEnd,omitempty"`
	Complete           bool       `json:"complete"`
	Items              []CostItem `json:"items,omitempty"`
	KnownSubtotalMinor int64      `json:"knownSubtotalMinor"`
	TotalMinor         *int64     `json:"totalMinor"`
}

type costLedger struct {
	Version int         `json:"version"`
	Entries []costEntry `json:"entries"`
}

type costEntry struct {
	Scope       string          `json:"scope"`
	ID          string          `json:"id"`
	Currency    string          `json:"currency"`
	PeriodStart time.Time       `json:"periodStart"`
	PeriodEnd   time.Time       `json:"periodEnd"`
	Basis       string          `json:"basis"`
	Complete    json.RawMessage `json:"complete"`
	Items       []costItemInput `json:"items"`
}

type costItemInput struct {
	Name        string `json:"name"`
	AmountMinor *int64 `json:"amountMinor"`
}

// ReadCost loads one project or Team's bounded cost record. Missing evidence is
// unknown; invalid configured evidence is unavailable. Paths and parser errors
// never enter the browser projection.
func ReadCost(path, scope, id string) CostReport {
	if path == "" {
		return CostReport{State: "unknown"}
	}
	unavailable := CostReport{State: "unavailable"}
	if !validCostScope(scope) || !validCostString(id) {
		return unavailable
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximumCostLedgerBytes {
		return unavailable
	}
	file, err := os.Open(path)
	if err != nil {
		return unavailable
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximumCostLedgerBytes {
		return unavailable
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumCostLedgerBytes+1))
	if err != nil || len(content) > maximumCostLedgerBytes || !utf8.Valid(content) {
		return unavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var ledger costLedger
	if decoder.Decode(&ledger) != nil || decoder.Decode(new(any)) != io.EOF || ledger.Version != 1 || ledger.Entries == nil || len(ledger.Entries) > maximumCostEntries {
		return unavailable
	}
	seen := make(map[string]bool, len(ledger.Entries))
	selected := CostReport{State: "unknown"}
	for _, entry := range ledger.Entries {
		if !validCostScope(entry.Scope) || !validCostString(entry.ID) || !validCostCurrency(entry.Currency) ||
			!entry.PeriodStart.After(time.Unix(0, 0)) || !entry.PeriodEnd.After(entry.PeriodStart) ||
			(entry.Basis != "recorded" && entry.Basis != "estimate") || entry.Items == nil || len(entry.Items) > maximumCostItems {
			return unavailable
		}
		complete := false
		switch string(bytes.TrimSpace(entry.Complete)) {
		case "true":
			complete = true
		case "", "false":
		default:
			return unavailable
		}
		key := entry.Scope + "\x00" + entry.ID
		if seen[key] {
			return unavailable
		}
		seen[key] = true
		var subtotal int64
		items := make([]CostItem, 0, len(entry.Items))
		for _, item := range entry.Items {
			if !validCostString(item.Name) || item.AmountMinor == nil || *item.AmountMinor < 0 || *item.AmountMinor > maximumSafeAmountMinor-subtotal {
				return unavailable
			}
			subtotal += *item.AmountMinor
			items = append(items, CostItem{Name: item.Name, AmountMinor: *item.AmountMinor})
		}
		if entry.Scope == scope && entry.ID == id {
			selected = CostReport{
				State: entry.Basis, Currency: entry.Currency,
				PeriodStart: entry.PeriodStart.UTC(), PeriodEnd: entry.PeriodEnd.UTC(),
				Complete: complete, Items: items, KnownSubtotalMinor: subtotal,
			}
			if complete {
				selected.TotalMinor = &subtotal
			}
		}
	}
	return selected
}

func validCostScope(value string) bool { return value == "project" || value == "team" }

func validCostCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for i := range value {
		if value[i] < 'A' || value[i] > 'Z' {
			return false
		}
	}
	return true
}

func validCostString(value string) bool {
	if value == "" || len(value) > maximumCostStringBytes || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
