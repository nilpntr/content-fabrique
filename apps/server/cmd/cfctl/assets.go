package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/assets"
	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/doctor"
	"github.com/nilpntr/content-fabrique/internal/envfile"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/storage"
	"github.com/nilpntr/content-fabrique/internal/store"
)

const assetsUsage = `Usage:
  cfctl assets import --workspace <wks_... or org_...> [--title-from-filename] [--wait] [flags] <files...>

Development only. Imports local media files into a workspace's library through the same code as the browser: each file becomes
an asset (uploading), its bytes go to object storage as a multipart upload (16 MiB parts, presigned like the API's), the upload
is completed, and the asset_ingest job is enqueued exactly like POST /v1/assets/{id}/complete does (the worker then probes it
and makes the proxy, poster and the rest). Files are read in place and never copied. Prints one line per file: the asset's
TypeID, its status and the file name.

It reads DATABASE_URL (the app's own role) and the R2_* settings from the env file, so it needs Postgres and MinIO running
(task up). It is refused when CF_ENV is staging or production, and when CF_ENV is not set to local, ci or bench.

  --workspace  the workspace to import into: a wks_... id or a Clerk org_... id (required)
  --user       the member the assets are created by: an e-mail address or a Clerk user id. Default: the workspace's first owner
  --title-from-filename
               the asset's title is the file name without its extension. That is also what the API does by default, so
               this flag only says it out loud
  --wait       after the import, wait until every asset is ready or failed (the worker must be running: task dev) and
               print the outcome
  --timeout    how long --wait waits in all (default 15m)
  --env-file   the env file; default .env at the repository root, and a relative name is taken from there too
  --yes        needed unless CF_ENV is local or ci, the database is on this machine, and CF_ENV and DATABASE_URL come from
               the same place

Local example:  task dev   (in one terminal)   then   cd apps/server && go run ./cmd/cfctl assets import --workspace wks_01... --wait ~/Footage/*.mov
`

// importPollInterval is how often --wait looks at the assets. A variable so the tests do not wait seconds.
var importPollInterval = 2 * time.Second

// importContentTypes maps the extensions of the files a creator has to the content types the API accepts.
var importContentTypes = map[string]string{
	".mp4": "video/mp4", ".m4v": "video/mp4", ".mov": "video/quicktime", ".webm": "video/webm",
	".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".aac": "audio/aac", ".wav": "audio/wav", ".flac": "audio/flac", ".ogg": "audio/ogg",
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".heic": "image/heic",
}

func cmdAssets(args []string, stdout, stderr io.Writer, lookup lookupEnv) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, assetsUsage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help", "-help":
		_, _ = fmt.Fprint(stdout, assetsUsage)
		return 0
	case "import":
		return cmdAssetsImport(args[1:], stdout, stderr, lookup)
	}
	_, _ = fmt.Fprintf(stderr, "cfctl assets: unknown command %q\n\n%s", args[0], assetsUsage)
	return 2
}

func cmdAssetsImport(args []string, stdout, stderr io.Writer, lookup lookupEnv) int {
	flags := flag.NewFlagSet("assets import", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, assetsUsage) }
	workspaceArg := flags.String("workspace", "", "workspace id")
	userArg := flags.String("user", "", "member who creates the assets")
	_ = flags.Bool("title-from-filename", false, "title is the file name without its extension")
	wait := flags.Bool("wait", false, "wait for the ingest")
	timeout := flags.Duration("timeout", 15*time.Minute, "how long --wait waits")
	envFileArg := flags.String("env-file", "", "env file")
	yes := flags.Bool("yes", false, "confirm outside the plain local setup")
	// Flags may follow the files: `import --workspace x a.mp4 --wait`.
	var files []string
	rest := args
	for {
		if err := flags.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if flags.NArg() == 0 {
			break
		}
		files = append(files, flags.Arg(0))
		rest = flags.Args()[1:]
	}

	// Arguments first, so a typo never needs a database.
	var (
		wantWS  ids.WorkspaceID
		wantOrg string
	)
	switch w := strings.TrimSpace(*workspaceArg); {
	case w == "":
		_, _ = fmt.Fprintln(stderr, "cfctl assets import: --workspace is required (a wks_... id or a Clerk org_... id)")
		return 2
	case strings.HasPrefix(w, "org_"):
		wantOrg = w
	default:
		id, err := ids.ParseWorkspaceID(w)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "cfctl assets import: --workspace must be a workspace id like wks_01... or a Clerk organization id like org_...")
			return 2
		}
		wantWS = id
	}
	if len(files) == 0 {
		_, _ = fmt.Fprintln(stderr, "cfctl assets import: name at least one file")
		return 2
	}
	plans, err := planImports(files)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cfctl assets import: %v\n", err)
		return 2
	}

	root, example, code := loadExample(stderr)
	if code != 0 {
		return code
	}
	explicit := *envFileArg != ""
	path := *envFileArg
	if !explicit {
		path = filepath.Join(root, ".env")
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	file := map[string]string{}
	parsed, err := envfile.ParseFile(path)
	switch {
	case err == nil:
		file = parsed
	case errors.Is(err, fs.ErrNotExist) && !explicit:
		// process environment only
	default:
		_, _ = fmt.Fprintf(stderr, "cfctl assets import: cannot read the env file %s\n", displayPath(root, path))
		return 2
	}
	vars := doctor.MergeEnv(example, file, lookup)
	redactor := doctor.NewRedactor(example, vars)
	out, errOut := redactor.Writer(stdout), redactor.Writer(stderr)

	// Development only: the check is on CF_ENV, and it fails closed (an unset CF_ENV is not "local").
	cfEnv := vars["CF_ENV"]
	if msg := importEnvRefusal(cfEnv); msg != "" {
		_, _ = fmt.Fprintf(errOut, "cfctl assets import: %s (env file %s)\n", msg, displayPath(root, path))
		return 2
	}
	dsn := vars["DATABASE_URL"]
	if dsn == "" {
		_, _ = fmt.Fprintf(errOut, "cfctl assets import: DATABASE_URL is not set in %s or the environment\n", displayPath(root, path))
		return 2
	}
	if err := config.ValidatePostgresURL(dsn); err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl assets import: DATABASE_URL: %v\n", err)
		return 2
	}
	cfSource, urlSource := sourceOf("CF_ENV", file, lookup), sourceOf("DATABASE_URL", file, lookup)
	if msg := mutationRefusal("assets import", "it writes assets into a workspace", "DATABASE_URL", cfEnv, cfSource, urlSource, dsn, *yes, false); msg != "" {
		_, _ = fmt.Fprintf(errOut, "cfctl assets import: %s (env file %s)\n", msg, displayPath(root, path))
		return 2
	}
	storageCfg := storage.Config{
		Endpoint: vars["R2_ENDPOINT"], Bucket: vars["R2_BUCKET"], AccessKeyID: vars["R2_ACCESS_KEY_ID"],
		SecretAccessKey: vars["R2_SECRET_ACCESS_KEY"], Jurisdiction: strings.ToLower(vars["R2_JURISDICTION"]),
	}
	if !storageCfg.Configured() {
		_, _ = fmt.Fprintf(errOut, "cfctl assets import: the R2_* settings (R2_ENDPOINT, R2_BUCKET, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY) are not set in %s or the environment\n", displayPath(root, path))
		return 2
	}

	ctx, stop := signalContext()
	defer stop()
	st, err := store.Open(ctx, dsn, store.Options{AppName: "cfctl", MaxConns: 4})
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl assets import: %v\n", err)
		return 1
	}
	defer st.Close()
	objects, err := storage.New(storageCfg)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl assets import: object storage: %v\n", err)
		return 1
	}
	river, err := jobs.NewInsertClient(st.Pool(), nil)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl assets import: %v\n", err)
		return 1
	}
	svc, err := assets.NewService(st, objects, jobs.NewEnqueuer(river, nil), nil)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl assets import: %v\n", err)
		return 1
	}

	_, _ = fmt.Fprintf(out, "cfctl assets import (CF_ENV=%s from %s, DATABASE_URL from %s, env file %s)\n", cfEnv, cfSource, urlSource, displayPath(root, path))
	who, ws, code := chooseImportActor(ctx, st, wantWS, wantOrg, *userArg, errOut)
	if code != 0 {
		return code
	}
	_, _ = fmt.Fprintf(out, "  workspace   %q (%s)\n", ws.Name, ws.ID)

	imp := &importer{svc: svc, who: who, http: &http.Client{Timeout: 15 * time.Minute}}
	var imported []store.Asset
	failed := 0
	for _, p := range plans {
		a, err := imp.importFile(ctx, p)
		if err != nil {
			failed++
			_, _ = fmt.Fprintf(errOut, "%s: %v\n", p.name, err)
			continue
		}
		imported = append(imported, a)
		_, _ = fmt.Fprintf(out, "%s  %s  %s  (%s)\n", a.ID, a.Status, a.Filename, humanBytes(a.SizeBytes))
	}

	if *wait && len(imported) > 0 {
		if !waitForIngest(ctx, st, who.Workspace, imported, *timeout, out) {
			failed++
		}
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// importEnvRefusal is the dev-only guard: why the command may not run with this CF_ENV, or "".
func importEnvRefusal(cfEnv string) string {
	switch cfEnv {
	case config.EnvLocal, config.EnvCI, config.EnvBench:
		return ""
	case config.EnvStaging, config.EnvProduction:
		return "refusing to run with CF_ENV=" + cfEnv + ": this command is for development only. Upload through the app"
	case "":
		return "CF_ENV is not set: this command is for development only and runs with CF_ENV=local, ci or bench"
	}
	return "CF_ENV=" + cfEnv + " is not local, ci or bench: this command is for development only"
}

// importPlan is a file to import, checked before anything is written.
type importPlan struct {
	path, name, contentType string
	size                    int64
}

// planImports checks every file up front, so a typo in the last name does not leave half a batch imported.
func planImports(files []string) ([]importPlan, error) {
	plans := make([]importPlan, 0, len(files))
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s: not a regular file", f)
		}
		if info.Size() < 1 {
			return nil, fmt.Errorf("%s: the file is empty", f)
		}
		if info.Size() > assets.MaxFileSize {
			return nil, fmt.Errorf("%s: larger than 20 GB", f)
		}
		ext := strings.ToLower(filepath.Ext(f))
		ct := importContentTypes[ext]
		if ct == "" { // an extension the table lacks: any other video or audio type the system knows
			guess, _, _ := strings.Cut(mime.TypeByExtension(ext), ";")
			if strings.HasPrefix(guess, "video/") || strings.HasPrefix(guess, "audio/") {
				ct = guess
			}
		}
		if ct == "" {
			return nil, fmt.Errorf("%s: cannot tell the media type from the extension %q (mp4, mov, webm, mp3, m4a, wav, jpg, png, heic ...)", f, ext)
		}
		plans = append(plans, importPlan{path: f, name: filepath.Base(f), contentType: ct, size: info.Size()})
	}
	return plans, nil
}

// chooseImportActor finds the workspace and the member the assets are attributed to.
func chooseImportActor(ctx context.Context, st *store.Store, wantWS ids.WorkspaceID, wantOrg, user string, errOut io.Writer) (assets.Actor, store.Workspace, int) {
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
		_, _ = fmt.Fprintln(errOut, "cfctl assets import: no such workspace. Workspaces exist once someone has signed in to the app")
		return assets.Actor{}, store.Workspace{}, 1
	}
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl assets import: looking up the workspace failed: %v\n", err)
		return assets.Actor{}, store.Workspace{}, 1
	}
	members, err := st.ListMembers(ctx, ws.ID)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl assets import: looking up the members failed: %v\n", err)
		return assets.Actor{}, store.Workspace{}, 1
	}
	if user != "" {
		for _, m := range members {
			if strings.EqualFold(m.Email, strings.TrimSpace(user)) || m.ClerkUserID == strings.TrimSpace(user) {
				return assets.Actor{Workspace: ws.ID, User: m.UserID}, ws, 0
			}
		}
		_, _ = fmt.Fprintln(errOut, "cfctl assets import: that person is not a member of the workspace")
		return assets.Actor{}, store.Workspace{}, 2
	}
	for _, m := range members { // oldest first
		if store.Role(m.Role) == store.RoleOwner {
			return assets.Actor{Workspace: ws.ID, User: m.UserID}, ws, 0
		}
	}
	_, _ = fmt.Fprintln(errOut, "cfctl assets import: the workspace has no owner to attribute the assets to; pass --user")
	return assets.Actor{}, store.Workspace{}, 2
}

// importer uploads files through the assets service: the same Create, PresignParts and Complete calls the API makes.
type importer struct {
	svc  *assets.Service
	who  assets.Actor
	http *http.Client
}

// importFile creates the asset, uploads every part from the file and completes the upload. A failure after the asset was
// created aborts the upload, so it does not count against the workspace's 25 uploads in progress.
func (im *importer) importFile(ctx context.Context, p importPlan) (a store.Asset, err error) {
	created, e := im.svc.Create(ctx, im.who, api.CreateAssetRequest{Filename: p.name, SizeBytes: p.size, ContentType: p.contentType})
	if e != nil {
		return store.Asset{}, e
	}
	id := created.Asset.ID
	defer func() {
		if err == nil {
			return
		}
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if e := im.svc.Abort(actx, im.who, id); e != nil && e.Status != http.StatusNotFound {
			err = fmt.Errorf("%w (and the upload could not be aborted: %v)", err, e)
		}
	}()

	f, err := os.Open(p.path)
	if err != nil {
		return store.Asset{}, err
	}
	defer func() { _ = f.Close() }()

	urls := map[int]api.PartUrl{}
	for _, u := range created.Parts {
		urls[u.PartNumber] = u
	}
	count := int(created.Upload.PartCount)
	refs := make([]api.CompletedPartRef, 0, count)
	buf := make([]byte, created.Upload.PartSize)
	for n := 1; n <= count; n++ {
		u, ok := urls[n]
		if !ok { // past the first batch (or the URL has expired): ask for the next 100
			var numbers []int
			for k := n; k <= count && len(numbers) < assets.MaxPartBatch; k++ {
				numbers = append(numbers, k)
			}
			more, _, e := im.svc.PresignParts(ctx, im.who, id, numbers)
			if e != nil {
				return store.Asset{}, e
			}
			for _, m := range more {
				urls[m.PartNumber] = m
			}
			u = urls[n]
		}
		size := int(u.SizeBytes)
		if size > len(buf) {
			return store.Asset{}, fmt.Errorf("part %d is larger than the part size", n)
		}
		if _, err := io.ReadFull(io.NewSectionReader(f, int64(n-1)*created.Upload.PartSize, int64(size)), buf[:size]); err != nil {
			return store.Asset{}, fmt.Errorf("read part %d (did the file change while it was imported?): %w", n, err)
		}
		etag, err := im.put(ctx, u.Url, buf[:size])
		if err != nil {
			return store.Asset{}, fmt.Errorf("upload part %d of %d: %w", n, count, err)
		}
		refs = append(refs, api.CompletedPartRef{PartNumber: n, Etag: etag})
		delete(urls, n)
	}
	done, e := im.svc.Complete(ctx, im.who, id, refs)
	if e != nil {
		return store.Asset{}, e
	}
	return done, nil
}

// put sends one part to its presigned URL and returns its ETag, retrying a few times (a local MinIO can hiccup).
func (im *importer) put(ctx context.Context, target string, data []byte) (string, error) {
	var last error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(data))
		if err != nil {
			return "", errors.New("the part URL is not valid")
		}
		resp, err := im.http.Do(req)
		if err != nil {
			// The URL is a secret until it expires and a *url.Error quotes it: report only the cause.
			var ue *url.Error
			last = errors.New("the request to object storage failed")
			if errors.As(err, &ue) && ue.Err != nil {
				last = fmt.Errorf("the request to object storage failed: %w", ue.Err)
			}
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			last = fmt.Errorf("object storage answered %s", resp.Status)
			continue
		}
		etag := resp.Header.Get("ETag")
		if etag == "" {
			return "", errors.New("object storage sent no ETag")
		}
		return etag, nil
	}
	return "", last
}

// waitForIngest polls until every asset is ready or failed, or the time is up, and prints each outcome. It returns true when
// all of them are ready.
func waitForIngest(ctx context.Context, st *store.Store, ws ids.WorkspaceID, list []store.Asset, timeout time.Duration, out io.Writer) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pending := map[ids.AssetID]store.Asset{}
	for _, a := range list {
		pending[a.ID] = a
	}
	ok := true
	_, _ = fmt.Fprintf(out, "waiting for the ingest of %d asset(s); the worker must be running (task dev)\n", len(pending))
	last := map[ids.AssetID]string{}
	for len(pending) > 0 {
		for id, a := range pending {
			cur, err := st.GetAsset(ctx, ws, id)
			switch {
			case err != nil && ctx.Err() != nil:
			case err != nil:
				_, _ = fmt.Fprintf(out, "%s  cannot be read: %v\n", id, err)
				delete(pending, id)
				ok = false
			case cur.Status == store.AssetReady:
				_, _ = fmt.Fprintf(out, "%s  ready  %s%s\n", id, a.Filename, mediaSummary(cur))
				delete(pending, id)
			case cur.Status == store.AssetFailed:
				_, _ = fmt.Fprintf(out, "%s  failed  %s  reason: %s\n", id, a.Filename, cmp.Or(deref(cur.FailureReason), "unknown"))
				delete(pending, id)
				ok = false
			default:
				line := fmt.Sprintf("%s  %s  %d%%", id, cur.Status, cur.IngestProgress)
				if stage := deref(cur.IngestStage); stage != "" {
					line += "  " + stage
				}
				if line != last[id] {
					_, _ = fmt.Fprintln(out, line)
					last[id] = line
				}
			}
		}
		if len(pending) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			for id, a := range pending {
				_, _ = fmt.Fprintf(out, "%s  not finished  %s  (gave up waiting; is the worker running?)\n", id, a.Filename)
			}
			return false
		case <-time.After(importPollInterval):
		}
	}
	return ok
}

func mediaSummary(a store.Asset) string {
	if a.DurationMs == nil {
		return ""
	}
	s := fmt.Sprintf("  %.1fs", float64(*a.DurationMs)/1000)
	if a.Width != nil && a.Height != nil {
		s += fmt.Sprintf("  %dx%d", *a.Width, *a.Height)
	}
	return s
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
