package storageinspect

import "testing"

func TestMountTableIncludesSameDeviceBindMountsAndEscapedPaths(t *testing.T) {
	mounts, err := parseMountPoints("42 30 8:1 / /work rw - ext4 /dev/root rw\n43 42 8:1 /source /work/nested\\040bind rw - ext4 /dev/root rw\n")
	if err != nil || len(mounts) != 2 || !mounts["/work"] || !mounts["/work/nested bind"] {
		t.Fatalf("mount points = %v, %v", mounts, err)
	}
	if _, err := parseMountPoints("invalid mount table"); err == nil {
		t.Fatal("accepted malformed mount information")
	}
}
