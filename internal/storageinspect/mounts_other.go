//go:build !linux

package storageinspect

func mountPoints() (map[string]bool, error) { return nil, nil }
