package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/doctor"
	"github.com/nilpntr/content-fabrique/internal/envfile"
	"github.com/nilpntr/content-fabrique/internal/envlint"
)

// lookupEnv is os.LookupEnv; tests pass a fake process environment.
type lookupEnv func(string) (string, bool)

const doctorUsage = `Usage:
  cfctl doctor --env <local|ci|bench|staging|production> [--phase P0..P7] [--env-file PATH] [--json]

Checks the required variables and makes one cheap authenticated call per service. Prints variable names and
statuses (ok, missing, invalid, skipped), never values. Exits 1 on any missing or invalid.

  --env       the environment to check (required)
  --phase     the project phase; later-phase variables become required, and the sandbox gateway is checked from P4 (default P0)
  --env-file  the env file; default .env for local and bench, .env.staging and .env.production for those, none for ci
              (process environment only). A non-empty value in the file wins; the process environment fills every
              name the file lacks or leaves empty.
  --json      machine-readable output
`

func cmdDoctor(args []string, stdout, stderr io.Writer, lookup lookupEnv) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, doctorUsage) }
	envName := flags.String("env", "", "environment")
	phaseArg := flags.String("phase", "P0", "phase")
	envFileArg := flags.String("env-file", "", "env file")
	asJSON := flags.Bool("json", false, "JSON output")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl doctor: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	if !slices.Contains(doctor.Environments, *envName) {
		_, _ = fmt.Fprintf(stderr, "cfctl doctor: --env must be one of %s\n", strings.Join(doctor.Environments, ", "))
		return 2
	}
	phase, err := doctor.ParsePhase(*phaseArg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl doctor: --phase: %v\n", err)
		return 2
	}

	root, example, code := loadExample(stderr)
	if code != 0 {
		return code
	}

	// Pick the env file.
	explicit := *envFileArg != ""
	path := *envFileArg
	if !explicit {
		switch *envName {
		case "local", "bench":
			path = filepath.Join(root, ".env")
		case "staging", "production":
			path = filepath.Join(root, ".env."+*envName)
		}
	}
	file := map[string]string{}
	shown := "(none: process environment only)"
	if path != "" {
		shown = displayPath(root, path)
		m, err := envfile.ParseFile(path)
		switch {
		case err == nil:
			file = m
		case errors.Is(err, fs.ErrNotExist) && !explicit:
			shown += " (not found: process environment only)"
		default:
			_, _ = fmt.Fprintf(stderr, "cfctl doctor: cannot read the env file %s\n", shown)
			return 2
		}
	}

	vars := doctor.MergeEnv(example, file, lookup)
	redactor := doctor.NewRedactor(example, vars)
	out := redactor.Writer(stdout)

	rep := doctor.Run(context.Background(), doctor.Options{
		Env:       *envName,
		Phase:     phase,
		Example:   example,
		Vars:      vars,
		RoutingFS: os.DirFS(filepath.Join(root, "apps", "server", "config", "routing")),
	})
	// Redact the results before encoding: JSON escapes & < > and quotes, which would hide a value from the writer.
	redacted := redactor.Report(rep)
	if *asJSON {
		if err := doctor.WriteJSON(out, redacted, redactor.Redact(shown)); err != nil {
			_, _ = fmt.Fprintln(stderr, "cfctl doctor: cannot write the report")
			return 2
		}
	} else {
		doctor.WriteText(out, redacted, redactor.Redact(shown))
	}
	if rep.Failed() {
		return 1
	}
	return 0
}

// loadExample finds the repository root and parses .env.example, printing the problem and returning a non-zero code on failure.
func loadExample(stderr io.Writer) (root string, example []envfile.Var, code int) {
	cwd, err := os.Getwd()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cfctl: cannot determine the working directory")
		return "", nil, 2
	}
	root, err = envfile.FindRoot(cwd)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl: %v\n", err)
		return "", nil, 2
	}
	f, err := os.Open(filepath.Join(root, envfile.ExampleFile)) //nolint:gosec // G304: the root is the operator's repo checkout, not request input
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl: cannot read %s\n", envfile.ExampleFile)
		return "", nil, 2
	}
	defer func() { _ = f.Close() }()
	example, err = envfile.ParseExample(f)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl: %v\n", err)
		return "", nil, 2
	}
	return root, example, 0
}

func displayPath(root, path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		if rel, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return path
}

// ---- cfctl env -------------------------------------------------------------------------------------------------

const envUsage = `Usage:
  cfctl env init [--env-file PATH]   copy .env.example to .env if missing and generate the empty [AUTO] secrets
  cfctl env lint                     fail if code reads a variable that .env.example does not list
`

func cmdEnv(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, envUsage)
		return 2
	}
	switch args[0] {
	case "init":
		return cmdEnvInit(args[1:], stdout, stderr)
	case "lint":
		return cmdEnvLint(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(stdout, envUsage)
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "cfctl env: unknown subcommand %q\n\n%s", args[0], envUsage)
	return 2
}

// cmdEnvInit creates the env file and fills the generated secrets. It prints names only, never values.
func cmdEnvInit(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("env init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	envFileArg := flags.String("env-file", "", "env file to create or fill (default .env at the repository root)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	root, _, code := loadExample(stderr)
	if code != 0 {
		return code
	}
	path := *envFileArg
	if path == "" {
		path = filepath.Join(root, ".env")
	}
	shown := displayPath(root, path)
	res, err := envfile.Init(filepath.Join(root, envfile.ExampleFile), path, nil)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl env init: failed for %s\n", shown)
		return 1
	}
	if res.Created {
		_, _ = fmt.Fprintf(stdout, "created %s from %s\n", shown, envfile.ExampleFile)
	}
	if len(res.Generated) > 0 {
		_, _ = fmt.Fprintf(stdout, "generated: %s\n", strings.Join(res.Generated, ", "))
	}
	if len(res.AlreadySet) > 0 {
		_, _ = fmt.Fprintf(stdout, "already set: %s\n", strings.Join(res.AlreadySet, ", "))
	}
	if !res.Created && len(res.Generated) == 0 {
		_, _ = fmt.Fprintf(stdout, "%s is up to date, nothing changed\n", shown)
	}
	return 0
}

func cmdEnvLint(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		_, _ = fmt.Fprint(stderr, envUsage)
		return 2
	}
	root, example, code := loadExample(stderr)
	if code != 0 {
		return code
	}
	known := map[string]bool{}
	for _, v := range example {
		known[v.Name] = true
	}
	findings, err := envlint.Lint(root, known)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl env lint: %v\n", err)
		return 2
	}
	for _, f := range findings {
		_, _ = fmt.Fprintln(stderr, f)
	}
	if len(findings) > 0 {
		_, _ = fmt.Fprintf(stderr, "env lint: %d unlisted variable read(s). Add the name to %s, or stop reading it.\n", len(findings), envfile.ExampleFile)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "env lint: ok, every variable the code reads is listed in %s\n", envfile.ExampleFile)
	return 0
}
