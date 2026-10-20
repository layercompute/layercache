package artifact

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// ReadStats reads persisted Local Cache metadata without initializing or
// maintaining the cache. It is safe to use while the Local Cache runtime is
// stopped.
func ReadStats(ctx context.Context, root string) (Stats, error) {
	metadataPath := filepath.Join(root, "metadata.db")
	if _, err := os.Stat(metadataPath); errors.Is(err, os.ErrNotExist) {
		return Stats{}, nil
	} else if err != nil {
		return Stats{}, fmt.Errorf("inspect Local Cache metadata: %w", err)
	}

	dataSource := (&url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(metadataPath),
		RawQuery: "mode=ro",
	}).String()
	database, err := sql.Open("sqlite", dataSource)
	if err != nil {
		return Stats{}, fmt.Errorf("open Local Cache metadata read-only: %w", err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	return readArtifactStats(ctx, database)
}
