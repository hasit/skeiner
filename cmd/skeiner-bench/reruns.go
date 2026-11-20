package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/hasit/skeiner/internal/fixture"
)

func measureReruns(ctx context.Context, binary, spec string, samples int) error {
	h, err := fixture.New("assertion")
	if err != nil {
		return err
	}
	server := httptest.NewServer(h)
	defer server.Close()
	dir, err := os.MkdirTemp("", "skeiner-reruns-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "investigate.js")
	if err := os.WriteFile(file, []byte(program), 0600); err != nil {
		return err
	}
	run := func(cache string) (time.Duration, error) {
		args := []string{"run", "--spec", spec, "--base-url", server.URL, "--token-env", "", "--allow", "getRun,listJobAttempts,getAttemptLogs", "--program", file}
		if cache != "" {
			args = append(args, "--runtime-cache", cache)
		}
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Stderr = os.Stderr
		start := time.Now()
		raw, err := cmd.Output()
		elapsed := time.Since(start)
		if err != nil {
			return 0, err
		}
		return elapsed, verify(raw)
	}
	warm := filepath.Join(dir, "warm")
	if _, err := run(warm); err != nil {
		return err
	}
	labels := []string{"no cache", "empty cache", "populated cache"}
	times := make([][]time.Duration, len(labels))
	for sample := range samples {
		empty := filepath.Join(dir, "empty")
		dirs := []string{"", empty, warm}
		for i := range labels {
			index := (sample + i) % len(labels)
			elapsed, err := run(dirs[index])
			if err != nil {
				return fmt.Errorf("%s: %w", labels[index], err)
			}
			times[index] = append(times[index], elapsed)
		}
		if err := os.RemoveAll(empty); err != nil {
			return err
		}
	}
	for i, label := range labels {
		summarize(label, times[i])
	}
	return nil
}
