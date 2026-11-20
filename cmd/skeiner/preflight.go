package main

import (
	"encoding/json"
	"io"
	"slices"

	"github.com/hasit/skeiner/internal/catalog"
	"github.com/hasit/skeiner/internal/dispatch"
)

// Preflight is local owner output. The MCP client sees only enabled operations.
type preflightReport struct {
	Allowlist          string                  `json:"allowlist"`
	BearerConfigured   bool                    `json:"bearerConfigured"`
	ConfiguredHeaders  []string                `json:"configuredHeaders,omitempty"`
	Enabled            []catalog.OperationInfo `json:"enabled"`
	Excluded           []catalog.OperationInfo `json:"excluded"`
	Warnings           []string                `json:"warnings"`
	ConfigurationError string                  `json:"configurationError,omitempty"`
}

func writePreflight(out io.Writer, cat *catalog.Catalog, allowlist string, cfg dispatch.Config, redact func(string) string, configErr error) error {
	report := preflightReport{
		Allowlist:        allowlist,
		BearerConfigured: cfg.BearerToken != "",
		Enabled:          []catalog.OperationInfo{}, Excluded: []catalog.OperationInfo{}, Warnings: []string{},
	}
	for name := range cfg.CredentialHeaders {
		report.ConfiguredHeaders = append(report.ConfiguredHeaders, name)
	}
	slices.Sort(report.ConfiguredHeaders)
	if configErr != nil {
		report.ConfigurationError = redact(configErr.Error())
	}
	for _, op := range cat.Inventory() {
		op.Method = redact(op.Method)
		op.ID = redact(op.ID)
		op.Path = redact(op.Path)
		if op.Reason == "" {
			report.Enabled = append(report.Enabled, op)
		} else {
			op.Reason = redact(op.Reason)
			report.Excluded = append(report.Excluded, op)
		}
	}
	for _, warning := range cat.Warnings {
		report.Warnings = append(report.Warnings, redact(warning))
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}
