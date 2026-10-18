package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/creatorstyle"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/media"
	"github.com/nilpntr/content-fabrique/internal/sandbox"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
)

const styleUsage = `Usage:
  cfctl style analyze --workspace <wks_... or org_...> --files <a.mp4,b.mp4,...> [--append] [--no-vision] [--no-summary] [--no-store] [flags]
  cfctl style show    --workspace <wks_... or org_...> [--json] [flags]
  cfctl style set     --workspace <wks_... or org_...> [--vibe calm|lively|hype] [--transitions off|subtle|lots] [--text-animation off|on]
                      [--camera-motion off|subtle|lots] [--title-colour '#RRGGBB'] [--title-position top|upper_third|auto]
                      [--title-case upper|lower|as_typed] [--music always|when_added|never] [--pace slower|like_mine|faster]
                      [--chapter-titles off|on] [--title-template '<TOPIC> <N>'] [--ending-hold short|long] [--cut-timing full_reps|mid_rep]
                      [--angle-switches off|on] [flags]
  cfctl style clear   --workspace <wks_... or org_...> [flags]

DEVELOPMENT ONLY, like cfctl project show (refused unless CF_ENV is local, ci or bench). The creator style profile (P6-05,
docs/design/style-profile-v1.md) is what a creator's finished videos look and sound like: cuts and shot lengths, effects, the light, the
title text, the music. analyze measures the reference videos you name (local files; they are only ever an input of the sandbox, and
nothing of them is kept but numbers and the title text), reads the title style from three frames of each hook with one Haiku call
(at most $0.01 a reference, metered like every model call), aggregates the measures into the workspace's profile and prints it.
show prints the stored profile in plain language with the summary and the effective settings, with --json as the stored document.
set replaces the creator's settings (a flag left out is not set). clear forgets the whole style, as DELETE /v1/style does. Every
command goes through the code the app uses (package creatorstyle): the files are stored as references measured from files (no asset),
and the profile and its summary are made from them and the app's references together (P6-05b, docs/design/style-profile-v1.md
section 11).

  --workspace   a wks_... id or a Clerk org_... id (required)
  --files       the reference videos, comma separated (or give them as arguments)
  --append      add the files to the references measured from files before (at most 10 are used, the newest) instead of replacing them
  --no-vision   do not read the title text and write no summary (no model call, no cost)
  --no-summary  do not write the summary (one call of at most $0.03)
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
	case "set":
		return cmdStyleSet(args[1:], stdout, stderr, lookup, deps)
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
	noSummary := flags.Bool("no-summary", false, "skip the summary")
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
	var writer *styleprofile.Writer
	if !*noVision && !*noStore {
		reader, rcode := styleReader(env, runner, st, deps, errOut)
		if rcode != 0 {
			return rcode
		}
		an.Reader = reader
		if !*noSummary {
			writer = &styleprofile.Writer{Models: reader.Models, Router: reader.Router}
		}
	} else if !*noVision {
		_, _ = fmt.Fprintln(out, "note: --no-store skips the title text read (it needs the ledger); add the workspace and drop --no-store to read it")
	}

	var costBefore int64
	if st != nil {
		costBefore, _ = styleCost(ctx, st, ws)
	}
	var measures []styleprofile.Measures
	for _, f := range files {
		start := time.Now()
		res, err := an.AnalyzeFile(ctx, ws, f)
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl %s: %s: %v (nothing was stored)\n", name, filepath.Base(f), err)
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
		// The same file analysed again replaces its earlier measures instead of counting twice.
		measures = slices.DeleteFunc(measures, func(m styleprofile.Measures) bool {
			return m.Name == res.Measures.Name && m.DurationS == res.Measures.DurationS
		})
		measures = append(measures, res.Measures)
	}
	if *noStore {
		printProfile(out, styleprofile.Aggregate(measures), *asJSON, false)
		return 0
	}
	if err := storeFileReferences(ctx, st, ws, measures, *appendRefs); err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		return 1
	}
	// The profile and the summary are made as the app makes them (creatorstyle.Learn), from these files and the app's references.
	res, err := creatorstyle.Learn(ctx, creatorstyle.LearnDeps{Store: st, Writer: writer, Force: true}, ws, true)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		return 1
	}
	if after, cerr := styleCost(ctx, st, ws); cerr == nil && after > costBefore {
		_, _ = fmt.Fprintf(out, "model cost of the title reads and the summary (the growth of this workspace's ledger during the command): %s\n", usd(after-costBefore))
	}
	style, err := styleprofile.LoadStyle(ctx, st, ws)
	if err != nil || style.Profile == nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: the profile was not stored (%s): %v\n", name, res.Outcome, err)
		return 1
	}
	_, _ = fmt.Fprintf(out, "made from %d references, summary: %s\n", res.References, res.Summary)
	printStyle(out, style, *asJSON, true)
	return 0
}

// storeFileReferences stores the measures of files as references without an asset: they replace the ones measured from files before
// (append keeps those, and replaces only a file of the same name and length).
func storeFileReferences(ctx context.Context, st *store.Store, ws ids.WorkspaceID, ms []styleprofile.Measures, appendRefs bool) error {
	return st.InTx(ctx, func(tx *store.Store) error {
		if !appendRefs {
			if err := tx.DeleteFileStyleReferences(ctx, ws); err != nil {
				return err
			}
		} else {
			rows, err := tx.ListStyleReferences(ctx, ws)
			if err != nil {
				return err
			}
			for _, r := range rows {
				if _, ok := store.StyleReferenceAsset(r); ok || r.Status != store.StyleRefReady {
					continue
				}
				var old styleprofile.Measures
				if json.Unmarshal(r.Measures, &old) != nil {
					continue
				}
				if slices.ContainsFunc(ms, func(m styleprofile.Measures) bool { return m.Name == old.Name && m.DurationS == old.DurationS }) {
					if err := tx.DeleteStyleReference(ctx, ws, r.ID); err != nil {
						return err
					}
				}
			}
		}
		for _, m := range ms {
			mj, err := json.Marshal(m)
			if err != nil {
				return err
			}
			name := strings.TrimSpace(m.Name)
			if name == "" {
				name = "reference"
			}
			if _, err := tx.InsertFileStyleReference(ctx, ws, name, mj, json.RawMessage(`{"source":"cfctl"}`)); err != nil {
				return err
			}
		}
		return nil
	})
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

// printStyle prints the profile, the summary and the effective settings.
func printStyle(out io.Writer, st *styleprofile.Style, asJSON, stored bool) {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(st.Profile)
		return
	}
	if st.Profile != nil {
		printProfile(out, st.Profile, false, false)
	} else {
		_, _ = fmt.Fprintln(out, "\nNo profile: no reference video was measured.")
	}
	if st.Summary != nil {
		_, _ = fmt.Fprintf(out, "\nSummary (%s, %s): %s\n", st.Summary.PromptVersion, st.Summary.Model, st.Summary.Text)
	}
	r := st.Resolved
	_, _ = fmt.Fprintln(out, "\nSettings the editor follows (where each comes from):")
	for _, l := range []struct{ name, value, source string }{
		{"vibe", r.Vibe, r.Sources.Vibe}, {"transitions", r.Transitions, r.Sources.Transitions}, {"text animation", r.TextAnimation, r.Sources.TextAnimation},
		{"camera motion", r.CameraMotion, r.Sources.CameraMotion}, {"title colour", cmp.Or(r.TitleColour, "-"), r.Sources.TitleColour},
		{"title position", r.TitlePosition, r.Sources.TitlePosition}, {"title case", r.TitleCase, r.Sources.TitleCase}, {"music", r.Music, r.Sources.Music},
		{"pace", r.Pace, r.Sources.Pace}, {"chapter titles", r.ChapterTitles, r.Sources.ChapterTitles}, {"title form", cmp.Or(r.TitleTemplate, "-"), r.Sources.TitleTemplate},
		{"ending hold", r.EndingHold, r.Sources.EndingHold}, {"cut timing", r.CutTiming, r.Sources.CutTiming}, {"angle switches", r.AngleSwitches, r.Sources.AngleSwitches},
	} {
		_, _ = fmt.Fprintf(out, "  %-15s %-12s %s\n", l.name, l.value, cmp.Or(l.source, "default"))
	}
	if stored {
		_, _ = fmt.Fprintln(out, "\nStored as the workspace's creator style profile.")
	}
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
	svc, err := styleService(st)
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
		return 1
	}
	if remove {
		if e := svc.Reset(ctx, creatorstyle.Actor{Workspace: ws.ID}); e != nil {
			_, _ = fmt.Fprintf(env.err, "cfctl %s: %s\n", name, e.Message)
			return 1
		}
		_, _ = fmt.Fprintln(env.out, "deleted the creator style of the workspace (the profile, the references and the settings)")
		return 0
	}
	v, e := svc.Get(ctx, creatorstyle.Actor{Workspace: ws.ID})
	if e != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %s\n", name, e.Message)
		return 1
	}
	if v.Style.Profile == nil && v.Style.Mine.IsZero() {
		_, _ = fmt.Fprintln(env.out, "this workspace has no creator style profile (cfctl style analyze makes one)")
		return 0
	}
	if !*asJSON {
		_, _ = fmt.Fprintf(env.out, "Status: %s (%d of %d references measured)\n", v.Status, v.Done, v.Total)
		for _, r := range v.References {
			line := fmt.Sprintf("  - %s: %s", r.Name, r.Status)
			if r.FailureReason != "" {
				line += " (" + r.FailureReason + ")"
			}
			if r.Asset == nil {
				line += ", measured from a file"
			}
			_, _ = fmt.Fprintln(env.out, line)
		}
	}
	printStyle(env.out, v.Style, *asJSON, false)
	return 0
}

// styleService is the app's style service over the store, with a River insert client for the jobs it enqueues (removing the objects
// of the references, making the profile again).
func styleService(st *store.Store) (*creatorstyle.Service, error) {
	river, err := jobs.NewInsertClient(st.Pool(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return nil, err
	}
	return creatorstyle.NewService(st, nil, jobs.NewEnqueuer(river, nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func cmdStyleSet(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	const name = "style set"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, styleUsage) }
	workspaceArg := flags.String("workspace", "", "workspace id")
	envFileArg := flags.String("env-file", "", "env file")
	yes := flags.Bool("yes", false, "confirm outside plain local development")
	var in styleprofile.Settings
	flags.StringVar(&in.Vibe, "vibe", "", "calm, lively or hype")
	flags.StringVar(&in.Transitions, "transitions", "", "off, subtle or lots")
	flags.StringVar(&in.TextAnimation, "text-animation", "", "off or on")
	flags.StringVar(&in.CameraMotion, "camera-motion", "", "off, subtle or lots")
	flags.StringVar(&in.TitleColour, "title-colour", "", "#RRGGBB")
	flags.StringVar(&in.TitlePosition, "title-position", "", "top, upper_third or auto")
	flags.StringVar(&in.TitleCase, "title-case", "", "upper, lower or as_typed")
	flags.StringVar(&in.Music, "music", "", "always, when_added or never")
	flags.StringVar(&in.Pace, "pace", "", "slower, like_mine or faster")
	flags.StringVar(&in.ChapterTitles, "chapter-titles", "", "off or on")
	flags.StringVar(&in.TitleTemplate, "title-template", "", "a title form such as <TOPIC> <N>")
	flags.StringVar(&in.EndingHold, "ending-hold", "", "short or long")
	flags.StringVar(&in.CutTiming, "cut-timing", "", "full_reps or mid_rep")
	flags.StringVar(&in.AngleSwitches, "angle-switches", "", "off or on")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if _, err := in.Normalize(); err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
		return 2
	}
	wantWS, wantOrg, ok := parseWorkspaceRef(name, *workspaceArg, stderr)
	if !ok {
		return 2
	}
	env, code := guardDevCommand(name, *envFileArg, true, *yes, stdout, stderr, lookup)
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
	svc, err := styleService(st)
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
		return 1
	}
	v, e := svc.PutSettings(ctx, creatorstyle.Actor{Workspace: ws.ID}, in)
	if e != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %s\n", name, e.Message)
		return 1
	}
	_, _ = fmt.Fprintln(env.out, "stored the creator's settings")
	printStyle(env.out, v.Style, false, false)
	return 0
}
