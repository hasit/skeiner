// Command skeiner serves an OpenAPI catalog and evaluates agents through MCP.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/hasit/skeiner/examples/ci/replay"
	"github.com/hasit/skeiner/internal/catalog"
	"github.com/hasit/skeiner/internal/dispatch"
	"github.com/hasit/skeiner/internal/eval"
	"github.com/hasit/skeiner/internal/fixture"
	"github.com/hasit/skeiner/internal/sandbox"
	"github.com/hasit/skeiner/internal/service"
	"github.com/hasit/skeiner/internal/transport"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const usage = `Skeiner — run JavaScript programs against an OpenAPI catalog

  skeiner preflight --spec ./openapi.yaml
  skeiner serve --spec ./openapi.yaml
  skeiner run --spec ./openapi.yaml --program ./investigate.js
  skeiner eval --spec ./examples/ci/openapi.yaml --suite ./evals/ci.yaml
  skeiner tasks --suite ./evals/ci.yaml
  skeiner grade --suite ./evals/ci.yaml --case CASE --input ./receipt.json
  skeiner fixture [--listen 127.0.0.1:8087]
  skeiner version

Use a subcommand with -h to see its options.
`

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "skeiner:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, diag io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "help", "-h", "--help":
		_, err := fmt.Fprint(out, usage)
		return err
	case "version", "--version":
		_, err := fmt.Fprintln(out, transport.Implementation.Version)
		return err
	case "fixture":
		return serveFixture(ctx, args[1:], diag)
	case "tasks", "grade":
		return ownerEvaluation(args[0], args[1:], out, diag)
	case "preflight", "serve", "run", "eval":
	default:
		return fmt.Errorf("unknown command %q; use skeiner help", args[0])
	}

	mode := args[0]
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(diag)
	spec := fs.String("spec", "", "local OpenAPI JSON or YAML file")
	base := fs.String("base-url", os.Getenv("SKEINER_BASE_URL"), "upstream base URL (overrides spec server)")
	tokenEnv := fs.String("token-env", os.Getenv("SKEINER_TOKEN_ENV"), "name of environment variable holding a bearer token")
	var headerBindings []string
	fs.Func("header-env", "bind a declared GET credential header to a host environment variable, e.g. Api-Key=DISCOURSE_KEY (repeatable)", func(v string) error {
		headerBindings = append(headerBindings, v)
		return nil
	})
	var ids, getPaths []string
	fs.Func("allow", "expose comma-separated operation `IDs` (with no allow flags, use SKEINER_ALLOW if set, otherwise all supported operations)", func(v string) error {
		ids = splitOperationIDs(v)
		return nil
	})
	fs.Func("allow-get", "expose GET at one exact OpenAPI `path` template (repeatable, combines with --allow)", func(v string) error {
		getPaths = append(getPaths, v)
		return nil
	})
	var suite, driver, model, caseID, state string
	var program, programMode string
	if mode == "run" {
		fs.StringVar(&program, "program", "", "file containing a JavaScript function body that returns JSON")
		fs.StringVar(&programMode, "mode", "", "JavaScript call mode (sync or async, detected when omitted)")
	}
	stateURLs := map[string]string{}
	if mode == "eval" {
		fs.StringVar(&suite, "suite", "", "owner-side YAML task suite")
		fs.StringVar(&driver, "driver", "", "replay (CI integration) or openai (live model); defaults to suite driver")
		fs.Func("state-url", "map a suite state to a prepared API base URL: NAME=URL (repeatable)", func(v string) error {
			k, u, ok := strings.Cut(v, "=")
			if !ok || k == "" || u == "" {
				return errors.New("expected state name and base URL separated by =")
			}
			stateURLs[k] = u
			return nil
		})
		fs.StringVar(&model, "model", "gpt-4.1", "OpenAI model for live evaluations")
		fs.StringVar(&caseID, "case", "", "run only this case ID")
		fs.StringVar(&state, "state", "", "override a case's fixture state to reproduce a regression")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if len(fs.Args()) != 0 || *spec == "" {
		return errors.New("expected --spec and no positional arguments")
	}
	if mode == "eval" && suite == "" {
		return errors.New("eval requires --suite")
	}
	if mode == "run" && program == "" {
		return errors.New("run requires --program")
	}
	if ids == nil && len(getPaths) == 0 {
		if inherited, ok := os.LookupEnv("SKEINER_ALLOW"); ok {
			ids = splitOperationIDs(inherited)
		}
	}
	var token string
	if *tokenEnv != "" {
		token = os.Getenv(*tokenEnv)
		if token == "" {
			return errors.New("configured bearer token environment variable is empty or missing")
		}
	}
	headers, err := readCredentialHeaders(headerBindings)
	if err != nil {
		return err
	}
	secrets := []string{token}
	for _, value := range headers {
		secrets = append(secrets, value)
	}
	redact := func(s string) string { return dispatch.Redact(s, secrets...) }
	cat, err := catalog.LoadSelected(*spec, ids, getPaths)
	if err != nil {
		return errors.New(redact(err.Error()))
	}
	if mode != "preflight" {
		for _, warning := range cat.Warnings {
			fmt.Fprintln(diag, "catalog:", redact(warning))
		}
	}
	cfg := dispatch.Config{BaseURL: *base, BearerToken: token, CredentialHeaders: headers}
	callerCat := cat.WithCredentials(token != "", headers)
	var client *dispatch.Client
	if mode != "eval" || len(headers) > 0 {
		allowlist := "all"
		switch {
		case len(ids) > 0 || len(getPaths) > 0:
			allowlist = "restricted"
		case ids != nil:
			allowlist = "empty"
		}
		client, err = dispatch.New(callerCat, cfg) // validates local configuration without contacting the API
		if err == nil {
			defer client.Close()
			if mode != "eval" && callerCat.Len() == 0 && allowlist != "empty" {
				err = errors.New("no callable operations under the configured policy; inspect the catalog with skeiner preflight --spec FILE")
			}
		}
		if mode == "preflight" {
			if reportErr := writePreflight(out, callerCat, allowlist, cfg, redact, err); reportErr != nil {
				return reportErr
			}
		}
		if err != nil || mode == "preflight" {
			return err
		}
	}
	engine, err := sandbox.New(ctx)
	if err != nil {
		return fmt.Errorf("initialize embedded JavaScript: %w", err)
	}
	defer engine.Close(context.Background())
	if mode == "serve" {
		return transport.New(service.New(callerCat, engine, client, nil)).Run(ctx, &mcp.StdioTransport{})
	}
	if mode == "run" {
		return runProgram(ctx, service.New(callerCat, engine, client, nil), program, sandbox.Mode(programMode), out)
	}

	if driver == "" {
		driver, err = eval.DefaultDriver(suite)
		if err != nil {
			return err
		}
	}
	var agent eval.Driver
	switch driver {
	case "replay":
		agent = replay.Driver{}
	case "openai":
		agent, err = eval.OpenAI(model)
		if err != nil {
			return err
		}
	default:
		return errors.New("--driver must be replay or openai")
	}
	opts := eval.Options{Driver: agent, Case: caseID, StateOverride: state}
	evalSecrets := append(secrets, os.Getenv("OPENAI_API_KEY"))
	opts.Redact = func(s string) string {
		return dispatch.Redact(s, evalSecrets...)
	}
	opts.Connect = func(ctx context.Context, state string, observe func(eval.Exchange)) (eval.Session, error) {
		stateCfg := cfg
		var local *httptest.Server
		if url, ok := stateURLs[state]; ok {
			stateCfg.BaseURL = url
		} else if state != "" {
			h, err := fixture.New(state)
			if err != nil {
				return nil, err
			}
			local = httptest.NewServer(h)
			stateCfg.BaseURL = local.URL
			stateCfg.BearerToken = ""
		}
		// Credentials belong to the configured API, including its path prefix.
		trustedBase := cfg.BaseURL
		if trustedBase == "" {
			trustedBase = cat.BaseURL
		}
		if stateCfg.BaseURL != "" && strings.TrimSuffix(stateCfg.BaseURL, "/") != strings.TrimSuffix(trustedBase, "/") {
			stateCfg.CredentialHeaders = nil
			stateCfg.BearerToken = ""
		}
		stateCat := cat.WithCredentials(stateCfg.BearerToken != "", stateCfg.CredentialHeaders)
		client, err := dispatch.New(stateCat, stateCfg)
		if err != nil {
			if local != nil {
				local.Close()
			}
			return nil, err
		}
		cleanup := func() {
			client.Close()
			if local != nil {
				local.Close()
			}
		}
		s := service.New(stateCat, engine, client, func(e service.Exchange) { observe(eval.Exchange(e)) })
		session, err := openSession(ctx, s, cleanup)
		if err != nil {
			cleanup()
			return nil, err
		}
		return session, nil
	}
	report, err := eval.Run(ctx, suite, opts)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return err
	}
	if !report.Passed() {
		return errors.New("evaluation failed; inspect checks, diagnosis and trace in the JSON report")
	}
	return nil
}

func runProgram(ctx context.Context, s *service.Service, path string, mode sandbox.Mode, out io.Writer) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	code, err := io.ReadAll(io.LimitReader(file, sandbox.MaxCodeBytes+1))
	if err != nil {
		return err
	}
	result, err := s.ExecuteMode(ctx, string(code), mode)
	if err != nil {
		return service.PublicError(err)
	}
	_, err = fmt.Fprintln(out, string(result))
	return err
}

func readCredentialHeaders(bindings []string) (map[string]string, error) {
	values := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		name, env, ok := strings.Cut(binding, "=")
		header, supported := catalog.CredentialHeaderName(name)
		if !ok || !supported || env == "" || strings.Contains(env, "=") {
			return nil, errors.New("--header-env expects Api-Key=ENV_NAME or Api-Username=ENV_NAME")
		}
		if _, exists := values[header]; exists {
			return nil, fmt.Errorf("--header-env repeats the %s binding", header)
		}
		value := os.Getenv(env)
		if value == "" {
			return nil, fmt.Errorf("the environment variable configured for %s is missing or empty", header)
		}
		values[header] = value
	}
	return values, nil
}

// An explicitly empty list selects zero operations. A nil list leaves the
// choice of named operations unspecified.
func splitOperationIDs(value string) []string {
	ids := []string{}
	for _, s := range strings.Split(value, ",") {
		if id := strings.TrimSpace(s); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func serveFixture(ctx context.Context, args []string, diag io.Writer) error {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	fs.SetOutput(diag)
	listen := fs.String("listen", "127.0.0.1:8087", "local HTTP listen address")
	state := fs.String("state", "all", "fixture snapshot")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected fixture argument")
	}
	h, err := fixture.New(*state)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	fmt.Fprintf(diag, "synthetic CI fixture listening at http://%s\n", ln.Addr())
	srv := http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	stop := context.AfterFunc(ctx, func() { _ = srv.Close() })
	defer stop()
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
