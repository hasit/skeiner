// Package replay contains the credential-free CI protocol exerciser. It is an
// example driver, separate from the generic eval runner and live model adapter.
package replay

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/hasit/skeiner/internal/eval"
)

type Driver struct{}

func (Driver) Name() string { return "deterministic-ci-replay" }

//go:embed investigate.js
var program string
var runID = regexp.MustCompile(`(?i)\brun\s+#?(\d+)\b`)
var commit = regexp.MustCompile(`(?i)\b(?:commit|sha)\s+([a-f0-9]{6,64})\b`)

func (Driver) Answer(ctx context.Context, task eval.TaskInput, client eval.Client) (eval.Answer, error) {
	var out eval.Answer
	if task.Version != 1 {
		return out, errors.New("CI replay supports only the legacy fixture suite; use a live driver for structured tasks")
	}
	prompt := task.Prompt
	run, sha := runID.FindStringSubmatch(prompt), commit.FindStringSubmatch(prompt)
	if len(run) != 2 || len(sha) != 2 {
		return out, errors.New("CI replay needs a run number and commit SHA in the prompt")
	}
	tools, err := client.Tools(ctx)
	if err != nil {
		return out, err
	}
	available := map[string]bool{}
	for _, t := range tools {
		available[t.Name] = true
	}
	if !available["search"] || !available["execute"] {
		return out, errors.New("MCP did not advertise search and execute")
	}
	input := map[string]string{"runID": run[1], "sha": sha[1]}
	lookups := []struct{ query, parameter, key string }{
		{"look up run by id and jobs", "runId", "runOp"},
		{"job attempts including retries", "jobId", "attemptsOp"},
		{"failed job logs paginated lines", "attemptId", "logsOp"},
	}
	for _, q := range lookups {
		result, err := client.Call(ctx, "search", map[string]any{"query": q.query})
		if err != nil {
			return out, err
		}
		var matches []struct {
			ID     string `json:"operationId"`
			Params []struct {
				Name string `json:"name"`
				In   string `json:"in"`
			} `json:"parameters"`
		}
		if err = json.Unmarshal(result, &matches); err != nil {
			return out, fmt.Errorf("discovery: %w", err)
		}
		for _, m := range matches {
			for _, p := range m.Params {
				if p.Name == q.parameter && p.In == "path" {
					input[q.key] = m.ID
					break
				}
			}
			if input[q.key] != "" {
				break
			}
		}
		if input[q.key] == "" {
			return out, fmt.Errorf("discovery: no operation matching %q with path input %s", q.query, q.parameter)
		}
	}
	b, _ := json.Marshal(input)
	result, err := client.Call(ctx, "execute", map[string]any{"code": "const input = " + string(b) + ";\n" + program})
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(result, &out); err != nil {
		return out, fmt.Errorf("decode replay answer: %w", err)
	}
	return out, nil
}
