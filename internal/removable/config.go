// Package removable verifies an explicitly enrolled cache directory on a mount.
package removable

import (
	"encoding/hex"
	"errors"
	"math"
	"path/filepath"
	"strings"
)

type Config struct {
	MountPath  string `json:"mountPath"`
	Directory  string `json:"directory"`
	VolumeUUID string `json:"volumeUuid,omitempty"`
	ID         string `json:"id"`
	MaxBytes   int64  `json:"maxBytes"`
	Only       bool   `json:"onlyWhileAttached,omitempty"`
}

func (cfg Config) Validate() error {
	if !filepath.IsAbs(cfg.MountPath) || filepath.Clean(cfg.MountPath) != cfg.MountPath ||
		cfg.MountPath == filepath.Dir(cfg.MountPath) || strings.ContainsRune(cfg.MountPath, 0) {
		return errors.New("ssd.mountPath must name an absolute, clean filesystem mount point")
	}
	if cfg.Directory == "" || cfg.Directory == "." || cfg.Directory == ".." ||
		len(cfg.Directory) > 128 || strings.ContainsAny(cfg.Directory, "/\\\x00") {
		return errors.New("ssd.directory must be a single directory name")
	}
	if cfg.MaxBytes <= 0 || cfg.MaxBytes == math.MaxInt64 {
		return errors.New("ssd.maxBytes must be a positive bounded integer")
	}
	id, err := hex.DecodeString(cfg.ID)
	if err != nil || len(id) != 16 || cfg.ID != strings.ToLower(cfg.ID) {
		return errors.New("ssd.id must be a 128-bit lowercase hex enrollment ID")
	}
	return nil
}

func (cfg Config) MetadataDir(internalDir string) string {
	return filepath.Join(internalDir, "removable", cfg.ID)
}

const markerName = ".layercache-ssd"
