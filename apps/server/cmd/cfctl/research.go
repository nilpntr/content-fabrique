package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/research"
)

const researchUsage = `Usage:
  cfctl research sources [--env-file <path>]
  cfctl research forget <handle> [--platform tiktok|instagram|youtube] [--undo] [--yes] [--allow-production] [--env-file <path>]
  cfctl research sweep [--yes] [--allow-production] [--env-file <path>]
  cfctl research template > watch-sheet.csv
  cfctl research import <sheet.csv|sheet.json> [--observer <name>] [--dry-run] [--replay <dir> | --record <dir> | --no-resolve]
                        [--yes] [--allow-production] [--env-file <path>]
  cfctl research add-post <post link> [--observer <name>] [--handle h] [--watched d] [--posted d] [--length 0:42] [--format id]
                        [--hook-kind k] [--hook-text "<TOPIC> 101"] [--caption-pattern p] [--cta c] [--topic facet:value] [--cuts n]
                        [--first-cut s] [--views n] [--likes n] [--comments n] [--saves n] [--shares n] [--notes "..."]
                        [--dry-run] [--replay <dir> | --record <dir> | --no-resolve] [--yes] [--allow-production] [--env-file <path>]

The creator research engine (docs/05-decisions.md D-41) keeps links, metadata, short notes and our own derived numbers about other
people's PUBLIC posts. Never a video file. Handles and public numbers are personal data, so the engine can forget a person and deletes
everything on a timer.

  sources   print the allow-list of sources (each with the methods, metrics and platforms it may use) and the retention in force
            (RESEARCH_RETENTION_DAYS, default 365; RESEARCH_YOUTUBE_RETENTION_DAYS, default and maximum 30). No database.
  forget    the takedown: delete the creator with this handle (on one platform, or on every platform without --platform), their posts,
            annotations and evidence, and the findings that rested on that evidence, and keep a tombstone (the platform and a hash of the
            handle, nothing else) so the creator is never ingested again, even if we held no data on them. The data cannot be brought
            back. Run it on request, in any environment: production needs --allow-production and --yes.
  sweep     delete now what the daily research_sweep job would delete: observations past their retain_until, the findings, posts and idle
            candidate creators left without evidence. Safe to run twice.
  template  print the empty watch sheet with an explanation of every column (internal/research/watch-sheet.template.csv).
  import    import a watch sheet: what a PERSON saw while watching public posts (the counts, the hook, the form, a short note in their
            own words). Every row is checked first (post links only, no media anywhere, format ids and tag values that exist, title
            forms, never the creator's words) and nothing is stored when one row is wrong. Each TikTok link is then resolved through
            TikTok's public oEmbed endpoint (caption, author, thumbnail link; metadata only, never a video or a picture) at one request
            a second at most, and its post time is read from its id and checked against the date the sheet shows. A forgotten creator
            is refused before any call; a 429 from TikTok stops the import. Importing the same sheet twice stores nothing twice.
  add-post  the same for one post, from flags.

  --observer          who watched, for rows without an observer column
  --dry-run           check the rows (and the shown dates against the TikTok ids) only: no database, no network
  --replay <dir>      resolve TikTok links from recorded oEmbed fixtures only; no network call. A missing fixture fails the row
  --record <dir>      call oEmbed and record each answer (metadata only) as a fixture in the directory; fixtures already there are
                      used instead of a call (the cache)
  --no-resolve        no oEmbed call; the post time still comes from the TikTok id

  --platform          limit forget to one platform
  --undo              with forget: remove the tombstone, so the creator may be ingested again (no data comes back)
  --yes               needed unless CF_ENV is local or ci and the database is on this machine
  --allow-production  needed in addition to --yes when CF_ENV=production
  --env-file          the env file; default .env at the repository root
`

func cmdResearch(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, researchUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, researchUsage)
		return 0
	case "sources":
		return cmdResearchSources(args[1:], stdout, stderr, lookup)
	case "forget":
		return cmdResearchForget(args[1:], stdout, stderr, lookup, deps)
	case "sweep":
		return cmdResearchSweep(args[1:], stdout, stderr, lookup, deps)
	case "template":
		return cmdResearchTemplate(args[1:], stdout, stderr)
	case "import":
		return cmdResearchImport(args[1:], stdout, stderr, lookup, deps)
	case "add-post":
		return cmdResearchAddPost(args[1:], stdout, stderr, lookup, deps)
	}
	_, _ = fmt.Fprintf(stderr, "cfctl research: unknown command %q\n\n%s", args[0], researchUsage)
	return 2
}

func cmdResearchSources(args []string, stdout, stderr io.Writer, lookup lookupEnv) int {
	const name = "research sources"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, researchUsage) }
	envFileArg := flags.String("env-file", "", "env file")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	// The same env file, with the same precedence, as `sweep`: the retention printed is the retention the sweep uses.
	env, code := loadAgentEnv(name, *envFileArg, stdout, stderr, lookup)
	if code != 0 {
		return code
	}
	ret, err := research.LoadRetention(env.get)
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
		return 2
	}
	stdout = env.out
	_, _ = fmt.Fprintln(stdout, "Allowed sources (a source that is not listed cannot write evidence):")
	for _, s := range research.Sources() {
		methods := make([]string, 0, 2)
		for _, m := range s.Methods() {
			methods = append(methods, string(m))
		}
		platforms := make([]string, 0, 3)
		for _, p := range s.Platforms() {
			platforms = append(platforms, string(p))
		}
		metrics := strings.Join(s.Metrics(), ", ")
		if metrics == "" {
			metrics = "none"
		}
		_, _ = fmt.Fprintf(stdout, "  %-22s method %-10s platforms %-24s metrics %s; kept %d days\n",
			s, strings.Join(methods, "|"), strings.Join(platforms, ","), metrics, int(ret.For(s)/research.Day))
	}
	return 0
}

func cmdResearchForget(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	const name = "research forget"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, researchUsage) }
	platformArg := flags.String("platform", "", "limit to one platform")
	envFileArg := flags.String("env-file", "", "env file")
	yes := flags.Bool("yes", false, "confirm outside plain local development")
	allowProd := flags.Bool("allow-production", false, "allow CF_ENV=production")
	undo := flags.Bool("undo", false, "remove the tombstone")
	var handle string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		handle, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if handle == "" && flags.NArg() == 1 {
		handle = flags.Arg(0)
	} else if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: give exactly one handle\n", name)
		return 2
	}
	if handle == "" {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: name the creator's handle\n", name)
		return 2
	}
	h, err := research.NormalizeHandle(handle)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
		return 2
	}
	var platform research.Platform
	if *platformArg != "" {
		if platform, err = research.ParsePlatform(*platformArg); err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
			return 2
		}
	}
	env, code := loadAgentEnv(name, *envFileArg, stdout, stderr, lookup)
	if code != 0 {
		return code
	}
	if msg := mutationRefusal(name, "it deletes a creator and all their research data, for good", "DATABASE_URL", env.vars["CF_ENV"],
		sourceOf("CF_ENV", env.file, lookup), sourceOf("DATABASE_URL", env.file, lookup), env.vars["DATABASE_URL"], *yes, *allowProd); msg != "" {
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
	if *undo {
		n, err := st.UnforgetResearchCreator(ctx, platform, h)
		if err != nil {
			_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
			return 1
		}
		_, _ = fmt.Fprintf(env.out, "removed %d tombstone(s) for %q; no data was brought back\n", n, h)
		return 0
	}
	res, err := st.ForgetResearchCreator(ctx, platform, h)
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
		return 1
	}
	_, _ = fmt.Fprintf(env.out, "forgot %q: %d creator(s), %d post(s), %d annotation(s), %d observation(s), %d finding(s); the handle is now blocked from ingestion\n",
		h, res.Creators, res.Posts, res.Annotations, res.Evidence, res.Findings)
	return 0
}

func cmdResearchSweep(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	const name = "research sweep"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, researchUsage) }
	envFileArg := flags.String("env-file", "", "env file")
	yes := flags.Bool("yes", false, "confirm outside plain local development")
	allowProd := flags.Bool("allow-production", false, "allow CF_ENV=production")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: takes no arguments\n", name)
		return 2
	}
	env, code := loadAgentEnv(name, *envFileArg, stdout, stderr, lookup)
	if code != 0 {
		return code
	}
	ret, err := research.LoadRetention(env.get)
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
		return 2
	}
	if msg := mutationRefusal(name, "it deletes research data past its retention", "DATABASE_URL", env.vars["CF_ENV"],
		sourceOf("CF_ENV", env.file, lookup), sourceOf("DATABASE_URL", env.file, lookup), env.vars["DATABASE_URL"], *yes, *allowProd); msg != "" {
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
	res, err := st.SweepResearch(ctx, time.Now(), ret)
	if err != nil {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %v\n", name, err)
		return 1
	}
	_, _ = fmt.Fprintf(env.out, "swept: %d observation(s), %d finding(s), %d post(s), %d idle creator(s)\n", res.Evidence, res.Findings, res.Posts, res.Creators)
	return 0
}
