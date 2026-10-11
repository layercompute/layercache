package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/layercache/layercache/internal/xcodecache"
)

func runXcode(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "run" {
		return errors.New("use xcode run [options] -- xcodebuild [arguments]")
	}
	flags := flag.NewFlagSet("xcode run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options xcodecache.Options
	flags.StringVar(&options.DataDir, "data-dir", "", "persistent compilation-cache directory")
	flags.StringVar(&options.Socket, "socket", "", "private absolute Unix socket path")
	flags.StringVar(&options.Project, "project", "", "project identity")
	flags.StringVar(&options.Compatibility, "compatibility", "", "exact Xcode/SDK/toolchain identity")
	flags.StringVar(&options.StatsFile, "stats-file", "", "protocol hit statistics JSON output")
	flags.Int64Var(&options.MaxBytes, "max-size", 5<<30, "maximum artifact bytes")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if options.DataDir == "" {
		return errors.New("data-dir is required")
	}
	command := flags.Args()
	if len(command) == 0 {
		return errors.New("xcodebuild command required after --")
	}
	serveCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return xcodecache.Run(serveCtx, options, command, stdout, stderr)
}
