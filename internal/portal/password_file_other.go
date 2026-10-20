//go:build !linux && !darwin

package portal

import "errors"

func readPasswordAccountsFile(string) ([]byte, error) {
	return nil, errors.New("protected password accounts require Linux or macOS")
}
func writePasswordAccountsFile(string, []byte) error {
	return errors.New("protected password accounts require Linux or macOS")
}
