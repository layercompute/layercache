package actionscache

import (
	"errors"
	"fmt"
)

// These reasons describe this lookup only. A missing underlying entry does not
// identify whether quota, expiry or an explicit deletion removed it.
var (
	ErrLookupNoMatch      = fmt.Errorf("no matching Actions cache entry: %w", ErrNotFound)
	ErrLookupEntryRemoved = fmt.Errorf("Actions cache entry no longer available: %w", ErrNotFound)
	ErrLookupIntegrity    = fmt.Errorf("Actions cache entry failed integrity validation: %w", ErrNotFound)
)

func lookupMissReason(err error) string {
	switch {
	case errors.Is(err, ErrLookupIntegrity):
		return "integrity-rejected"
	case errors.Is(err, ErrLookupEntryRemoved):
		return "entry-removed"
	case errors.Is(err, ErrLookupNoMatch):
		return "no-matching-entry"
	case errors.Is(err, errRemoteDegraded):
		return "remote-unavailable"
	default:
		return "not-found"
	}
}
