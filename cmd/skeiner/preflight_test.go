package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPreflightReflectsServingPolicy(t *testing.T) {
	spec := filepath.Join(t.TempDir(), "spec.yaml")
	const secret = "example-credential"
	err := os.WriteFile(spec, []byte(`openapi: 3.2.0
servers: [{url: "https://api.example.test"}]
components:
  securitySchemes: {access: {type: http, scheme: bearer}}
paths:
  /public:
    get: &read {operationId: public, responses: {'200': {content: {application/json: {schema: {type: string}}}}}}
  /private:
    get: {<<: *read, operationId: private, security: [{access: []}]}
  /off:
    delete: {<<: *read, operationId: remove}
  /example-credential:
    get: {<<: *read, operationId: example-credential, requestBody: {}}
  /standard:
    get: {<<: *read, operationId: shared}
  /custom:
    additionalOperations:
      LINK: {<<: *read, operationId: shared}
      example-credential: {<<: *read, operationId: customOnly}
`), 0600)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"preflight", "--spec", spec, "--base-url", "https://api.example.test", "--allow", "public,private,shared,customOnly," + secret}
	var out, diag bytes.Buffer
	inspect := func(tokenEnv string) preflightReport {
		t.Helper()
		out.Reset()
		diag.Reset()
		if err := run(context.Background(), append(args, "--token-env", tokenEnv), &out, &diag); err != nil {
			t.Fatal(err)
		}
		var report preflightReport
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		return report
	}
	anon := inspect("")
	if len(anon.Enabled) != 1 || anon.Enabled[0].ID != "public" || anon.Allowlist != "restricted" || anon.BearerConfigured {
		t.Fatalf("unexpected anonymous policy: %+v", anon)
	}
	expect := map[string]string{
		"GET /private private": "bearer", "DELETE /off remove": "allowlist", "GET /" + secret + " " + secret: "requestBody",
		"GET /standard shared": "ambiguous", "LINK /custom shared": "ambiguous",
		secret + " /custom customOnly": "additionalOperations",
	}
	for _, op := range anon.Excluded {
		key := op.Method + " " + op.Path + " " + op.ID
		reason, ok := expect[key]
		if !ok || !strings.Contains(op.Reason, reason) {
			t.Fatalf("unexpected exclusion: %+v", op)
		}
		delete(expect, key)
	}
	if len(expect) != 0 {
		t.Fatalf("missing exclusions: %v", expect)
	}
	t.Setenv("TEST_SKEINER_BEARER", secret)
	authed := inspect("TEST_SKEINER_BEARER")
	if len(authed.Enabled) != 2 || authed.Enabled[0].ID != "private" || !authed.BearerConfigured {
		t.Fatalf("unexpected configured policy: %+v", authed)
	}
	if strings.Contains(out.String()+diag.String(), secret) {
		t.Fatal("configured credential leaked in the operation inventory or diagnostics")
	}
}

func TestPreflightGETSelectors(t *testing.T) {
	spec := filepath.Join(t.TempDir(), "spec.yaml")
	err := os.WriteFile(spec, []byte(`openapi: 3.1.0
paths:
  /items:
    get: &read {responses: {'200': {content: {application/json: {schema: {type: string}}}}}}
    delete: {<<: *read, operationId: removeItems}
  /items/{id}:
    parameters: [{name: id, in: path, required: true, schema: {type: string}}]
    get: {<<: *read, operationId: getItem}
`), 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SKEINER_ALLOW", "removeItems")
	for _, tt := range []struct {
		name           string
		flags, enabled []string
	}{
		{"environment only", nil, []string{"removeItems"}},
		{"repeated paths override environment", []string{"--allow-get", "/items", "--allow-get", "/items/{id}"}, []string{"GET /items", "getItem"}},
		{"explicit union", []string{"--allow", "removeItems", "--allow-get", "/items/{id}"}, []string{"removeItems", "getItem"}},
		{"path with empty named list", []string{"--allow", "", "--allow-get", "/items"}, []string{"GET /items"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := []string{"preflight", "--spec", spec, "--base-url", "https://api.example.test", "--token-env", ""}
			var out bytes.Buffer
			if err := run(context.Background(), append(args, tt.flags...), &out, io.Discard); err != nil {
				t.Fatalf("preflight: %v\n%s", err, out.String())
			}
			var report preflightReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			var enabled []string
			for _, operation := range report.Enabled {
				enabled = append(enabled, operation.ID)
			}
			if report.Allowlist != "restricted" || !slices.Equal(enabled, tt.enabled) {
				t.Fatalf("effective policy = %s %v; want restricted %v", report.Allowlist, enabled, tt.enabled)
			}
		})
	}
}

func TestEmptyCatalogHasActionableExitStatus(t *testing.T) {
	spec, _ := examplePaths()
	for _, tt := range []struct {
		flags []string
		mode  string
		fail  bool
	}{
		{[]string{"--allow", "unavailable"}, "restricted", true},
		{[]string{"--allow", ""}, "empty", false},
		{[]string{"--allow-get", "/unknown"}, "restricted", true},
		{[]string{"--allow", "", "--allow-get", ""}, "restricted", true},
	} {
		var out bytes.Buffer
		args := append([]string{"preflight", "--spec", spec, "--base-url", "http://127.0.0.1:1", "--token-env", ""}, tt.flags...)
		err := run(context.Background(), args, &out, io.Discard)
		var report preflightReport
		if decodeErr := json.Unmarshal(out.Bytes(), &report); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if (err != nil) != tt.fail || report.Allowlist != tt.mode || len(report.Enabled) != 0 ||
			len(report.Excluded) != 5 || (report.ConfigurationError != "") != tt.fail {
			t.Fatalf("flags=%q: %+v, %v", tt.flags, report, err)
		}
		if tt.fail {
			if len(report.Warnings) == 0 {
				t.Fatalf("missing unavailable-selector warning for %q", tt.flags)
			}
			args[0] = "serve"
			out.Reset()
			err := run(context.Background(), args, &out, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "preflight") || out.Len() != 0 {
				t.Fatalf("serve did not surface unusable policy before MCP startup: %v", err)
			}
		}
	}
}

func TestPreflightHostHeaderBindings(t *testing.T) {
	const key = "trial-account-api-key"
	const username = "trial-account"
	t.Setenv("SKEINER_TRIAL_KEY", key)
	t.Setenv("SKEINER_TRIAL_USERNAME", username)
	spec := filepath.Join(t.TempDir(), "spec.yaml")
	err := os.WriteFile(spec, []byte(`openapi: 3.1.0
servers: [{url: "https://declared.example.test"}]
paths:
  /latest.json:
    get:
      operationId: listTopics
      parameters:
        - {in: header, name: Api-Key, required: true, schema: {type: string}}
        - {in: header, name: Api-Username, required: true, schema: {type: string}}
      responses: {'200': {content: {application/json: {schema: {type: array, items: {type: string}}}}}}
  /trial-account-api-key:
    get: {operationId: trial-account, requestBody: {}}
`), 0600)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"preflight", "--spec", spec, "--base-url", "https://forum.example.test", "--token-env", "", "--allow-get", "/latest.json", "--header-env", "Api-Key=SKEINER_TRIAL_KEY"}
	var out, diag bytes.Buffer
	if err := run(context.Background(), args, &out, &diag); err == nil || !strings.Contains(out.String(), "Api-Username") {
		t.Fatalf("missing username should exclude the feed: %v: %s", err, out.String())
	}
	out.Reset()
	args = append(args, "--header-env", "api-username=SKEINER_TRIAL_USERNAME")
	if err := run(context.Background(), args, &out, &diag); err != nil {
		t.Fatal(err)
	}
	var report preflightReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Enabled) != 1 || report.Enabled[0].ID != "listTopics" || report.BearerConfigured {
		t.Fatalf("unexpected effective header policy: %+v", report)
	}
	if strings.Contains(out.String()+diag.String(), username) || strings.Contains(out.String()+diag.String(), "[REDACTED]-api-key") {
		t.Fatal("inventory or diagnostics exposed part of a configured credential")
	}
}
