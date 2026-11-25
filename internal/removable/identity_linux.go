package removable

import "context"

// On Linux the enrollment marker and the mounted filesystem boundary identify
// the pool. The mount device and inode are rechecked during operation.
func volumeUUID(context.Context, string) (string, error) { return "", nil }
