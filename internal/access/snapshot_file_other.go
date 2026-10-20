//go:build !linux && !darwin

package access

func readSnapshotCredentialFile(string) ([]byte, error) {
	return nil, ErrSnapshotCredentials
}
