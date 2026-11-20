package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/clipfeedback"
	"github.com/nilpntr/content-fabrique/internal/ids"
)

const proofUsage = `Usage:
  cfctl proof shots <prj_...> --version <n> --workspace <wks_... or org_...> [--env-file <path>]
  cfctl proof never <prj_...> --version <n> --shot <n> --workspace <...> [--whole-clip] [--note "..."] [--yes]
  cfctl proof later <prj_...> --version <n> --shot <n> --by <seconds> --workspace <...> [--note "..."] [--yes]
  cfctl proof list --workspace <...> [--asset <ast_...>]
  cfctl proof forget <cfb_...> --workspace <...> [--yes]

DEVELOPMENT ONLY. Proof edits and clip feedback (docs/design/proof-edit-v1.md), the code behind the API's shot list and
/v1/clip-feedback:

  shots    the shot list of a version: the number its proof shows at the top right of each shot, the clip and the second it
           starts at, and the feedback that already holds for that range
  never    "never use this shot again": stored against the shot's clip and the range it plays (the whole clip with --whole-clip),
           never against the number, so every later edit of the workspace honours it
  later    "start this shot a few seconds later": the part of the take before the new start (--by seconds, 0.5 to 30) is not used
           again
  list     the workspace's clip feedback, newest first
  forget   take feedback back

never, later and forget act as the workspace's first owner and are refused unless CF_ENV is local, ci or bench. A proof itself
is a preview render with proof true (POST /v1/projects/{id}/renders, or cfctl render --proof for a local spec), and "make it
final" is POST /v1/renders/{id}/final.
`

func cmdProof(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, proofUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, proofUsage)
		return 0
	case "shots", "never", "later", "list", "forget":
		return cmdProofRun(args[0], args[1:], stdout, stderr, lookup, deps)
	}
	_, _ = fmt.Fprintf(stderr, "cfctl proof: unknown command %q\n\n%s", args[0], proofUsage)
	return 2
}

func cmdProofRun(sub string, args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	name := "proof " + sub
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, proofUsage) }
	workspaceArg := flags.String("workspace", "", "workspace id")
	envFileArg := flags.String("env-file", "", "env file")
	version := flags.Int("version", 0, "timeline version")
	shot := flags.Int("shot", 0, "shot number")
	by := flags.Float64("by", 0, "seconds later")
	wholeClip := flags.Bool("whole-clip", false, "the whole clip")
	note := flags.String("note", "", "the creator's note")
	assetArg := flags.String("asset", "", "one clip")
	yes := flags.Bool("yes", false, "confirm a write outside a local database")
	var positional []string
	rest := args
	for len(rest) > 0 {
		if err := flags.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if flags.NArg() == 0 {
			break
		}
		positional = append(positional, flags.Arg(0))
		rest = flags.Args()[1:]
	}
	wantArgs := map[string]int{"shots": 1, "never": 1, "later": 1, "list": 0, "forget": 1}[sub]
	if len(positional) != wantArgs {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: give %d id(s); see cfctl proof help\n", name, wantArgs)
		return 2
	}
	var pid ids.ProjectID
	if sub == "shots" || sub == "never" || sub == "later" {
		var err error
		if pid, err = ids.ParseProjectID(positional[0]); err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: %q is not a project id like prj_01...\n", name, positional[0])
			return 2
		}
		if *version < 1 {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: --version is required (the timeline version, from 1)\n", name)
			return 2
		}
		if sub != "shots" && *shot < 1 {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: --shot is required (the number the proof shows)\n", name)
			return 2
		}
	}
	if sub == "later" && (*by < float64(clipfeedback.MinOffsetMs)/1000 || *by > float64(clipfeedback.MaxOffsetMs)/1000) {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --by is how many seconds later the take should start, 0.5 to 30\n", name)
		return 2
	}
	wantWS, wantOrg, ok := parseWorkspaceRef(name, *workspaceArg, stderr)
	if !ok {
		return 2
	}
	mutates := sub == "never" || sub == "later" || sub == "forget"
	env, code := guardDevCommand(name, *envFileArg, mutates, *yes, stdout, stderr, lookup)
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
	svc, err := clipfeedback.NewService(st, nil)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		return 1
	}
	refused := func(e *clipfeedback.Error) int {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %s: %s\n", name, e.Code, e.Message)
		if e.Status >= 500 {
			return 1
		}
		return 2
	}

	switch sub {
	case "shots":
		list, e := svc.ShotList(ctx, ws.ID, pid, *version)
		if e != nil {
			return refused(e)
		}
		_, _ = fmt.Fprintf(out, "Shot list of version %d of %s (the numbers its proof shows at the top right):\n", list.Version, list.Project)
		for _, s := range list.Shots {
			title := s.Title
			if !s.HasTitle {
				title = "(no longer in the project)"
			}
			_, _ = fmt.Fprintf(out, "  %2d  %s  %s  from %s to %s of the clip (on screen %s to %s)\n", s.Number, s.AssetID, title,
				clipfeedback.Secs(s.Source.InMs), clipfeedback.Secs(s.Source.OutMs), clipfeedback.Secs(s.AtMs), clipfeedback.Secs(s.EndMs))
			for _, f := range s.Feedback {
				_, _ = fmt.Fprintf(out, "        feedback %s: %s\n", f.ID, clipfeedback.RuleOf(f).Plain())
			}
		}
		return 0
	case "never", "later":
		owner, _, code := workspaceOwner(ctx, st, ws, name, errOut)
		if code != 0 {
			return code
		}
		in := clipfeedback.Give{Project: pid, Version: *version, ShotNumber: *shot, Kind: clipfeedback.NeverUse, WholeClip: *wholeClip, Note: *note}
		if sub == "later" {
			in.Kind, in.OffsetMs = clipfeedback.StartLater, int64(*by*1000+0.5)
		}
		row, e := svc.Give(ctx, clipfeedback.Giver{Workspace: ws.ID, User: owner.User}, in)
		if e != nil {
			return refused(e)
		}
		_, _ = fmt.Fprintf(out, "Stored %s on clip %s: %s. Every later edit of the workspace honours it.\n", row.ID, row.AssetID, clipfeedback.RuleOf(row).Plain())
		return 0
	case "list":
		var asset *ids.AssetID
		if *assetArg != "" {
			aid, err := ids.ParseAssetID(*assetArg)
			if err != nil {
				_, _ = fmt.Fprintf(errOut, "cfctl %s: %q is not an asset id like ast_01...\n", name, *assetArg)
				return 2
			}
			asset = &aid
		}
		rows, e := svc.List(ctx, ws.ID, asset, clipfeedback.MaxPageSize)
		if e != nil {
			return refused(e)
		}
		if len(rows) == 0 {
			_, _ = fmt.Fprintln(out, "No clip feedback.")
			return 0
		}
		for _, r := range rows {
			line := fmt.Sprintf("%s  %s  %s  (shot %d of version %d of %s, %s)", r.ID, r.AssetID, clipfeedback.RuleOf(r).Plain(), r.ShotNumber,
				r.TimelineVersion, r.ProjectID, r.CreatedAt.UTC().Format(time.DateOnly))
			if r.Note != "" {
				line += "  note: " + strings.ReplaceAll(r.Note, "\n", " ")
			}
			_, _ = fmt.Fprintln(out, line)
		}
		return 0
	default: // forget
		id, err := ids.ParseClipFeedbackID(positional[0])
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl %s: %q is not a clip feedback id like cfb_01...\n", name, positional[0])
			return 2
		}
		if e := svc.Delete(ctx, ws.ID, id); e != nil {
			return refused(e)
		}
		_, _ = fmt.Fprintf(out, "Took %s back: the clip may be used there again.\n", id)
		return 0
	}
}
