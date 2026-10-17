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
	"slices"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/projects"
	"github.com/nilpntr/content-fabrique/internal/store"
)

const projectUsage = `Usage:
  cfctl project create --workspace <wks_... or org_...> --format 9:16|1:1|4:5|16:9 [--title "..."]
                       [--brief "..." | --brief-file <path>] [--assets ast_...,ast_... | --all-ready] [flags]
  cfctl project show <prj_...> --workspace <wks_... or org_...> [flags]

DEVELOPMENT ONLY. create makes a project through projects.Service.Create, the code behind POST /v1/projects, acting as the
workspace's first owner (it is refused when there is none). It prints the project, its footage and the commands that run the
editor on it. show prints a project: its footage, every timeline version with its summary, and the latest render when the
database has a renders table. Both are refused unless CF_ENV is local, ci or bench, like cfctl assets import.

  --workspace   a wks_... id or a Clerk org_... id (required)
  --format      create: the output format (required)
  --title       create: the project's title; default "Untitled project"
  --brief       create: the creator's instructions (at most 4,000 characters)
  --brief-file  create: read the brief from a file instead (not together with --brief)
  --assets      create: the footage, ready video or audio asset ids separated by commas, in order
  --all-ready   create: use every ready video asset of the workspace, oldest first (at most 50; not together with --assets)
  --env-file    the env file; default .env at the repository root
  --yes         create: needed unless CF_ENV is local or ci, the database is on this machine, and CF_ENV and DATABASE_URL
                come from the same place
`

// maxBriefFileBytes caps --brief-file; the API takes 4,000 characters and a character is at most 4 bytes.
const maxBriefFileBytes = 64 << 10

func cmdProject(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, projectUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, projectUsage)
		return 0
	case "create":
		return cmdProjectCreate(args[1:], stdout, stderr, lookup, deps)
	case "show":
		return cmdProjectShow(args[1:], stdout, stderr, lookup, deps)
	}
	_, _ = fmt.Fprintf(stderr, "cfctl project: unknown command %q\n\n%s", args[0], projectUsage)
	return 2
}

// parseWorkspaceRef reads --workspace: a wks_ id (ws) or a Clerk org_ id (org).
func parseWorkspaceRef(name, v string, stderr io.Writer) (ws ids.WorkspaceID, org string, ok bool) {
	v = strings.TrimSpace(v)
	switch {
	case v == "":
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --workspace is required (a wks_... id or a Clerk org_... id)\n", name)
	case strings.HasPrefix(v, "org_"):
		return ids.WorkspaceID{}, v, true
	default:
		id, err := ids.ParseWorkspaceID(v)
		if err == nil {
			return id, "", true
		}
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --workspace must be a workspace id like wks_01... or a Clerk organization id like org_...\n", name)
	}
	return ids.WorkspaceID{}, "", false
}

// findWorkspace resolves the workspace named by --workspace. The int is an exit code, 0 on success.
func findWorkspace(ctx context.Context, st *store.Store, name string, wantWS ids.WorkspaceID, wantOrg string, errOut io.Writer) (store.Workspace, int) {
	var (
		ws  store.Workspace
		err error
	)
	if wantOrg != "" {
		ws, err = st.GetWorkspaceByClerkOrgID(ctx, wantOrg)
	} else {
		ws, err = st.GetWorkspace(ctx, wantWS)
	}
	if errors.Is(err, store.ErrNotFound) || (err == nil && ws.DeletedAt != nil) {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: no such workspace. Workspaces exist once someone has signed in to the app\n", name)
		return store.Workspace{}, 1
	}
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: looking up the workspace failed: %v\n", name, err)
		return store.Workspace{}, 1
	}
	return ws, 0
}

// guardDevCommand is the shared start of project create and show: the environment and the development-only check, and for
// a command that writes (mutates) the same CF_ENV / DATABASE_URL source guard as cfctl assets import.
func guardDevCommand(name, envFileArg string, mutates, yes bool, stdout, stderr io.Writer, lookup lookupEnv) (*agentEnv, int) {
	env, code := loadAgentEnv(name, envFileArg, stdout, stderr, lookup)
	if code != 0 {
		return nil, code
	}
	cfEnv := env.vars["CF_ENV"]
	if msg := importEnvRefusal(cfEnv); msg != "" {
		_, _ = fmt.Fprintf(env.err, "cfctl %s: %s (env file %s)\n", name, msg, displayPath(env.root, env.path))
		return nil, 2
	}
	if mutates {
		dsn := env.vars["DATABASE_URL"]
		if dsn == "" {
			_, _ = fmt.Fprintf(env.err, "cfctl %s: DATABASE_URL is not set in %s or the environment\n", name, displayPath(env.root, env.path))
			return nil, 2
		}
		if msg := mutationRefusal(name, "it writes a project into a workspace", "DATABASE_URL", cfEnv,
			sourceOf("CF_ENV", env.file, lookup), sourceOf("DATABASE_URL", env.file, lookup), dsn, yes, false); msg != "" {
			_, _ = fmt.Fprintf(env.err, "cfctl %s: %s (env file %s)\n", name, msg, displayPath(env.root, env.path))
			return nil, 2
		}
	}
	return env, 0
}

func cmdProjectCreate(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	const name = "project create"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, projectUsage) }
	workspaceArg := flags.String("workspace", "", "workspace id")
	formatArg := flags.String("format", "", "output format")
	title := flags.String("title", "", "title")
	brief := flags.String("brief", "", "brief")
	briefFile := flags.String("brief-file", "", "file with the brief")
	assetsArg := flags.String("assets", "", "asset ids")
	allReady := flags.Bool("all-ready", false, "every ready video asset")
	envFileArg := flags.String("env-file", "", "env file")
	yes := flags.Bool("yes", false, "confirm outside plain local development")
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
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })

	// Arguments first, so a typo never needs a database.
	wantWS, wantOrg, ok := parseWorkspaceRef(name, *workspaceArg, stderr)
	if !ok {
		return 2
	}
	format := strings.TrimSpace(*formatArg)
	if !slices.Contains([]string{store.Format9x16, store.Format1x1, store.Format4x5, store.Format16x9}, format) {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: --format must be one of 9:16, 1:1, 4:5, 16:9\n", name)
		return 2
	}
	switch {
	case *allReady && set["assets"]:
		_, _ = fmt.Fprintf(stderr, "cfctl %s: use --assets or --all-ready, not both\n", name)
		return 2
	case !*allReady && strings.TrimSpace(*assetsArg) == "":
		_, _ = fmt.Fprintf(stderr, "cfctl %s: name the footage with --assets ast_...,ast_... or --all-ready\n", name)
		return 2
	case set["brief"] && set["brief-file"]:
		_, _ = fmt.Fprintf(stderr, "cfctl %s: use --brief or --brief-file, not both\n", name)
		return 2
	}
	var assetIDs []string
	if !*allReady {
		for _, raw := range strings.Split(*assetsArg, ",") {
			raw = strings.TrimSpace(raw)
			if _, err := ids.ParseAssetID(raw); err != nil {
				_, _ = fmt.Fprintf(stderr, "cfctl %s: --assets holds %q, which is not an asset id like ast_01...\n", name, raw)
				return 2
			}
			assetIDs = append(assetIDs, raw)
		}
	}
	briefText := *brief
	if set["brief-file"] {
		b, err := readBriefFile(*briefFile)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl %s: %v\n", name, err)
			return 2
		}
		briefText = b
	}

	env, code := guardDevCommand(name, *envFileArg, true, *yes, stdout, stderr, lookup)
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
	svc, err := projects.NewService(st, nil)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %v\n", name, err)
		return 1
	}

	ws, code := findWorkspace(ctx, st, name, wantWS, wantOrg, errOut)
	if code != 0 {
		return code
	}
	who, owner, code := workspaceOwner(ctx, st, ws, name, errOut)
	if code != 0 {
		return code
	}
	if *allReady {
		list, err := readyVideoAssets(ctx, st, ws.ID)
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "cfctl %s: listing the assets failed: %v\n", name, err)
			return 1
		}
		if len(list) == 0 {
			_, _ = fmt.Fprintf(errOut, "cfctl %s: workspace %s has no ready video assets. Import some with cfctl assets import --wait\n", name, ws.ID)
			return 1
		}
		if len(list) > store.MaxProjectAssets {
			_, _ = fmt.Fprintf(errOut, "cfctl %s: %d ready video assets; using the oldest %d\n", name, len(list), store.MaxProjectAssets)
			list = list[:store.MaxProjectAssets]
		}
		for _, a := range list {
			assetIDs = append(assetIDs, a.ID.String())
		}
	}

	req := api.CreateProjectRequest{Format: api.ProjectFormat(format), AssetIds: assetIDs}
	if set["title"] {
		req.Title = title
	}
	if briefText != "" {
		req.Brief = &briefText
	}
	_, _ = fmt.Fprintf(out, "cfctl project create (CF_ENV=%s from %s, DATABASE_URL from %s, env file %s)\n", env.vars["CF_ENV"],
		sourceOf("CF_ENV", env.file, lookup), sourceOf("DATABASE_URL", env.file, lookup), displayPath(env.root, env.path))
	_, _ = fmt.Fprintf(out, "  workspace   %q (%s)\n  created by  %s (owner)\n", ws.Name, ws.ID, owner)
	d, perr := svc.Create(ctx, who, req)
	if perr != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %s: %s\n", name, perr.Code, perr.Message)
		return 1
	}
	printProjectHead(out, d)
	_, _ = fmt.Fprintf(out, "\nnext, run the editor on it:\n"+
		"  cfctl agent run --agent editor --workspace %[1]s --project %[2]s --profile dev-cheap --budget-usd 0.25 --events --yes\n"+
		"  cfctl agent run --agent editor --workspace %[1]s --project %[2]s --profile v1-start --budget-usd 5 --events --yes\n", ws.ID, d.Project.ID)
	return 0
}

// workspaceOwner picks the member the project is created as: the workspace's first owner (members come oldest first).
func workspaceOwner(ctx context.Context, st *store.Store, ws store.Workspace, name string, errOut io.Writer) (projects.Actor, string, int) {
	members, err := st.ListMembers(ctx, ws.ID)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: looking up the members failed: %v\n", name, err)
		return projects.Actor{}, "", 1
	}
	for _, m := range members {
		if store.Role(m.Role) == store.RoleOwner {
			return projects.Actor{Workspace: ws.ID, User: m.UserID}, m.Email, 0
		}
	}
	_, _ = fmt.Fprintf(errOut, "cfctl %s: the workspace has no owner to create the project as\n", name)
	return projects.Actor{}, "", 2
}

// readyVideoAssets returns every ready video asset of the workspace (probed, so the editor can use it), oldest first.
func readyVideoAssets(ctx context.Context, st *store.Store, ws ids.WorkspaceID) ([]store.Asset, error) {
	const pageSize = 500
	var ready []store.Asset
	var before *ids.AssetID
	for {
		page, err := st.ListAssets(ctx, ws, before, pageSize)
		if err != nil {
			return nil, err
		}
		for _, a := range page {
			if a.Kind == store.AssetKindVideo && a.Status == store.AssetReady && a.DurationMs != nil {
				ready = append(ready, a)
			}
		}
		if len(page) < pageSize {
			break
		}
		last := page[len(page)-1].ID
		before = &last
	}
	slices.SortFunc(ready, func(a, b store.Asset) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.ID.String(), b.ID.String()))
	})
	return ready, nil
}

func readBriefFile(path string) (string, error) {
	info, err := os.Stat(path) //nolint:gosec // G703: the operator names the file on the command line
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBriefFileBytes {
		return "", fmt.Errorf("%s: the brief file must be a regular text file of at most %d KiB", path, maxBriefFileBytes>>10)
	}
	b, err := os.ReadFile(path) //nolint:gosec // G304: the operator names the file on the command line
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func printProjectHead(out io.Writer, d projects.Detail) {
	p := d.Project
	_, _ = fmt.Fprintf(out, "\nproject  %s\n  title    %s\n  format   %s\n  status   %s\n", p.ID, p.Title, p.Format, p.Status)
	if p.Brief != "" {
		_, _ = fmt.Fprintf(out, "  brief    %s\n", clip(p.Brief, 200))
	}
	_, _ = fmt.Fprintf(out, "  assets   %d\n", len(d.Assets))
	for _, a := range d.Assets {
		line := fmt.Sprintf("    %s  %s", a.AssetID, a.Title)
		if a.DurationMs != nil {
			line += fmt.Sprintf("  (%.1fs)", float64(*a.DurationMs)/1000)
		}
		if a.Deleted {
			line += "  [deleted]"
		}
		_, _ = fmt.Fprintln(out, line)
	}
}

func cmdProjectShow(args []string, stdout, stderr io.Writer, lookup lookupEnv, deps agentDeps) int {
	const name = "project show"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, projectUsage) }
	workspaceArg := flags.String("workspace", "", "workspace id")
	envFileArg := flags.String("env-file", "", "env file")
	// Flags may come after the project id.
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
	if len(positional) != 1 {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: give exactly one project id (prj_...)\n", name)
		return 2
	}
	pid, err := ids.ParseProjectID(positional[0])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl %s: %q is not a project id like prj_01...\n", name, positional[0])
		return 2
	}
	wantWS, wantOrg, ok := parseWorkspaceRef(name, *workspaceArg, stderr)
	if !ok {
		return 2
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
	who := projects.Actor{Workspace: ws.ID}
	d, perr := svc.Get(ctx, who, pid)
	if perr != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %s: %s\n", name, perr.Code, perr.Message)
		return 1
	}
	printProjectHead(out, d)
	if d.Project.CurrentTimelineVersion != nil {
		_, _ = fmt.Fprintf(out, "  current  version %d\n", *d.Project.CurrentTimelineVersion)
	}
	versions, _, perr := svc.Versions(ctx, who, pid, projects.MaxVersionPage, 0)
	if perr != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl %s: %s: %s\n", name, perr.Code, perr.Message)
		return 1
	}
	_, _ = fmt.Fprintf(out, "\nversions (%d):\n", len(versions))
	for _, v := range versions {
		parent := "-"
		if v.ParentVersion != nil {
			parent = fmt.Sprint(*v.ParentVersion)
		}
		_, _ = fmt.Fprintf(out, "  v%-3d parent %-3s %s %s  %s\n", v.Version, parent, v.AuthorKind, v.AuthorID, v.CreatedAt.UTC().Format(time.RFC3339))
		for _, s := range v.Summary {
			_, _ = fmt.Fprintf(out, "        - %s\n", clip(s, 160))
		}
	}
	printLatestRender(ctx, out, st, ws.ID, pid)
	return 0
}

// printLatestRender prints the project's newest render when the database has a renders table; before that table exists it
// prints nothing. The row is read as JSON, so the command follows the table's columns without knowing them.
func printLatestRender(ctx context.Context, out io.Writer, st *store.Store, ws ids.WorkspaceID, project ids.ProjectID) {
	var exists bool
	if err := st.Pool().QueryRow(ctx, `SELECT to_regclass('renders') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		return
	}
	var raw []byte
	err := st.Pool().QueryRow(ctx, `SELECT to_jsonb(r) FROM renders r WHERE r.workspace_id = $1 AND r.project_id = $2 ORDER BY r.created_at DESC LIMIT 1`,
		ws, project).Scan(&raw)
	if err != nil {
		_, _ = fmt.Fprintln(out, "\nlatest render: none")
		return
	}
	var row map[string]any
	if json.Unmarshal(raw, &row) != nil {
		return
	}
	_, _ = fmt.Fprintln(out, "\nlatest render:")
	for _, k := range []string{"id", "status", "timeline_version", "version", "format", "width", "height", "duration_ms", "failure_reason", "created_at", "finished_at"} {
		if v, ok := row[k]; ok && v != nil {
			_, _ = fmt.Fprintf(out, "  %-17s %v\n", k, v)
		}
	}
}
