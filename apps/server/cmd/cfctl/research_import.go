package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/nilpntr/content-fabrique/internal/research"
	"github.com/nilpntr/content-fabrique/internal/research/ingest"
	"github.com/nilpntr/content-fabrique/internal/research/oembed"
)

// The watch sheet commands (phase R2): import a sheet, add one post, print the template. See researchUsage.

// ingestFlags are the flags import and add-post share.
type ingestFlags struct {
	observer, replay, record, envFile *string
	dryRun, noResolve, yes, allowProd *bool
}

func addIngestFlags(flags *flag.FlagSet) ingestFlags {
	return ingestFlags{
		observer:  flags.String("observer", "", "who watched (for rows without an observer)"),
		replay:    flags.String("replay", "", "resolve TikTok links from recorded oEmbed fixtures only (no network)"),
		record:    flags.String("record", "", "record each oEmbed answer as a fixture in this directory (and reuse the ones there)"),
		envFile:   flags.String("env-file", "", "env file"),
		dryRun:    flags.Bool("dry-run", false, "check the rows only: no database, no network"),
		noResolve: flags.Bool("no-resolve", false, "do not call oEmbed"),
		yes:       flags.Bool("yes", false, "confirm outside plain local development"),
		allowProd: flags.Bool("allow-production", false, "allow CF_ENV=production"),
	}
}

// splitPositional takes the one positional argument, anywhere among the flags (flags before and after it are both read). It returns -1
// for --help.
func splitPositional(name string, flags *flag.FlagSet, args []string, stderr io.Writer, what string) (string, int) {
	var pos []string
	for {
		if err := flags.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return "", -1
			}
			return "", 2
		}
		if flags.NArg() == 0 {
			break
		}
		pos, args = append(pos, flags.Arg(0)), flags.Args()[1:]
	}
	switch len(pos) {
	case 0:
		_, _ = fmt.Fprintf(stderr, "cfctl %s: name the %s\n", name, what)
		return "", 2
	case 1:
		return pos[0], 0
	}
	_, _ = fmt.Fprintf(stderr, "cfctl %s: give exactly one %s\n", name, what)
	return "", 2
}

func cmdResearchImport(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	const name = "research import"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, researchUsage) }
	f := addIngestFlags(flags)
	file, code := splitPositional(name, flags, args, stderr, "sheet file (.csv or .json)")
	if code != 0 {
		return max(code, 0)
	}
	info, err := os.Stat(file) //nolint:gosec // the operator names the file
	if err != nil || !info.Mode().IsRegular() {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: cannot read %s\n", name, file)
		return 2
	}
	if info.Size() > research.MaxSheetBytes {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: %s is over %d bytes; split it\n", name, file, research.MaxSheetBytes)
		return 2
	}
	data, err := os.ReadFile(file) //nolint:gosec // the operator names the file
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
		return 2
	}
	return runResearchIngest(name, filepath.Base(file), data, f, stdout, stderr, lookup, deps)
}

// addPostColumns maps the add-post flags to the sheet's columns.
var addPostColumns = []struct{ flag, column, usage string }{
	{"handle", "handle", "the creator's handle (needed for Instagram and YouTube links)"},
	{"watched", "watched_at", "when you watched: 2026-11-20 or an RFC 3339 time"},
	{"posted", "posted_at", "the date the post shows: 2026-11-02"},
	{"length", "length", "the video length: 42, 42s or 0:42"},
	{"format", "format", "a format id of config/formats"},
	{"hook-kind", "hook_kind", "what the first second does: set_first, title_first, ..."},
	{"hook-text", "hook_text", "the title FORM in capitals and slots: <TOPIC> 101"},
	{"caption-pattern", "caption_pattern", "single_title, story_captions, dialogue or none"},
	{"cta", "cta", "save, follow, comment or none"},
	{"topic", "topic", "a config/tags value as facet:value"},
	{"cuts", "cuts", "about how many cuts"},
	{"first-cut", "first_cut_s", "the second of the first cut"},
	{"views", "views", "views as shown (1.2K)"},
	{"likes", "likes", "likes as shown"},
	{"comments", "comments", "comments as shown"},
	{"saves", "saves", "saves as shown"},
	{"shares", "shares", "shares as shown"},
	{"notes", "notes", "a short note in your own words (no links)"},
}

func cmdResearchAddPost(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	const name = "research add-post"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, researchUsage) }
	f := addIngestFlags(flags)
	values := map[string]*string{}
	for _, c := range addPostColumns {
		values[c.column] = flags.String(c.flag, "", c.usage)
	}
	link, code := splitPositional(name, flags, args, stderr, "post link")
	if code != 0 {
		return max(code, 0)
	}
	row := map[string]string{"url": link}
	for col, v := range values {
		if *v != "" {
			row[col] = *v
		}
	}
	data, err := json.Marshal([]map[string]string{row})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
		return 2
	}
	return runResearchIngest(name, "add-post.json", data, f, stdout, stderr, lookup, deps)
}

func runResearchIngest(name, sheetName string, data []byte, f ingestFlags, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	if *f.replay != "" && *f.record != "" {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --replay and --record exclude each other\n", name)
		return 2
	}
	if *f.noResolve && (*f.replay != "" || *f.record != "") {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --no-resolve takes no fixture directory\n", name)
		return 2
	}
	rows, err := research.ParseWatchSheet(sheetName, data, research.SheetOptions{Observer: *f.observer})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
		return 2
	}
	if *f.dryRun {
		return researchDryRun(rows, stdout)
	}
	env, code := loadAgentEnv(name, *f.envFile, stdout, stderr, lookup)
	if code != 0 {
		return code
	}
	ret, err := research.LoadRetention(env.get)
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
		return 2
	}
	var resolver ingest.Resolver
	if !*f.noResolve {
		cfg, err := oembed.LoadConfig(env.get)
		if err != nil {
			_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
			return 2
		}
		switch {
		case *f.replay != "":
			cfg.Mode, cfg.Dir = oembed.Replay, *f.replay
		case *f.record != "":
			cfg.Dir = *f.record
		}
		client, err := oembed.New(cfg)
		if err != nil {
			_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
			return 2
		}
		resolver = client
	}
	if msg := mutationRefusal(name, "it writes research data about third-party public posts", "DATABASE_URL", env.vars["CF_ENV"],
		sourceOf("CF_ENV", env.file, lookup), sourceOf("DATABASE_URL", env.file, lookup), env.vars["DATABASE_URL"], *f.yes, *f.allowProd); msg != "" {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %s (env file %s)\n", name, msg, displayPath(env.root, env.path))
		return 2
	}
	ctx, stop := signalContext()
	defer stop()
	st, code := env.openDB(ctx, name, deps)
	if code != 0 {
		return code
	}
	defer st.Close()
	rep, err := ingest.Import(ctx, ingest.FromStore(st), rows, ingest.Options{Resolver: resolver, Retention: ret})
	printIngestReport(env.out, rep)
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: stopped: %v\n", name, err)
		return 1
	}
	if len(rep.Failed) > 0 {
		return 1
	}
	return 0
}

func printIngestReport(w io.Writer, rep ingest.Report) {
	_, _ = fmt.Fprintf(w, "imported %d of %d row(s): %d new creator(s), %d resolved by oEmbed, %d observation(s) stored, %d already stored, %d annotation(s)\n",
		rep.Imported, rep.Rows, rep.NewCreators, rep.Resolved, rep.Observations, rep.Duplicates, rep.Annotations)
	if rep.TimeChecks > 0 {
		_, _ = fmt.Fprintf(w, "post times: %d of %d shown date(s) agree with the time in the post id\n", rep.TimeMatches, rep.TimeChecks)
	}
	for _, warn := range rep.Warnings {
		_, _ = fmt.Fprintf(w, "  warning: %s\n", warn)
	}
	for _, f := range rep.Failed {
		_, _ = fmt.Fprintf(w, "  not imported: %v\n", f)
	}
}

// researchDryRun prints what an import would store, and checks the shown dates against the TikTok ids (no network, no database).
func researchDryRun(rows []research.SheetRow, stdout io.Writer) int {
	creators := map[string]bool{}
	checks, matches := 0, 0
	for _, r := range rows {
		creators[string(r.Post.Platform)+":"+r.Handle] = true
		checked, matched, warning := r.TimeCheck()
		if !checked {
			continue
		}
		checks++
		if matched {
			matches++
		} else {
			_, _ = fmt.Fprintf(stdout, "  warning: %s\n", warning)
		}
	}
	_, _ = fmt.Fprintf(stdout, "%d row(s) checked: %d post(s) by %d creator(s); nothing stored, no network call\n", len(rows), len(rows), len(creators))
	if checks > 0 {
		_, _ = fmt.Fprintf(stdout, "post times: %d of %d shown date(s) agree with the time in the post id\n", matches, checks)
	}
	return 0
}

func cmdResearchTemplate(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		_, _ = fmt.Fprintln(stderr, "cfctl research template: takes no arguments (redirect the output to a file)")
		return 2
	}
	_, _ = stdout.Write(research.WatchSheetTemplate())
	return 0
}
