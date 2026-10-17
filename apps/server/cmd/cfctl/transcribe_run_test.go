package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/jobs/jobstest"
	"github.com/nilpntr/content-fabrique/internal/store"
)

func (r *projectRig) transcribeRun(args ...string) (code int, stdout, stderr string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	code = cmdTranscribe(append([]string{"run"}, append(args, "--env-file", r.envFile)...), &out, &errb, fakeProcessEnv(nil))
	return code, out.String(), errb.String()
}

// withAudio makes a ready video of the workspace that has a 16 kHz audio derivative.
func (r *projectRig) withAudio(ws store.Workspace, title string) store.Asset {
	r.t.Helper()
	a := r.asset(ws, store.AssetKindVideo, "processing", title)
	ctx := context.Background()
	if _, err := r.st.ClaimAssetForIngest(ctx, ws.ID, a.ID); err != nil {
		r.t.Fatal(err)
	}
	done, err := r.st.FinishAssetIngest(ctx, ws.ID, a.ID, store.IngestResult{DurationMs: 4000, Width: 1080, Height: 1920, FPS: 30, HasAudio: true,
		Derivatives: []store.NewDerivative{{Kind: store.DerivativeAudio16k, Key: "ws/" + ws.ID.String() + "/assets/" + a.ID.String() + "/audio_16k.wav", ContentType: "audio/wav", SizeBytes: 10}}})
	if err != nil {
		r.t.Fatal(err)
	}
	return done
}

func (r *projectRig) transcribeJobs(a store.Asset) (n int, forced int) {
	r.t.Helper()
	err := r.st.Pool().QueryRow(context.Background(),
		`SELECT count(*), count(*) FILTER (WHERE (args->>'force')::bigint <> 0) FROM river_job WHERE kind = 'transcribe' AND args->>'asset_id' = $1`, a.ID.String()).Scan(&n, &forced)
	if err != nil {
		r.t.Fatal(err)
	}
	return n, forced
}

// `cfctl transcribe run` is how a no_speech or failed transcript is done again: it enqueues the job for the asset's current ingest,
// refuses a duplicate of an ingest that has a job, and --force enqueues another.
func TestTranscribeRunEnqueuesForTheCurrentIngest(t *testing.T) {
	r := newProjectRig(t)
	jobstest.Migrate(t, r.dsn)
	ctx := context.Background()
	a := r.withAudio(r.ws, "talk")

	// The rig has no worker hook: the ingest left the transcript pending without a job, which is the state to repair.
	if n, _ := r.transcribeJobs(a); n != 0 {
		t.Fatalf("%d jobs before", n)
	}
	args := []string{"--workspace", "org_project", "--asset", a.ID.String()}
	code, out, errOut := r.transcribeRun(args...)
	if code != 0 || !strings.Contains(out, "transcription enqueued") || !strings.Contains(out, "transcript_status pending") {
		t.Fatalf("run = %d\n%s\n%s", code, out, errOut)
	}
	if n, forced := r.transcribeJobs(a); n != 1 || forced != 0 {
		t.Fatalf("%d jobs, %d forced", n, forced)
	}
	var generation int64
	if err := r.st.Pool().QueryRow(ctx, `SELECT (args->>'generation')::bigint FROM river_job WHERE kind = 'transcribe' AND args->>'asset_id' = $1`, a.ID.String()).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != a.IngestedAt.UnixMicro() {
		t.Errorf("generation %d, want the asset's current ingest %d", generation, a.IngestedAt.UnixMicro())
	}

	// Again: the job of this ingest exists, nothing changes.
	if err := r.st.SetTranscriptStatus(ctx, r.ws.ID, a.ID, store.TranscriptNoSpeech); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = r.transcribeRun(args...)
	if code != 1 || !strings.Contains(errOut, "--force") {
		t.Fatalf("a duplicate: %d %s", code, errOut)
	}
	if n, _ := r.transcribeJobs(a); n != 1 {
		t.Errorf("%d jobs after a refused duplicate", n)
	}
	if got, _ := r.st.GetAsset(ctx, r.ws.ID, a.ID); got.TranscriptStatus != store.TranscriptNoSpeech {
		t.Errorf("a refused run changed the status to %q", got.TranscriptStatus)
	}

	// --force: a different job of the same ingest, and the transcript is pending again.
	code, out, errOut = r.transcribeRun(append(args, "--force")...)
	if code != 0 || !strings.Contains(out, "was no_speech") {
		t.Fatalf("forced run = %d\n%s\n%s", code, out, errOut)
	}
	if n, forced := r.transcribeJobs(a); n != 2 || forced != 1 {
		t.Fatalf("%d jobs, %d forced", n, forced)
	}
	if got, _ := r.st.GetAsset(ctx, r.ws.ID, a.ID); got.TranscriptStatus != store.TranscriptPending {
		t.Errorf("status %q", got.TranscriptStatus)
	}
	// Forced twice is two jobs (the nonce differs).
	if code, _, errOut = r.transcribeRun(append(args, "--force")...); code != 0 {
		t.Fatalf("second forced run: %d %s", code, errOut)
	}
	if n, forced := r.transcribeJobs(a); n != 3 || forced != 2 {
		t.Errorf("%d jobs, %d forced", n, forced)
	}
}

func TestTranscribeRunRefusals(t *testing.T) {
	r := newProjectRig(t)
	jobstest.Migrate(t, r.dsn)
	ctx := context.Background()
	other, err := r.st.CreateWorkspace(ctx, store.NewWorkspace{ClerkOrgID: "org_other", Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}

	silent := r.asset(r.ws, store.AssetKindVideo, "ready", "silent") // no audio derivative
	code, _, errOut := r.transcribeRun("--workspace", "org_project", "--asset", silent.ID.String())
	if code != 1 || !strings.Contains(errOut, "no 16 kHz audio") {
		t.Errorf("an asset without audio: %d %s", code, errOut)
	}
	pending := r.asset(r.ws, store.AssetKindVideo, "uploading", "busy")
	if code, _, errOut = r.transcribeRun("--workspace", "org_project", "--asset", pending.ID.String()); code != 1 || !strings.Contains(errOut, "not ready") {
		t.Errorf("an asset that is processing: %d %s", code, errOut)
	}
	// Another workspace's asset does not exist for this one.
	theirs := r.withAudio(other, "theirs")
	if code, _, errOut = r.transcribeRun("--workspace", "org_project", "--asset", theirs.ID.String()); code != 1 || !strings.Contains(errOut, "no such asset") {
		t.Errorf("another workspace's asset: %d %s", code, errOut)
	}
	if n, _ := r.transcribeJobs(theirs); n != 0 {
		t.Errorf("a job was enqueued for another workspace's asset")
	}
	// Bad arguments.
	for _, args := range [][]string{
		{"--asset", silent.ID.String()},
		{"--workspace", "org_project"},
		{"--workspace", "org_project", "--asset", "ast_nope"},
		{"--workspace", "nope", "--asset", silent.ID.String()},
	} {
		if code, _, _ := r.transcribeRun(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	// Not outside development.
	env := pingEnv(t, "CF_ENV=staging\nDATABASE_URL=postgres://u:p@localhost:1/db\n")
	var out, errb bytes.Buffer
	if code := cmdTranscribe([]string{"run", "--workspace", "org_x", "--asset", silent.ID.String(), "--env-file", env}, &out, &errb, fakeProcessEnv(nil)); code != 2 || !strings.Contains(errb.String(), "refusing to run with CF_ENV=staging") {
		t.Errorf("staging: %d %s", code, errb.String())
	}
}
