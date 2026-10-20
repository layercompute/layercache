// Package dashboard serves the CLI-connected dashboard. The browser receives
// projections of cache data, never runtime or Team Cache credentials.
package dashboard

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"time"

	"github.com/layercache/layercache/internal/measurement"
)

//go:embed assets/*
var assets embed.FS

type Status struct {
	ProjectID      string `json:"projectId"`
	UsageBytes     int64  `json:"usageBytes"`
	MaxBytes       *int64 `json:"maxBytes"`
	Artifacts      int64  `json:"artifacts"`
	EvictionPolicy string `json:"evictionPolicy"`
	Role           string `json:"role"`
	Visibility     string `json:"visibility"`
	StorageBackend string `json:"storageBackend"`
	Entries        int64  `json:"entries"`
	MetadataBytes  int64  `json:"metadataBytes"`
	CanPrune       bool   `json:"canPrune"`
}

type Cache struct {
	State        string                    `json:"state"`
	PruneAllowed bool                      `json:"pruneAllowed"`
	Status       *Status                   `json:"status,omitempty"`
	Report       *measurement.PeriodReport `json:"report,omitempty"`
	ReportState  string                    `json:"reportState"`
}

type Project struct {
	ID    string     `json:"id"`
	Local Cache      `json:"local"`
	Team  Cache      `json:"team"`
	Cost  CostReport `json:"cost"`
}

type Snapshot struct {
	Projects  []Project `json:"projects"`
	UpdatedAt time.Time `json:"updatedAt"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
}

type PruneRequest struct {
	ProjectID   string `json:"projectId"`
	Cache       string `json:"cache"`
	TargetBytes int64  `json:"targetBytes"`
	Confirm     bool   `json:"confirm"`
}

type PruneResult struct {
	TargetBytes   int64 `json:"targetBytes"`
	BeforeBytes   int64 `json:"beforeBytes"`
	AfterBytes    int64 `json:"afterBytes"`
	FreedBytes    int64 `json:"freedBytes"`
	BeforeEntries int64 `json:"beforeEntries"`
	AfterEntries  int64 `json:"afterEntries"`
}

type Options struct {
	Snapshot func(context.Context, time.Duration) Snapshot
	Prune    func(context.Context, PruneRequest) (PruneResult, error)
}

var (
	ErrPruneForbidden   = errors.New("cache maintenance is not authorized")
	ErrPruneInvalid     = errors.New("cache maintenance request is invalid")
	ErrPruneUnavailable = errors.New("cache maintenance is unavailable; refresh before trying again")
)

// New serves only fixed read routes. A fresh process token authenticates browser
// requests; the exact authority check also rejects DNS rebinding.
func New(authority, token string, snapshot func(context.Context, time.Duration) Snapshot) http.Handler {
	return NewWithOptions(authority, token, Options{Snapshot: snapshot})
}

// NewWithOptions enables maintenance only when the owner supplies a callback.
// Possession of a read-only dashboard link never changes that policy.
func NewWithOptions(authority, token string, options Options) http.Handler {
	files, _ := fs.Sub(assets, "assets")
	static := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if token == "" || r.Host != authority {
			http.Error(w, "invalid dashboard host", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/api/prune" && r.Method == http.MethodPost && options.Prune != nil {
			if !authorized(r, token) {
				http.Error(w, "open the dashboard link printed by the CLI", http.StatusUnauthorized)
				return
			}
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != "http://"+authority ||
				r.Header.Get("Sec-Fetch-Site") != "" && r.Header.Get("Sec-Fetch-Site") != "same-origin" {
				http.Error(w, "same-origin request required", http.StatusForbidden)
				return
			}
			if err != nil || mediaType != "application/json" {
				http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
				return
			}
			var body struct {
				ProjectID   string `json:"projectId"`
				Cache       string `json:"cache"`
				TargetBytes *int64 `json:"targetBytes"`
				Confirm     bool   `json:"confirm"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&body) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
				body.TargetBytes == nil || *body.TargetBytes < 0 || !body.Confirm || body.ProjectID == "" || len(body.ProjectID) > 512 ||
				body.Cache != "local" && body.Cache != "team" {
				http.Error(w, "invalid confirmed cache maintenance request", http.StatusBadRequest)
				return
			}
			result, err := options.Prune(r.Context(), PruneRequest{ProjectID: body.ProjectID, Cache: body.Cache, TargetBytes: *body.TargetBytes, Confirm: body.Confirm})
			if err != nil {
				switch {
				case errors.Is(err, ErrPruneForbidden):
					http.Error(w, ErrPruneForbidden.Error(), http.StatusForbidden)
				case errors.Is(err, ErrPruneInvalid):
					http.Error(w, ErrPruneInvalid.Error(), http.StatusBadRequest)
				default:
					http.Error(w, ErrPruneUnavailable.Error(), http.StatusBadGateway)
				}
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(result)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "read-only dashboard", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/api/snapshot" {
			if !authorized(r, token) {
				http.Error(w, "open the dashboard link printed by the CLI", http.StatusUnauthorized)
				return
			}
			period := 24 * time.Hour
			switch r.URL.Query().Get("period") {
			case "", "24h":
			case "7d":
				period = 7 * 24 * time.Hour
			default:
				http.Error(w, "invalid period", http.StatusBadRequest)
				return
			}
			if options.Snapshot == nil {
				http.Error(w, "dashboard snapshot unavailable", http.StatusServiceUnavailable)
				return
			}
			result := options.Snapshot(r.Context(), period)
			if result.Projects == nil {
				http.Error(w, "dashboard snapshot timed out; refresh to try again", http.StatusGatewayTimeout)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(result)
			return
		}
		switch r.URL.Path {
		case "/", "/app.js", "/style.css":
			static.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func authorized(r *http.Request, token string) bool {
	return len(r.Header.Values("Authorization")) == 1 && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) == 1
}
