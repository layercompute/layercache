package xcodecache

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cas "github.com/layercache/layercache/internal/xcodecache/proto/casv1"
	kv "github.com/layercache/layercache/internal/xcodecache/proto/kvv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestXcodeCompilerChild(t *testing.T) {
	if os.Getenv("LAYERCACHE_XCODE_CHILD") != "1" {
		return
	}
	socket := ""
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "COMPILATION_CACHE_REMOTE_SERVICE_PATH=") {
			socket = strings.TrimPrefix(arg, "COMPILATION_CACHE_REMOTE_SERVICE_PATH=")
		}
	}
	if socket == "" {
		os.Exit(2)
	}
	conn, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		os.Exit(3)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	id, err := cas.NewCASDBServiceClient(conn).Save(ctx, &cas.CASSaveRequest{Data: &cas.CASBlob{Blob: blob([]byte("compiled"))}})
	if err != nil {
		os.Exit(4)
	}
	_, err = kv.NewKeyValueDBClient(conn).PutValue(ctx, &kv.PutValueRequest{Key: []byte("child-action"), Value: &kv.Value{Entries: map[string][]byte{"CASID": id.GetCasId().Id}}})
	if err != nil {
		os.Exit(5)
	}
	if os.Getenv("LAYERCACHE_XCODE_CHILD_FAIL") == "1" {
		os.Exit(7)
	}
	os.Exit(0)
}

func TestRunOwnsSocketAndClosesStoreEvenForFailedCompiler(t *testing.T) {
	t.Setenv("LAYERCACHE_XCODE_CHILD", "1")
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "compiler failure"}[fail], func(t *testing.T) {
			if fail {
				t.Setenv("LAYERCACHE_XCODE_CHILD_FAIL", "1")
			} else {
				t.Setenv("LAYERCACHE_XCODE_CHILD_FAIL", "0")
			}
			root := t.TempDir()
			// macOS has a shorter Unix socket limit than the usual test temp path.
			socketRoot, err := os.MkdirTemp("/tmp", "lc-xcode-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(socketRoot)
			options := Options{DataDir: filepath.Join(root, "store"), Socket: filepath.Join(socketRoot, "cache.sock"), Project: "test-project", Compatibility: "test-toolchain", StatsFile: filepath.Join(root, "stats.json"), MaxBytes: 32 << 20}
			var output bytes.Buffer
			err = Run(context.Background(), options, []string{os.Args[0], "-test.run=^TestXcodeCompilerChild$", "--"}, &output, &output)
			if fail {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 7 {
					t.Fatalf("preserve compiler exit: %v %s", err, &output)
				}
			} else if err != nil {
				t.Fatalf("compiler must connect to ready cache: %v %s", err, &output)
			}
			if _, err = os.Lstat(options.Socket); !os.IsNotExist(err) {
				t.Fatalf("socket must be removed: %v", err)
			}
			if _, err = os.Stat(options.StatsFile); err != nil {
				t.Fatal(err)
			}
			// The independently opened consumer observes a fully checkpointed producer.
			consumer := openService(t, options.DataDir, options.Project, options.Compatibility)
			action, err := consumer.GetValue(context.Background(), &kv.GetValueRequest{Key: []byte("child-action")})
			if err != nil || action.GetOutcome() != kv.GetValueResponse_SUCCESS {
				t.Fatalf("closed store replay: %v %v", action, err)
			}
		})
	}
}
