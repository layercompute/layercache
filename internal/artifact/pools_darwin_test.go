package artifact

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/layercache/layercache/internal/removable"
	"golang.org/x/sys/unix"
)

func TestSSDWritesOnlyAcrossForcedDetachAndReconnect(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	internal, err := Open(ctx, filepath.Join(root, "internal"), 5_000_000_000, 5<<30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = internal.Close() })
	image, mount := filepath.Join(root, "ssd.sparseimage"), filepath.Join(root, "mount")
	diskCommand := func(tool string, args ...string) {
		t.Helper()
		commandCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if output, err := exec.CommandContext(commandCtx, tool, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", tool, args, err, output)
		}
	}
	const hdiutil = "/usr/bin/hdiutil"
	const diskutil = "/usr/sbin/diskutil"
	makeMountpoint := func() {
		t.Helper()
		if err := os.Mkdir(mount, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
	}
	diskCommand(hdiutil, "create", "-type", "SPARSE", "-size", "8g", "-fs", "APFS", "-volname", "LayerCache Test", image)
	makeMountpoint()
	diskCommand(hdiutil, "attach", "-nobrowse", "-mountpoint", mount, image)
	device := mount
	t.Cleanup(func() {
		var mounted unix.Statfs_t
		if device != mount && unix.Statfs(mount, &mounted) == nil &&
			unix.ByteSliceToString(mounted.Mntonname[:]) == mount && unix.ByteSliceToString(mounted.Mntfromname[:]) == device {
			unmountCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			output, err := exec.CommandContext(unmountCtx, diskutil, "unmount", "force", device).CombinedOutput()
			cancel()
			if err != nil {
				t.Logf("fixture unmount before eject: %v\n%s", err, output)
			}
		}
		// Disk Arbitration can still be finishing the unmount after diskutil
		// returns. Retry only the transient busy error on the fixture device.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		for {
			output, err := exec.CommandContext(cleanupCtx, hdiutil, "detach", "-force", device).CombinedOutput()
			if err == nil {
				return
			}
			if cleanupCtx.Err() != nil || !strings.Contains(string(output), "Resource busy") {
				t.Fatalf("failed to eject fixture image: %v\n%s", err, output)
			}
			select {
			case <-cleanupCtx.Done():
				t.Fatalf("fixture image remained busy: %v\n%s", err, output)
			case <-time.After(250 * time.Millisecond):
			}
		}
	})
	var fs unix.Statfs_t
	if err := unix.Statfs(mount, &fs); err != nil {
		t.Fatal(err)
	}
	mountedDevice := unix.ByteSliceToString(fs.Mntfromname[:])
	if !strings.HasPrefix(mountedDevice, "/dev/disk") || unix.ByteSliceToString(fs.Mntonname[:]) != mount {
		t.Fatal("test image is not mounted at the fixture mountpoint")
	}
	device = mountedDevice
	attach := func() {
		t.Helper()
		makeMountpoint()
		diskCommand(diskutil, "mount", "nobrowse", "-mountPoint", mount, device)
	}
	detach := func() {
		t.Helper()
		// Force-unmount the volume while I/O is active. Eject the disk image
		// only in cleanup, after the old read and write handles are closed.
		diskCommand(diskutil, "unmount", "force", device)
	}
	cfg := removable.Config{
		MountPath: mount, Directory: "cache", ID: strings.Repeat("a", 32), MaxBytes: 1 << 20, WritesOnly: true,
	}
	if err := removable.Enroll(ctx, &cfg, internal.root); err != nil {
		t.Fatal(err)
	}
	detach()
	pools := NewPools(internal, cfg, nil)
	t.Cleanup(func() {
		if err := pools.Close(); err != nil {
			t.Errorf("close fixture pools: %v", err)
		}
	})

	key := func(name string) Key {
		return Key{Integration: "turbo", Project: "test", Compatibility: "default", Native: name}
	}
	write := func(name, value string, want error) {
		t.Helper()
		_, _, err := pools.Put(WithSizeHint(ctx, int64(len(value))), key(name), Metadata{}, strings.NewReader(value))
		if !errors.Is(err, want) {
			t.Fatalf("write %s: %v, want %v", name, err, want)
		}
	}
	read := func(name, want string, fromInternal bool) {
		t.Helper()
		entry, body, err := pools.Get(ctx, key(name))
		if err != nil {
			t.Fatal(err)
		}
		defer body.Close()
		bytes, err := io.ReadAll(body)
		if err != nil || string(bytes) != want || (entry.poolStore == internal) != fromInternal {
			t.Fatalf("read %s from wrong store or with wrong bytes: %q, %v", name, bytes, err)
		}
	}

	write("internal", "Mac hit", nil)
	attach()
	lock, err := os.OpenFile(filepath.Join(mount, cfg.Directory, ".layercache-runtime.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	read("internal", "Mac hit", true)
	write("locked", "do not spill", ErrQuota)
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	read("internal", "Mac hit", true)
	write("internal", "different", ErrConflict)
	write("ssd", "SSD hit", nil)
	read("ssd", "SSD hit", false)
	if _, _, err := pools.Get(ctx, key("absent")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown artifact was not a miss: %v", err)
	}
	if got := pools.Status(ctx).Lookups; got != (PoolLookupStats{InternalHits: 2, AttachedHits: 1, Misses: 1}) {
		t.Fatalf("lookups attributed to the wrong storage location: %+v", got)
	}
	write("oversized", strings.Repeat("x", int(cfg.MaxBytes)+1), ErrQuota)
	for _, name := range []string{"ssd", "oversized", "locked"} {
		if _, err := internal.Head(ctx, key(name)); !errors.Is(err, ErrNotFound) {
			t.Fatalf("attached write %s reached the internal cache: %v", name, err)
		}
	}

	_, restore, err := pools.Get(ctx, key("ssd"))
	if err != nil {
		t.Fatal(err)
	}
	defer restore.Close()
	prefix := make([]byte, 2)
	if _, err := io.ReadFull(restore, prefix); err != nil || string(prefix) != "SS" {
		t.Fatalf("unexpected restore prefix %q: %v", prefix, err)
	}

	reader, writer := io.Pipe()
	defer writer.Close()
	result := make(chan error, 1)
	go func() {
		defer reader.Close()
		_, _, err := pools.Put(ctx, key("interrupted"), Metadata{}, reader)
		result <- err
	}()
	if _, err := io.WriteString(writer, "partial upload"); err != nil {
		t.Fatal(err)
	}
	detach()
	remaining, restoreErr := io.ReadAll(restore)
	if !strings.HasPrefix("D hit", string(remaining)) || (restoreErr == nil && string(remaining) != "D hit") {
		t.Fatalf("disconnect returned invalid restore bytes %q: %v", remaining, restoreErr)
	}
	read("internal", "Mac hit", true)
	write("detached", "Mac fallback", nil)
	attach()
	// macOS can fail the old upload while flushing its partially consumed
	// body. Keep the reader lease open to guarantee an old handle is still
	// draining. Writes must be refused without spilling into the Mac cache.
	write("while-draining", "retry later", ErrQuota)
	_ = restore.Close()
	_ = writer.Close()
	select {
	case err := <-result:
		if !errors.Is(err, ErrQuota) {
			t.Fatalf("interrupted write: %v, want quota refusal", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("interrupted write did not finish")
	}
	if _, err := pools.Head(ctx, key("interrupted")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("interrupted upload became a cache entry: %v", err)
	}
	staging, err := os.ReadDir(filepath.Join(mount, cfg.Directory, "staging"))
	if err != nil || len(staging) != 0 {
		t.Fatalf("reconnect left interrupted staging: %v, %v", staging, err)
	}
	read("ssd", "SSD hit", false)
	read("detached", "Mac fallback", true)
	write("interrupted", "complete retry", nil)
	read("interrupted", "complete retry", false)
	if _, err := internal.Head(ctx, key("while-draining")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reattached write spilled into the internal cache: %v", err)
	}
}
