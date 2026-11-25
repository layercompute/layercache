package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/xcodecache"
)

func runXcode(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("use xcode run, serve, status, or stop")
	}
	switch args[0] {
	case "run", "serve":
		return runXcodeService(ctx, args[0], args[1:], stdout, stderr)
	case "status", "stop":
		return runXcodeLifecycle(ctx, args[0], args[1:], stdout, stderr)
	default:
		return errors.New("use xcode run, serve, status, or stop")
	}
}

func runXcodeService(ctx context.Context, command string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("xcode "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options xcodecache.Options
	var sharedConfig *string
	if command == "run" {
		sharedConfig = flags.String("config", "", "use the private Xcode socket of an already running Local Cache")
	}
	flags.StringVar(&options.DataDir, "data-dir", "", "persistent compilation-cache directory")
	flags.StringVar(&options.Socket, "socket", "", "private absolute Unix socket path")
	flags.StringVar(&options.Project, "project", "", "project identity")
	flags.StringVar(&options.Compatibility, "compatibility", "", "exact Xcode/SDK/toolchain identity")
	flags.StringVar(&options.StatsFile, "stats-file", "", "protocol hit statistics JSON output")
	flags.Int64Var(&options.MaxBytes, "max-size", 5<<30, "maximum artifact bytes")
	flags.Int64Var(&options.MinFreeBytes, "min-free-bytes", 0, "minimum free filesystem bytes (0 keeps the automatic reserve; values below 5 GiB use 5 GiB)")
	if command == "serve" {
		flags.StringVar(&options.ReadyFile, "ready-file", "", "private absolute lifecycle receipt path")
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if sharedConfig != nil && *sharedConfig != "" {
		var incompatible bool
		flags.Visit(func(flag *flag.Flag) { incompatible = incompatible || flag.Name != "config" })
		if incompatible || flags.NArg() == 0 {
			return errors.New("xcode run --config selects the shared store and requires a child command")
		}
		cfg, err := config.Load(*sharedConfig)
		if err != nil {
			return err
		}
		if cfg.Xcode == nil {
			return errors.New("shared Xcode caching is disabled in this configuration")
		}
		serveCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		return xcodecache.RunShared(serveCtx, cfg.Xcode.Socket, flags.Args(), stdout, stderr)
	}
	if options.DataDir == "" {
		return errors.New("data-dir is required")
	}
	if command == "serve" {
		if !filepath.IsAbs(options.ReadyFile) {
			return errors.New("an absolute ready-file path is required")
		}
		if flags.NArg() != 0 {
			return errors.New("xcode serve does not accept a child command or positional arguments")
		}
	} else if flags.NArg() == 0 {
		return errors.New("xcodebuild command required after --")
	}
	serveCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if command == "serve" {
		return xcodecache.Serve(serveCtx, options)
	}
	return xcodecache.Run(serveCtx, options, flags.Args(), stdout, stderr)
}

func runXcodeLifecycle(ctx context.Context, command string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("xcode "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	readyFile := flags.String("ready-file", "", "private absolute lifecycle receipt path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !filepath.IsAbs(*readyFile) {
		return errors.New("an absolute ready-file path is required")
	}
	if flags.NArg() != 0 {
		return errors.New("xcode " + command + " does not accept positional arguments")
	}
	var status xcodecache.LifecycleStatus
	var err error
	if command == "stop" {
		status, err = xcodecache.Stop(ctx, *readyFile)
	} else {
		status, err = xcodecache.Status(ctx, *readyFile)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(status)
}
