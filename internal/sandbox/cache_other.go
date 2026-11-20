//go:build !linux && !darwin

package sandbox

import "errors"

func privateCacheDir(string) (string, error) {
	return "", errors.New("runtime compilation caching is supported on Linux and macOS")
}
