package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	"github.com/layercache/layercache/internal/storageinspect"
)

// Storage inspection deliberately bypasses configuration, credentials and store
// initialization. Opening a managed cache can reconcile or evict its contents.
func runStorage(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "inspect" {
		return errors.New("storage requires inspect --path ABSOLUTE_PATH [--path ABSOLUTE_PATH ...] [--json]")
	}
	flags := flag.NewFlagSet("storage inspect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var paths []string
	flags.Func("path", "absolute path to inspect without following symlinks; repeat for multiple roots", func(path string) error {
		paths = append(paths, path)
		return nil
	})
	jsonOutput := flags.Bool("json", false, "print the complete report as JSON, including partial results")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || len(paths) == 0 {
		return errors.New("storage inspect requires one or more --path flags and no positional arguments")
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, inspectErr := storageinspect.Inspect(ctx, paths)
	if inspectErr != nil {
		report.Incomplete = true
	}
	var outputErr error
	if *jsonOutput {
		outputErr = json.NewEncoder(stdout).Encode(report)
	} else {
		outputErr = printStorageReport(stdout, report)
	}
	if outputErr != nil {
		return outputErr
	}
	if inspectErr != nil {
		return fmt.Errorf("storage inspection incomplete: %w", inspectErr)
	}
	if report.Incomplete {
		return errors.New("storage inspection incomplete; inspect issues and limitations in the report")
	}
	return nil
}

func printStorageReport(output io.Writer, report storageinspect.Report) error {
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "Local storage inspection")
	for _, root := range report.Roots {
		_, _ = fmt.Fprintf(table, "%s\t%s available on filesystem\n", root.Path, storageBytes(root.Filesystem.AvailableBytes))
	}
	_, _ = fmt.Fprintf(table, "\nAllocated inode bytes\t%s\nApparent bytes\t%s\n", storageBytes(report.Totals.AllocatedBytes), formatStorageBytes(report.Totals.ApparentBytes))
	_, _ = fmt.Fprintf(table, "Files\t%d\nDirectories\t%d\nSymlinks\t%d\nSparse/compressed files\t%d\n", report.Totals.Files, report.Totals.Directories, report.Totals.Symlinks, report.Totals.SparseFiles)
	_, _ = fmt.Fprintf(table, "Hardlink aliases counted once\t%d\nNested mounts skipped\t%d\nDisappeared entries\t%d\n", report.HardlinkAliases, report.SkippedMounts, report.DisappearedEntries)
	_, _ = fmt.Fprintln(table, "\nPath hint\tAllocated bytes\tApparent bytes")
	for _, category := range report.Categories {
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\n", category.Name, storageBytes(category.Counts.AllocatedBytes), formatStorageBytes(category.Counts.ApparentBytes))
	}
	_, _ = fmt.Fprintln(table, "\nLive scan; categories do not identify deletable data. Inode allocation is not exclusive physical usage or reclaimable space.")
	if report.Incomplete {
		_, _ = fmt.Fprintf(table, "INCOMPLETE: %d issues; %d details shown.\n", report.IssueCount, len(report.Issues))
	}
	for _, issue := range report.Issues {
		_, _ = fmt.Fprintf(table, "%s: %s: %s\n", issue.Operation, issue.Path, issue.Error)
	}
	for _, limitation := range report.Limitations {
		_, _ = fmt.Fprintln(table, limitation)
	}
	return table.Flush()
}

func storageBytes(bytes *uint64) string {
	if bytes == nil {
		return "unknown"
	}
	return formatStorageBytes(*bytes)
}

func formatStorageBytes(bytes uint64) string {
	if bytes > math.MaxInt64 {
		return fmt.Sprintf("%d B", bytes)
	}
	return formatByteCount(int64(bytes))
}
