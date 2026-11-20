//go:build linux || darwin

package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestRuntimeCacheRejectsUnsafePaths(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "cache")
	if _, err := privateCacheDir(dir); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "linked")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := privateCacheDir(link); err == nil {
		t.Fatal("accepted a symlink as the cache directory")
	}
	file := filepath.Join(dir, "artifact")
	if err := os.Symlink(base, file); err != nil {
		t.Fatal(err)
	}
	if _, err := privateCacheDir(dir); err == nil {
		t.Fatal("accepted a linked cache entry")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := privateCacheDir(dir); err == nil {
		t.Fatal("accepted a shared cache entry")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := privateCacheDir(dir); err == nil {
		t.Fatal("accepted a shared cache directory")
	}
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := privateCacheDir(filepath.Join(dir, "child")); err == nil {
		t.Fatal("accepted a writable non-sticky ancestor")
	}
}

func TestRuntimeCacheAcrossEngines(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "cache")
	run := func() error {
		engine, err := NewWithCache(ctx, dir)
		if err != nil {
			return err
		}
		defer engine.Close(ctx)
		result, err := engine.Run(ctx, `const previous = globalThis.counter || 0; globalThis.counter = previous + 1; return globalThis.counter;`, nil)
		if err != nil {
			return err
		}
		if string(result) != "1" {
			return fmt.Errorf("guest inherited state from another engine: %s", result)
		}
		return nil
	}
	// Separate runtimes share only the filesystem, as independent commands do.
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := run(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	// A third engine can read the completed compilation after both writers close.
	if err := run(); err != nil {
		t.Fatal(err)
	}
}
