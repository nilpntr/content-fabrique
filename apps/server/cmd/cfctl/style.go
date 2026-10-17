package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
)

const styleUsage = `Usage:
  cfctl style analyze --workspace <wks_... or org_...> --files <a.mp4,b.mp4,...> [--append] [--no-vision] [--no-store] [flags]
  cfctl style show    --workspace <wks_... or org_...> [--json] [flags]
  cfctl style clear   --workspace <wks_... or org_...> [flags]

DEVELOPMENT ONLY, like cfctl project show (refused unless CF_ENV is local, ci or bench). The creator style profile (P6-05,
docs/design/style-profile-v1.md) is what a creator's finished videos look and sound like: cuts and shot lengths, effects, the light, the
title text, the music. analyze measures the reference videos you name (local files; they are only ever an input of the sandbox, and
nothing of them is kept but numbers and the title text), reads the title style from three frames of each hook with one Haiku call
(at most $0.01 a reference, metered like every model call), aggregates the measures into the workspace's profile and prints it.
show prints the stored profile in plain language, with --json as the stored document. clear deletes it.

  --workspace   a wks_... id or a Clerk org_... id (required)
  --files       the reference videos, comma separated (or give them as arguments)
  --append      add the references to the profile's existing ones (at most 10 are kept, the newest) instead of replacing them
  --no-vision   do not read the title text (no model call, no cost)
  --no-store    measure and print only: nothing is written and no database is needed (the title text is not read either)
  --provider    the sandbox: docker (default), hardened or local (ffmpeg on this machine, development only)
  --image       the media-tools image for docker and hardened (default media-tools:dev)
  --json        print the profile as JSON
  --env-file    the env file; default .env at the repository root
  --yes         needed unless CF_ENV is local or ci and the database is on this machine
`

func cmdStyle(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, styleUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, styleUsage)
		return 0
	case "analyze":
		return cmdStyleAnalyze(args[1:], stdout, stderr, lookup, deps)
	case "show":
		return cmdStyleShow(args[1:], stdout, stderr, lookup, deps, false)
	case "clear":
		return cmdStyleShow(args[1:], stdout, stderr, lookup, deps, true)
	}
	_, _ = fmt.Fprintf(stderr, "cfctl style: unknown command %q\n\n%s", args[0], styleUsage)
	return 2
}

func cmdStyleAnalyze(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	const name = "style analyze"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, styleUsage) }
	workspaceArg := flags.String("workspace", "", "workspace id")
	filesArg := flags.String("files", "", "reference videos")
	appendRefs := flags.Bool("append", false, "add to the existing references")
	noVision := flags.Bool("no-vision", false, "skip the title text read")
	noStore := flags.Bool("no-store", false, "measure and print only")
	provider := flags.String("provider", "docker", "sandbox provider")
	image := flags.String("image", "media-tools:dev", "media-tools image")
	asJSON := flags.Bool("json", false, "print JSON")
	envFileArg := flags.String("env-file", "", "env file")
	yes := flags.Bool("yes", false, "confirm outside plain local development")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	var files []string
	for _, f := range strings.Split(*filesArg, ",") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	files = append(files, flags.Args()...)
	if len(files) == 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: name the reference videos with --files\n", name)
		return 2
	}
	if len(files) > styleprofile.MaxReferences && !*appendRefs {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: at most %d references\n", name, styleprofile.MaxReferences)
		return 2
	}
	var wantWS ids.WorkspaceID
	var wantOrg string
	if !*noStore || *workspaceArg != "" {
		var ok bool
		if wantWS, wantOrg, ok = parseWorkspaceRef(name, *workspaceArg, stderr); !ok {
			return 2
		}
	}
	var roots []string
	for _, f := range files {
		abs, err := filepath.Abs(f)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
			return 2
		}
		fi, err := os.Stat(abs)
		if err != nil || !fi.Mode().IsRegular() {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: %s is not a file\n", name, f)
			return 2
		}
		resolved, err := filepath.EvalSymlinks(filepath.Dir(abs))
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
			return 2
		}
		roots = append(roots, resolved)
	}
	if tmp, err := filepath.EvalSymlinks(os.TempDir()); err == nil {
		roots = append(roots, tmp)
	}
	env, code := guardDevCommand(name, *envFileArg, !*noStore, *yes, stdout, stderr, lookup)
	if code != 0 {
		return code
	}
	out, errOut := env.out, env.err
	runner, err := styleRunner(*provider, *image, roots)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		return 2
	}
	ctx, stop := signalContext()
	defer stop()

	var st *store.Store
	ws := ids.WorkspaceID{UUID: uuid.New()}
	if !*noStore {
		if st, code = env.openDB(ctx, name, deps); code != 0 {
			return code
		}
		defer st.Close()
		w, code := findWorkspace(ctx, st, name, wantWS, wantOrg, errOut)
		if code != 0 {
			return code
		}
		ws = w.ID
	}
	an := &styleprofile.Analyzer{Runner: runner}
	if !*noVision && !*noStore {
		reader, rcode := styleReader(env, runner, st, deps, errOut)
		if rcode != 0 {
			return rcode
		}
		an.Reader = reader
	} else if !*noVision {
		_, _ = fmt.Fprintln(out, "note: --no-store skips the title text read (it needs the ledger); add the workspace and drop --no-store to read it")
	}

	var measures []styleprofile.Measures
	if *appendRefs && st != nil {
		_, old, err := styleprofile.Load(ctx, st, ws)
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
			return 1
		}
		measures = old
	}
	var cost int64
	for _, f := range files {
		start := time.Now()
		res, err := an.AnalyzeFile(ctx, ws, f)
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl %s: %s: %v\n", name, filepath.Base(f), err)
			return 1
		}
		line := fmt.Sprintf("analysed %s in %s: %.1f s, %d cuts", res.Measures.Name, time.Since(start).Round(time.Millisecond), res.Measures.DurationS, len(res.Measures.CutsS))
		if res.Vision != nil {
			switch {
			case res.Vision.Text != nil:
				line += ", title read"
			case res.VisionErr != nil:
				line += ", title not read: " + res.VisionErr.Error()
			default:
				line += ", title not read (" + res.Vision.Reason + ")"
			}
		}
		_, _ = fmt.Fprintln(out, line)
		measures = append(measures, res.Measures)
	}
	if len(measures) > styleprofile.MaxReferences {
		measures = measures[len(measures)-styleprofile.MaxReferences:]
	}
	prof := styleprofile.Aggregate(measures)
	if !*noStore {
		if _, err := styleprofile.Save(ctx, st, ws, measures); err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
			return 1
		}
		if cost, err = styleCost(ctx, st, ws); err == nil && cost > 0 {
			_, _ = fmt.Fprintf(out, "model cost of the title reads (this workspace's ledger, today): %s\n", usd(cost))
		}
	}
	printProfile(out, prof, *asJSON, !*noStore)
	return 0
}

// styleRunner builds the sandbox runner for the references: the allowed roots are the directories of the files and the temp directory.
func styleRunner(provider, image string, roots []string) (media.Runner, error) {
	pol := sandbox.Policy{AllowedRoots: roots}
	var p sandbox.Provider
	switch provider {
	case "local":
		p = sandbox.NewLocal(pol)
	case "docker":
		p = sandbox.NewDocker(pol)
	case "hardened":
		p = sandbox.NewHardened(pol)
	default:
		return nil, fmt.Errorf("unknown provider %q (use local, docker or hardened)", provider)
	}
	return &media.SandboxRunner{Provider: p, Image: image, Limits: sandbox.Limits{CPU: 2, MemoryMB: 3072, Timeout: 15 * time.Minute, PIDs: 256}}, nil
}

// styleReader builds the vision reader: the model factory over the store's ledger (spend holds and the usage ledger), the router.
func styleReader(env *agentEnv, runner media.Runner, st *store.Store, deps agentDeps, errOut io.Writer) (*styleprofile.Reader, int) {
	settings, err := llm.LoadSettings(env.get)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl style: %v\n", err)
		return nil, 2
	}
	routingFS := os.DirFS(filepath.Join(env.root, "apps", "server", "config", "routing"))
	router, err := llm.NewRouter(routingFS, cmp.Or(settings.RoutingProfile, "dev-cheap"))
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl style: routing: %v\n", err)
		return nil, 2
	}
	prices, err := llm.LoadPriceTable(os.DirFS(filepath.Join(env.root, "apps", "server", "config", "pricing")), llm.PriceFile)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl style: %v\n", err)
		return nil, 2
	}
	factory, err := llm.NewFactory(llm.FactoryOptions{Settings: settings, Getenv: env.get, Prices: prices, Ledger: st, HTTPClient: deps.httpClient})
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl style: %v\n", err)
		return nil, 2
	}
	return &styleprofile.Reader{Models: factory, Router: router, Runner: runner}, 0
}

// styleCost is what the workspace's ledger holds for today (the title reads are the only model calls of this command).
func styleCost(ctx context.Context, st *store.Store, ws ids.WorkspaceID) (int64, error) {
	now := time.Now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return st.UsageCostMicros(ctx, ws, day, day.Add(24*time.Hour))
}

func printProfile(out io.Writer, p *styleprofile.Profile, asJSON, stored bool) {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(p)
		return
	}
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, p.Describe())
	if len(p.Evidence) > 0 {
		_, _ = fmt.Fprintln(out, "\nEvidence:")
		for _, e := range p.Evidence {
			_, _ = fmt.Fprintln(out, "  - "+e)
		}
	}
	if stored {
		_, _ = fmt.Fprintln(out, "\nStored as the workspace's creator style profile.")
	}
}

func cmdStyleShow(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps, remove bool) int {
	name := "style show"
	if remove {
		name = "style clear"
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, styleUsage) }
	workspaceArg := flags.String("workspace", "", "workspace id")
	asJSON := flags.Bool("json", false, "print JSON")
	envFileArg := flags.String("env-file", "", "env file")
	yes := flags.Bool("yes", false, "confirm outside plain local development")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	wantWS, wantOrg, ok := parseWorkspaceRef(name, *workspaceArg, stderr)
	if !ok {
		return 2
	}
	env, code := guardDevCommand(name, *envFileArg, remove, *yes, stdout, stderr, lookup)
	if code != 0 {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	st, code := env.openDB(ctx, name, deps)
	if code != 0 {
		return code
	}
	defer st.Close()
	ws, code := findWorkspace(ctx, st, name, wantWS, wantOrg, env.err)
	if code != 0 {
		return code
	}
	if remove {
		if err := st.DeleteCreatorStyle(ctx, ws.ID); err != nil {
			_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
			return 1
		}
		_, _ = fmt.Fprintln(env.out, "deleted the creator style profile of the workspace")
		return 0
	}
	p, _, err := styleprofile.Load(ctx, st, ws.ID)
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
		return 1
	}
	if p == nil {
		_, _ = fmt.Fprintln(env.out, "this workspace has no creator style profile (cfctl style analyze makes one)")
		return 0
	}
	printProfile(env.out, p, *asJSON, false)
	return 0
}
