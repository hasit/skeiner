//go:build linux || darwin

package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// wazero caches executable machine code. Use a canonical path with trusted
// ancestors and require a dedicated, owner-private directory and contents.
func privateCacheDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", fmt.Errorf("runtime cache needs an existing parent: %w", err)
	}
	uid := uint32(os.Geteuid())
	for path := parent; ; path = filepath.Dir(path) {
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		owner := info.Sys().(*syscall.Stat_t).Uid
		// A trusted sticky directory (such as /tmp) preserves ownership of
		// its entries. Each child on our path must itself pass these checks.
		if !info.IsDir() || (owner != 0 && owner != uid) || (info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0) {
			return "", fmt.Errorf("runtime cache has an untrusted parent directory: %s", path)
		}
		if filepath.Dir(path) == path {
			break
		}
	}
	path := filepath.Join(parent, filepath.Base(abs))
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create runtime cache: %w", err)
	}
	err = filepath.WalkDir(path, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) && name != path {
			// A concurrent compiler can atomically rename its temporary file.
			return nil
		}
		if err != nil {
			return err
		}
		if (name == path && !info.IsDir()) || (!info.IsDir() && !info.Mode().IsRegular()) ||
			info.Sys().(*syscall.Stat_t).Uid != uid || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("runtime cache requires private directories and regular files owned by the current user: %s", name)
		}
		return nil
	})
	return path, err
}
