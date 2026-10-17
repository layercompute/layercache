//go:build !linux && !darwin

package storageinspect

import "io/fs"

const platformSupported = false

func metadata(fs.FileInfo) fileMetadata { return fileMetadata{} }

func filesystem(string) (Filesystem, error) { return Filesystem{Type: "unknown"}, nil }
