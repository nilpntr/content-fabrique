package renderjobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/editor"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The adapter answers the editor's questions: the contact sheet key and the length for a ready render, and for every other ending
// an error the editor knows how to report (a message for the model, the context's own error, or "unavailable").
func TestPreviewRendererAnswersTheEditor(t *testing.T) {
	ctx := context.Background()
	key, dur := "ws/wks_x/projects/prj_x/renders/ren_x/contact_sheet.jpg", int64(16000)
	ready := Render{Status: store.RenderReady, ContactSheetKey: &key, DurationMs: &dur}
	reason := ReasonRenderFailed
	call := func(f *fakeRequester, timeout time.Duration) (editor.ContactSheetRef, editor.DurationMs, error) {
		return NewPreviewRenderer(f, timeout).RenderAndWait(ctx, ids.WorkspaceID{}, ids.ProjectID{}, 1, uuid.Nil)
	}

	if ref, d, err := call(&fakeRequester{states: []Render{ready}}, time.Second); err != nil || string(ref) != key || int64(d) != dur {
		t.Errorf("ready: %q %d %v", ref, d, err)
	}
	var te *runtime.ToolError
	if _, _, err := call(&fakeRequester{states: []Render{{Status: store.RenderFailed, FailureReason: &reason}}}, time.Second); !errors.As(err, &te) || !strings.Contains(te.Msg, ReasonRenderFailed) {
		t.Errorf("failed: %v", err)
	}
	for _, status := range []int{422, 429} {
		if _, _, err := call(&fakeRequester{req: &Error{Status: status, Code: api.ErrorCodeTimelineInvalid, Message: "nope"}}, time.Second); !errors.As(err, &te) || !strings.Contains(te.Msg, "nope") {
			t.Errorf("refused with %d: %v", status, err)
		}
	}
	if _, _, err := call(&fakeRequester{req: &Error{Status: 404, Code: api.ErrorCodeNotFound, Message: "no such project"}}, time.Second); err == nil || errors.As(err, &te) {
		t.Errorf("a 404 is not the model's to fix: %v", err)
	}
	if _, _, err := call(&fakeRequester{states: []Render{{Status: store.RenderRendering}}}, 300*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("not finished in time: %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := NewPreviewRenderer(&fakeRequester{states: []Render{{Status: store.RenderQueued}}}, time.Minute).RenderAndWait(cctx, ids.WorkspaceID{}, ids.ProjectID{}, 1, uuid.Nil); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context: %v", err)
	}
	var none *PreviewRenderer
	if _, _, err := none.RenderAndWait(ctx, ids.WorkspaceID{}, ids.ProjectID{}, 1, uuid.Nil); !errors.Is(err, editor.ErrPreviewUnavailable) {
		t.Errorf("no renderer: %v", err)
	}
}

// Through the real service, queue, pipeline and storage: the editor gets a contact sheet key it accepts (under the workspace, a JPEG,
// an object that exists) and the length, the render is recorded as the run's, and asking again for the same version reuses it.
func TestPreviewRendererThroughTheQueue(t *testing.T) {
	f := newFixture(t, "org_preview_adapter")
	e, ctx := f.e, context.Background()
	e.startWorker(t)
	p := NewPreviewRenderer(e.svc, 90*time.Second)
	run := uuid.Must(uuid.NewV7())

	ref, d, err := p.RenderAndWait(ctx, f.w.ID, f.project.ID, f.spec, run)
	if err != nil {
		t.Fatal(err)
	}
	if mt, err := editor.CheckImageRef(f.w.ID, string(ref)); err != nil || mt != "image/jpeg" {
		t.Fatalf("the editor refuses the reference %q: %q %v", ref, mt, err)
	}
	if !e.exists(t, string(ref)) || d < 2900 || d > 3100 {
		t.Errorf("sheet exists: %v, length %d ms", e.exists(t, string(ref)), d)
	}
	rows, _, lerr := e.svc.List(ctx, f.w.ID, f.project.ID, ListFilter{}, 0, "")
	if lerr != nil || len(rows) != 1 || rows[0].Profile != store.ProfilePreview || rows[0].RequestedByKind != store.RenderRequestedByAgent || rows[0].RequestedByID != ids.Encode(ids.PrefixRun, run) {
		t.Fatalf("renders: %+v %v", rows, lerr)
	}
	ref2, _, err := p.RenderAndWait(ctx, f.w.ID, f.project.ID, f.spec, run)
	if err != nil || ref2 != ref {
		t.Errorf("asking again: %q %v", ref2, err)
	}
	if rows, _, _ := e.svc.List(ctx, f.w.ID, f.project.ID, ListFilter{}, 0, ""); len(rows) != 1 {
		t.Errorf("%d renders after asking twice", len(rows))
	}
	// The constructor the worker and cfctl use builds the same thing over the store and storage: it finds the render already there.
	wired, err := NewPreviewRendererFor(e.st, e.objs, quiet)
	if err != nil {
		t.Fatal(err)
	}
	var _ editor.PreviewRenderer = wired
	if ref3, _, err := wired.RenderAndWait(ctx, f.w.ID, f.project.ID, f.spec, run); err != nil || ref3 != ref {
		t.Errorf("the wired renderer: %q %v", ref3, err)
	}
	// Another workspace's project is not found, and nothing is rendered for it.
	other := e.newWorkspace(t, "org_preview_adapter_b")
	if _, _, err := p.RenderAndWait(ctx, other.ID, f.project.ID, f.spec, run); err == nil || errors.As(err, new(*runtime.ToolError)) {
		t.Errorf("another workspace's project: %v", err)
	}
}
