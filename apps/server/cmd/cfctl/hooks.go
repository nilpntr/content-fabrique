package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/agents/editor"
	"github.com/nilpntr/content-fabrique/internal/envfile"
	"github.com/nilpntr/content-fabrique/internal/hooklab"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/projects"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/styleprofile"
)

const hooksUsage = `Usage:
  cfctl hooks lab  --workspace <wks_... or org_...> --project <prj_...> [--version N] [--playbook <findings.yaml>] [--json] [flags]
  cfctl hooks eval [--suite <file.yaml>] [--json]

The hook lab (docs/design/hook-lab.md) scores the ways a video can open with a fixed rubric: the hook lands in the first two seconds,
the text is short enough to read and off the person's face and body, the first frame is live picture and not soft, and a spoken line
starts on the first word of a sentence that is not a filler. Everything is computed in Go from stored data: no model is called and
nothing is written, so both commands cost nothing and run the same in LLM_MODE=replay.

lab (DEVELOPMENT ONLY, like cfctl eval editor) runs it on a project: the opening of a stored timeline version (variant "current",
with its real text) next to up to three openings the lab proposes from the footage (the best spoken line, the best moment of the
picture, the clip the plan opens on), ranked best first, with the criteria each one fails.

What the clips say counts unless the plan of the run that made the version is of a kind that does not follow speech (a workout cut
to a song: the clips are not heard).

  --workspace   a wks_... id or a Clerk org_... id (required)
  --project     the project (prj_...) (required)
  --version     the timeline version whose opening is scored; default the current one (none when there is no timeline yet)
  --playbook    a findings file of the research playbook; its medium and high hook findings scale the weights (default: none)
  --json        print the lab as JSON (the record plan_edit stores as the run's hook_lab artifact)
  --env-file    the env file; default .env at the repository root

eval runs the lab's eval cases: made-up openings with the answer the lab must give. It needs no database and no env file.

  --suite       the case file; default suites/hook-lab.yaml at the repository root
  --json        print every case's result as JSON
`

func cmdHooks(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, hooksUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, hooksUsage)
		return 0
	case "lab":
		return cmdHooksLab(args[1:], stdout, stderr, lookup, deps)
	case "eval":
		return cmdHooksEval(args[1:], stdout, stderr)
	}
	_, _ = fmt.Fprintf(stderr, "cfctl hooks: unknown command %q\n\n%s", args[0], hooksUsage)
	return 2
}

func cmdHooksLab(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	const name = "hooks lab"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, hooksUsage) }
	workspaceArg := flags.String("workspace", "", "workspace id")
	projectArg := flags.String("project", "", "project id")
	version := flags.Int("version", 0, "timeline version")
	playbookArg := flags.String("playbook", "", "research playbook findings file")
	asJSON := flags.Bool("json", false, "print JSON")
	envFileArg := flags.String("env-file", "", "env file")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: unexpected argument %q\n", name, flags.Arg(0))
		return 2
	}
	pid, err := ids.ParseProjectID(strings.TrimSpace(*projectArg))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --project <prj_...> is required\n", name)
		return 2
	}
	if *version < 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --version is a number from 1\n", name)
		return 2
	}
	wantWS, wantOrg, ok := parseWorkspaceRef(name, *workspaceArg, stderr)
	if !ok {
		return 2
	}
	var playbook hooklab.Playbook
	if *playbookArg != "" {
		p, err := hooklab.LoadPlaybook(*playbookArg)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
			return 2
		}
		playbook = p
	}
	env, code := guardDevCommand(name, *envFileArg, false, false, stdout, stderr, lookup)
	if code != 0 {
		return code
	}
	out, errOut := env.out, env.err
	ctx, stop := signalContext()
	defer stop()
	st, code := env.openDB(ctx, name, deps)
	if code != 0 {
		return code
	}
	defer st.Close()
	ws, code := findWorkspace(ctx, st, name, wantWS, wantOrg, errOut)
	if code != 0 {
		return code
	}
	svc, err := projects.NewService(st, nil)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		return 1
	}
	style, err := styleprofile.LoadStyle(ctx, st, ws.ID)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: the style profile: %v\n", name, err)
		return 1
	}
	// The plan of the run that made the version names the clip it meant to open on (that clip is proposed too) and the kind of video.
	opt := editor.HookLabOptions{Version: *version, Style: style, Playbook: playbook, PlanOf: func(ctx context.Context, v store.TimelineVersion) *editor.Plan {
		plan, _ := planOfVersion(ctx, st, ws.ID, v)
		return plan
	}}
	rec, err := editor.HookLabForProject(ctx, svc, st, ws.ID, pid, opt)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rec); err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
			return 1
		}
		return 0
	}
	if len(rec.Ranked) == 0 {
		_, _ = fmt.Fprintf(out, "project %s: the footage holds no opening to score (no video clip with a length)\n", pid)
		return 0
	}
	if rec.Version > 0 {
		_, _ = fmt.Fprintf(out, "project %s, version %d\n", pid, rec.Version)
	} else {
		_, _ = fmt.Fprintf(out, "project %s, no timeline yet: the proposals only\n", pid)
	}
	facts, lines := editor.HookLabText(rec.Lab)
	_, _ = fmt.Fprint(out, facts)
	_, _ = fmt.Fprintln(out, "(a proposal has no text yet: it is judged on the room for a title, the edit as it is on its real text)")
	if lines != "" {
		_, _ = fmt.Fprintf(out, "The lines they open on:\n%s\n", lines)
	}
	_, _ = fmt.Fprintf(out, "pick: variant %s", rec.Pick)
	if rec.Close() {
		_, _ = fmt.Fprintf(out, " (within %.2f of the next one: as good as each other)", hooklab.CloseMargin)
	}
	_, _ = fmt.Fprintln(out)
	return 0
}

func cmdHooksEval(args []string, stdout, stderr io.Writer) int {
	const name = "hooks eval"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, hooksUsage) }
	suiteArg := flags.String("suite", "", "case file")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: unexpected argument %q\n", name, flags.Arg(0))
		return 2
	}
	path := *suiteArg
	if path == "" {
		wd, err := os.Getwd()
		if err == nil {
			wd, err = envfile.FindRoot(wd)
		}
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: the repository root was not found (%v); name the case file with --suite\n", name, err)
			return 2
		}
		path = filepath.Join(wd, "suites", "hook-lab.yaml")
	}
	suite, err := hooklab.LoadCases(path)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
		return 2
	}
	results := hooklab.RunCases(suite)
	failed := 0
	for _, r := range results {
		if !r.Passed {
			failed++
		}
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{"suite": suite.Name, "cases": len(results), "failed": failed, "results": results}); err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
			return 1
		}
	} else {
		for _, r := range results {
			if r.Passed {
				_, _ = fmt.Fprintf(stdout, "PASS %s: pick %s\n", r.ID, pickOr(r.Lab.Pick))
				continue
			}
			_, _ = fmt.Fprintf(stdout, "FAIL %s: %s\n", r.ID, strings.Join(r.Problems, "; "))
		}
		_, _ = fmt.Fprintf(stdout, "%s: %d of %d cases pass\n", suite.Name, len(results)-failed, len(results))
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func pickOr(pick string) string {
	if pick == "" {
		return "none"
	}
	return pick
}
