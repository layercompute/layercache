//go:build !linux && !darwin

package executioncollect

import (
	"context"
	"errors"
)

func loadIdentitySeed(context.Context, string) ([32]byte, error) {
	return [32]byte{}, errors.New("protected client identity storage unavailable")
}
