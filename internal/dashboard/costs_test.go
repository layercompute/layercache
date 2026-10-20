package dashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const costRecordFixture = `{"version":1,"entries":[{"scope":"project","id":"web-app","currency":"USD","periodStart":"2026-10-01T00:00:00Z","periodEnd":"2026-11-01T00:00:00Z","basis":"recorded","complete":true,"items":[{"name":"Hosted storage","amountMinor":125},{"name":"Compute","amountMinor":375}]}]}`

func writeCostFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "costs.json")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func changeCostFixture(t *testing.T, change func(map[string]any, map[string]any)) string {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(costRecordFixture), &document); err != nil {
		t.Fatal(err)
	}
	entry := document["entries"].([]any)[0].(map[string]any)
	change(document, entry)
	content, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestReadCostReportsOnlyExplicitTotals(t *testing.T) {
	path := writeCostFixture(t, costRecordFixture)
	got := ReadCost(path, "project", "web-app")
	if got.State != "recorded" || got.Currency != "USD" || !got.Complete || got.KnownSubtotalMinor != 500 || got.TotalMinor == nil || *got.TotalMinor != 500 || len(got.Items) != 2 {
		t.Fatalf("recorded report = %+v", got)
	}
	if !got.PeriodStart.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) || !got.PeriodEnd.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("period = %v to %v", got.PeriodStart, got.PeriodEnd)
	}
	for _, complete := range []string{"omitted", "false"} {
		t.Run(complete, func(t *testing.T) {
			content := changeCostFixture(t, func(_ map[string]any, entry map[string]any) {
				entry["basis"] = "estimate"
				delete(entry, "complete")
				if complete == "false" {
					entry["complete"] = false
				}
			})
			got := ReadCost(writeCostFixture(t, content), "project", "web-app")
			if got.State != "estimate" || got.Complete || got.TotalMinor != nil || got.KnownSubtotalMinor != 500 {
				t.Fatalf("partial estimate = %+v", got)
			}
		})
	}
	content := changeCostFixture(t, func(_ map[string]any, entry map[string]any) { entry["items"] = []any{} })
	zero := ReadCost(writeCostFixture(t, content), "project", "web-app")
	if zero.State != "recorded" || zero.TotalMinor == nil || *zero.TotalMinor != 0 {
		t.Fatalf("explicit complete zero = %+v", zero)
	}
}

func TestReadCostUnknownAndUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name, path, scope, id, state string
	}{
		{"not configured", "", "project", "web-app", "unknown"},
		{"missing record", writeCostFixture(t, costRecordFixture), "project", "other", "unknown"},
		{"scope isolated", writeCostFixture(t, costRecordFixture), "team", "web-app", "unknown"},
		{"empty ledger", writeCostFixture(t, `{"version":1,"entries":[]}`), "project", "web-app", "unknown"},
		{"missing file", filepath.Join(t.TempDir(), "operator-private-name"), "project", "web-app", "unavailable"},
		{"directory", t.TempDir(), "project", "web-app", "unavailable"},
		{"invalid selector scope", writeCostFixture(t, costRecordFixture), "public", "web-app", "unavailable"},
		{"invalid selector id", writeCostFixture(t, costRecordFixture), "project", "", "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ReadCost(tc.path, tc.scope, tc.id)
			if got.State != tc.state || got.TotalMinor != nil || got.Complete || got.KnownSubtotalMinor != 0 || got.Currency != "" || len(got.Items) != 0 {
				t.Fatalf("missing evidence report = %+v", got)
			}
			encoded, err := json.Marshal(got)
			if err != nil || tc.path != "" && strings.Contains(string(encoded), tc.path) {
				t.Fatalf("projection contains a path or cannot marshal: %s, %v", encoded, err)
			}
		})
	}
}

func TestReadCostRejectsInvalidWholeLedger(t *testing.T) {
	changes := map[string]func(map[string]any, map[string]any){
		"unsupported version":    func(doc, _ map[string]any) { doc["version"] = 2 },
		"unknown document field": func(doc, _ map[string]any) { doc["secret"] = "private-value" },
		"unknown entry field":    func(_ map[string]any, entry map[string]any) { entry["secret"] = "private-value" },
		"missing entries":        func(doc, _ map[string]any) { delete(doc, "entries") },
		"null entries":           func(doc, _ map[string]any) { doc["entries"] = nil },
		"duplicate scope and id": func(doc, entry map[string]any) { doc["entries"] = []any{entry, entry} },
		"invalid scope":          func(_ map[string]any, entry map[string]any) { entry["scope"] = "public" },
		"empty id":               func(_ map[string]any, entry map[string]any) { entry["id"] = "" },
		"long id": func(_ map[string]any, entry map[string]any) {
			entry["id"] = strings.Repeat("x", maximumCostStringBytes+1)
		},
		"untrimmed id":         func(_ map[string]any, entry map[string]any) { entry["id"] = "web-app " },
		"control id":           func(_ map[string]any, entry map[string]any) { entry["id"] = "web\napp" },
		"lowercase currency":   func(_ map[string]any, entry map[string]any) { entry["currency"] = "usd" },
		"numeric currency":     func(_ map[string]any, entry map[string]any) { entry["currency"] = "123" },
		"long currency":        func(_ map[string]any, entry map[string]any) { entry["currency"] = "USDD" },
		"invalid basis":        func(_ map[string]any, entry map[string]any) { entry["basis"] = "invoice-verified" },
		"null completeness":    func(_ map[string]any, entry map[string]any) { entry["complete"] = nil },
		"string completeness":  func(_ map[string]any, entry map[string]any) { entry["complete"] = "true" },
		"numeric completeness": func(_ map[string]any, entry map[string]any) { entry["complete"] = 1 },
		"missing period":       func(_ map[string]any, entry map[string]any) { delete(entry, "periodStart") },
		"nonpositive period":   func(_ map[string]any, entry map[string]any) { entry["periodStart"] = "1970-01-01T00:00:00Z" },
		"equal period":         func(_ map[string]any, entry map[string]any) { entry["periodEnd"] = entry["periodStart"] },
		"reversed period":      func(_ map[string]any, entry map[string]any) { entry["periodEnd"] = "2026-09-30T00:00:00Z" },
		"missing items":        func(_ map[string]any, entry map[string]any) { delete(entry, "items") },
		"null items":           func(_ map[string]any, entry map[string]any) { entry["items"] = nil },
		"empty item name": func(_ map[string]any, entry map[string]any) {
			entry["items"] = []any{map[string]any{"name": "", "amountMinor": 1}}
		},
		"long item name": func(_ map[string]any, entry map[string]any) {
			entry["items"] = []any{map[string]any{"name": strings.Repeat("x", maximumCostStringBytes+1), "amountMinor": 1}}
		},
		"unknown item field": func(_ map[string]any, entry map[string]any) {
			entry["items"] = []any{map[string]any{"name": "Storage", "amountMinor": 1, "secret": "private-value"}}
		},
		"missing amount": func(_ map[string]any, entry map[string]any) {
			entry["items"] = []any{map[string]any{"name": "Storage"}}
		},
		"null amount": func(_ map[string]any, entry map[string]any) {
			entry["items"] = []any{map[string]any{"name": "Storage", "amountMinor": nil}}
		},
		"negative amount": func(_ map[string]any, entry map[string]any) {
			entry["items"] = []any{map[string]any{"name": "Storage", "amountMinor": -1}}
		},
		"unsafe amount": func(_ map[string]any, entry map[string]any) {
			entry["items"] = []any{map[string]any{"name": "Storage", "amountMinor": maximumSafeAmountMinor + 1}}
		},
		"unsafe subtotal": func(_ map[string]any, entry map[string]any) {
			entry["items"] = []any{map[string]any{"name": "Storage", "amountMinor": maximumSafeAmountMinor}, map[string]any{"name": "Compute", "amountMinor": 1}}
		},
		"int64 overflow": func(_ map[string]any, entry map[string]any) {
			entry["items"] = []any{map[string]any{"name": "Storage", "amountMinor": json.Number("9223372036854775808")}}
		},
		"fractional amount": func(_ map[string]any, entry map[string]any) {
			entry["items"] = []any{map[string]any{"name": "Storage", "amountMinor": 1.5}}
		},
		"invalid unselected entry": func(doc, entry map[string]any) {
			doc["entries"] = []any{entry, map[string]any{"scope": "team", "id": "other"}}
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			got := ReadCost(writeCostFixture(t, changeCostFixture(t, change)), "project", "web-app")
			if got.State != "unavailable" || got.TotalMinor != nil || got.KnownSubtotalMinor != 0 || len(got.Items) != 0 {
				t.Fatalf("invalid ledger accepted: %+v", got)
			}
		})
	}
	for name, content := range map[string]string{
		"trailing record": costRecordFixture + `{}`,
		"trailing junk":   costRecordFixture + `garbage`,
		"truncated":       costRecordFixture[:len(costRecordFixture)-1],
		"invalid UTF-8":   strings.Replace(costRecordFixture, "Hosted storage", "Hosted\xffstorage", 1),
		"empty":           "",
		"null document":   "null",
	} {
		t.Run(name, func(t *testing.T) {
			if got := ReadCost(writeCostFixture(t, content), "project", "web-app"); got.State != "unavailable" {
				t.Fatalf("invalid JSON accepted: %+v", got)
			}
		})
	}
}

func TestReadCostBoundsAndScope(t *testing.T) {
	for _, over := range []bool{false, true} {
		addition := 0
		if over {
			addition = 1
		}
		want := "recorded"
		if over {
			want = "unavailable"
		}
		t.Run("file bytes "+want, func(t *testing.T) {
			content := costRecordFixture + strings.Repeat(" ", maximumCostLedgerBytes-len(costRecordFixture)+addition)
			if got := ReadCost(writeCostFixture(t, content), "project", "web-app"); got.State != want {
				t.Fatalf("file bound state = %s", got.State)
			}
		})
		t.Run("item count "+want, func(t *testing.T) {
			content := changeCostFixture(t, func(_ map[string]any, entry map[string]any) {
				items := make([]any, maximumCostItems+addition)
				for i := range items {
					items[i] = map[string]any{"name": strings.Repeat("x", maximumCostStringBytes), "amountMinor": 1}
				}
				entry["items"] = items
			})
			if got := ReadCost(writeCostFixture(t, content), "project", "web-app"); got.State != want {
				t.Fatalf("item bound state = %s", got.State)
			}
		})
		t.Run("entry count "+want, func(t *testing.T) {
			content := changeCostFixture(t, func(doc, entry map[string]any) {
				entries := make([]any, maximumCostEntries+addition)
				entries[0] = entry
				for i := 1; i < len(entries); i++ {
					copy := make(map[string]any, len(entry))
					for key, value := range entry {
						copy[key] = value
					}
					copy["id"] = "project-" + strconv.Itoa(i)
					entries[i] = copy
				}
				doc["entries"] = entries
			})
			if got := ReadCost(writeCostFixture(t, content), "project", "web-app"); got.State != want {
				t.Fatalf("entry bound state = %s", got.State)
			}
		})
	}
	content := changeCostFixture(t, func(doc, entry map[string]any) {
		entry["items"] = []any{map[string]any{"name": "Maximum exact cost", "amountMinor": maximumSafeAmountMinor}}
		entry["periodStart"] = "2026-10-01T02:00:00+02:00"
		team := make(map[string]any, len(entry))
		for key, value := range entry {
			team[key] = value
		}
		team["scope"], team["currency"] = "team", "EUR"
		doc["entries"] = []any{entry, team}
	})
	path := writeCostFixture(t, content)
	for scope, currency := range map[string]string{"project": "USD", "team": "EUR"} {
		got := ReadCost(path, scope, "web-app")
		if got.State != "recorded" || got.Currency != currency || got.TotalMinor == nil || *got.TotalMinor != maximumSafeAmountMinor || got.PeriodStart.Location() != time.UTC || got.PeriodStart.Hour() != 0 {
			t.Fatalf("scoped exact boundary report = %+v", got)
		}
	}
}
