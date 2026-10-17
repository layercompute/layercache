// Package storageinspect measures explicit local paths without managing their
// contents. Categories are path hints, never evidence that data can be deleted.
package storageinspect

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maximumIssues = 32

type Counts struct {
	// Bytes include regular files, directories, symlinks and other entries.
	ApparentBytes uint64 `json:"apparentBytes"`
	// AllocatedBytes sums st_blocks * 512. It is not exclusive physical usage:
	// reflinks can share blocks. Nil means allocation information is unavailable.
	AllocatedBytes *uint64 `json:"allocatedBytes"`
	Files          uint64  `json:"files"`
	Directories    uint64  `json:"directories"`
	Symlinks       uint64  `json:"symlinks"`
	OtherEntries   uint64  `json:"otherEntries"`
	// SparseFiles counts regular files with allocated bytes below apparent size.
	// Filesystem compression can also cause this difference.
	SparseFiles       uint64 `json:"sparseFiles"`
	allocationMissing bool
}

type Filesystem struct {
	DeviceID       *uint64 `json:"deviceId"`
	Type           string  `json:"type"`
	CapacityBytes  *uint64 `json:"capacityBytes"`
	AvailableBytes *uint64 `json:"availableBytes"`
}

type Root struct {
	Path       string     `json:"path"`
	Filesystem Filesystem `json:"filesystem"`
}

type Category struct {
	Name   string `json:"name"`
	Counts Counts `json:"counts"`
}

type Issue struct {
	Path      string `json:"path"`
	Operation string `json:"operation"`
	Error     string `json:"error"`
}

type Report struct {
	Roots              []Root     `json:"roots"`
	Totals             Counts     `json:"totals"`
	Categories         []Category `json:"categories"`
	HardlinkAliases    uint64     `json:"hardlinkAliases"`
	SkippedMounts      uint64     `json:"skippedMounts"`
	DisappearedEntries uint64     `json:"disappearedEntries"`
	IssueCount         uint64     `json:"issueCount"`
	Issues             []Issue    `json:"issues"`
	Incomplete         bool       `json:"incomplete"`
	Limitations        []string   `json:"limitations"`
}

type fileIdentity struct{ device, inode uint64 }

type fileMetadata struct {
	identity  fileIdentity
	known     bool
	allocated *uint64
}

// Inspect visits absolute paths without following symlinks. On Linux and Darwin
// it skips nested mounts and counts inode aliases once across the entire report.
// Explicitly requested mounted roots are inspected independently. Other
// platforms report unavailable capabilities in Limitations and Incomplete.
// Categories use the first encountered path of an inode, in lexical path order.
// The live scan is not an atomic filesystem snapshot. Entry errors return a
// partial report with at most 32 issue details; invalid arguments, cancellation
// and numeric overflow return an error as well as the partial report.
func Inspect(ctx context.Context, paths []string) (Report, error) {
	report := Report{Roots: []Root{}, Categories: []Category{}, Issues: []Issue{}, Limitations: []string{}}
	if err := ctx.Err(); err != nil {
		report.Incomplete = true
		return report, err
	}
	if len(paths) == 0 {
		return report, errors.New("storage inspection requires at least one absolute path")
	}
	roots := make([]string, 0, len(paths))
	unique := make(map[string]bool)
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			return report, fmt.Errorf("storage inspection requires an absolute path: %q", path)
		}
		path = filepath.Clean(path)
		if !unique[path] {
			roots = append(roots, path)
			unique[path] = true
		}
	}
	sort.Strings(roots)
	if !platformSupported {
		report.Incomplete = true
		report.Limitations = append(report.Limitations, "Filesystem capacity, allocated bytes, inode deduplication and nested mount detection are unsupported on this platform.")
	}
	mounts, err := mountPoints()
	if err != nil {
		report.issue("", "mount-table", err)
		return report, fmt.Errorf("cannot inspect storage without a mount table: %w", err)
	}
	seen := make(map[fileIdentity]string)
	seenPaths := make(map[string]bool)
	categories := make(map[string]*Counts)
	finish := func(err error) (Report, error) {
		for _, name := range []string{"dependencies", "emulator", "build-output", "unclassified"} {
			if counts := categories[name]; counts != nil {
				report.Categories = append(report.Categories, Category{Name: name, Counts: *counts})
			}
		}
		return report, err
	}
	for _, path := range roots {
		if err := ctx.Err(); err != nil {
			report.Incomplete = true
			return finish(err)
		}
		root := Root{Path: path, Filesystem: Filesystem{Type: "unknown"}}
		// An explicitly supplied path must not escape through a symlink in one
		// of its ancestors. A root that is itself a symlink is counted only.
		if err := inspectAncestors(path); err != nil {
			report.Roots = append(report.Roots, root)
			report.issue(path, "ancestors", err)
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			report.Roots = append(report.Roots, root)
			report.issue(path, "lstat", err)
			continue
		}
		rootMetadata := metadata(info)
		if rootMetadata.known {
			device := rootMetadata.identity.device
			root.Filesystem.DeviceID = &device
		}
		// Statfs follows symlinks, so query their parent instead of the target.
		volumePath := path
		if info.Mode()&os.ModeSymlink != 0 {
			volumePath = filepath.Dir(path)
		}
		volume, volumeErr := filesystem(volumePath)
		volume.DeviceID = root.Filesystem.DeviceID
		root.Filesystem = volume
		report.Roots = append(report.Roots, root)
		if volumeErr != nil {
			report.issue(path, "statfs", volumeErr)
		}
		err = walkRoot(path, info, func(current string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				report.issue(current, "walk", walkErr)
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				report.issue(current, "lstat", err)
				return nil
			}
			meta := metadata(info)
			if current != path && info.IsDir() && (mounts[current] || rootMetadata.known && meta.known && meta.identity.device != rootMetadata.identity.device) {
				report.SkippedMounts++
				return fs.SkipDir
			}
			previousPath, known := seen[meta.identity]
			if meta.known && known || !meta.known && seenPaths[current] {
				if info.IsDir() {
					return fs.SkipDir
				}
				if meta.known && previousPath != current && info.Mode().IsRegular() && !seenPaths[current] {
					report.HardlinkAliases++
					seenPaths[current] = true
				}
				return nil
			}
			if meta.known {
				seen[meta.identity] = current
			} else {
				seenPaths[current] = true
			}
			name := category(current, info)
			if categories[name] == nil {
				categories[name] = &Counts{}
			}
			if err := addEntry(&report.Totals, info, meta); err != nil {
				return err
			}
			return addEntry(categories[name], info, meta)
		})
		if err != nil {
			report.Incomplete = true
			return finish(err)
		}
	}
	return finish(nil)
}

// Rooted traversal prevents a concurrent directory-to-symlink replacement from
// redirecting the scan outside its selected root. WalkDir does not descend into
// symlink entries. The identity check also fences replacement while opening.
func walkRoot(path string, info fs.FileInfo, visit fs.WalkDirFunc) error {
	if !info.IsDir() {
		return visit(path, fs.FileInfoToDirEntry(info), nil)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return visit(path, nil, err)
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil {
		return visit(path, nil, err)
	}
	if !os.SameFile(info, opened) {
		return visit(path, nil, errors.New("directory changed while opening storage inspection root"))
	}
	return fs.WalkDir(root.FS(), ".", func(relative string, entry fs.DirEntry, err error) error {
		return visit(filepath.Join(path, relative), entry, err)
	})
}

func (report *Report) issue(path, operation string, err error) {
	report.Incomplete = true
	report.IssueCount++
	if errors.Is(err, fs.ErrNotExist) {
		report.DisappearedEntries++
	}
	if len(report.Issues) < maximumIssues {
		report.Issues = append(report.Issues, Issue{Path: path, Operation: operation, Error: err.Error()})
	}
}

func inspectAncestors(path string) error {
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("ancestor %q is a symlink", parent)
		}
		if parent == filepath.Dir(parent) {
			return nil
		}
	}
}

func addEntry(counts *Counts, info fs.FileInfo, meta fileMetadata) error {
	if info.Size() < 0 || uint64(info.Size()) > math.MaxUint64-counts.ApparentBytes {
		return errors.New("storage inspection apparent byte count overflow")
	}
	counts.ApparentBytes += uint64(info.Size())
	if meta.allocated == nil {
		counts.allocationMissing = true
		counts.AllocatedBytes = nil
	} else if !counts.allocationMissing {
		if counts.AllocatedBytes == nil {
			counts.AllocatedBytes = new(uint64)
		}
		if *meta.allocated > math.MaxUint64-*counts.AllocatedBytes {
			return errors.New("storage inspection allocated byte count overflow")
		}
		*counts.AllocatedBytes += *meta.allocated
	}
	switch {
	case info.Mode().IsRegular():
		counts.Files++
		if meta.allocated != nil && *meta.allocated < uint64(info.Size()) {
			counts.SparseFiles++
		}
	case info.IsDir():
		counts.Directories++
	case info.Mode()&os.ModeSymlink != 0:
		counts.Symlinks++
	default:
		counts.OtherEntries++
	}
	return nil
}

func category(path string, info fs.FileInfo) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, part := range parts {
		if part == "node_modules" {
			return "dependencies"
		}
	}
	for _, part := range parts {
		if part == "avd" || strings.HasSuffix(part, ".avd") {
			return "emulator"
		}
	}
	if info.Mode().IsRegular() && (strings.HasSuffix(path, ".qcow2") || strings.HasSuffix(path, ".img")) {
		return "emulator"
	}
	for _, part := range parts {
		if part == "build" || part == ".cxx" || part == "DerivedData" {
			return "build-output"
		}
	}
	return "unclassified"
}
