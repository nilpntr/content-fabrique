package annotations

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

func wantErr(t *testing.T, what string, e *Error, status int) {
	t.Helper()
	if e == nil || e.Status != status {
		t.Errorf("%s: got %v, want status %d", what, e, status)
	}
}

func TestCreateStoresTheTargetsFactsNotTheRequests(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.a

	// On a render: the project and the version come from the render.
	c, err := e.svc.Create(ctx, a.editor, CreateInput{TargetType: "render", TargetID: a.renderID(), StartMs: i64(4000), EndMs: i64(6000), Body: "  make this tighter  "})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.ID.String(), "ann_") || c.ID.Version() != 7 || c.WorkspaceID != a.ws.ID || c.TargetType != "render" || c.TargetID != a.render.ID.UUID ||
		c.ProjectID != a.project.ID || c.TimelineVersion == nil || *c.TimelineVersion != 2 || c.StartMs != 4000 || c.EndMs != 6000 || c.Kind != "comment" ||
		c.Body != "make this tighter" || c.Status != "open" || c.AuthorKind != "user" || c.AuthorID != a.editor.User.String() || !c.ParentID.IsZero() ||
		c.Pin != nil || c.AppliedInRunID.Valid || c.AddressedInVersion != nil || c.DeletedAt != nil {
		t.Fatalf("comment on a render: %+v", c)
	}

	// On a version: the version number comes from the version.
	v, err := e.svc.Create(ctx, a.owner, CreateInput{TargetType: "timeline_version", TargetID: a.version1.ID.String(), StartMs: i64(100), Body: "x"})
	if err != nil || v.ProjectID != a.project.ID || v.TimelineVersion == nil || *v.TimelineVersion != 1 || v.EndMs != 100 {
		t.Fatalf("comment on a version: %+v, %v", v, err)
	}

	// On a clip: no project, no version.
	cl, err := e.svc.Create(ctx, a.owner, CreateInput{TargetType: "asset", TargetID: a.assetID(), StartMs: i64(1000), EndMs: i64(60000), Kind: "include", Body: ""})
	if err != nil || !cl.ProjectID.IsZero() || cl.TimelineVersion != nil || cl.Kind != "include" || cl.Body != "" {
		t.Fatalf("comment on a clip: %+v, %v", cl, err)
	}

	// A project_id in the request is only checked against the target's.
	if _, err := e.svc.Create(ctx, a.owner, CreateInput{TargetType: "render", TargetID: a.renderID(), StartMs: i64(1), Body: "x", Project: &a.project.ID}); err != nil {
		t.Errorf("the render's own project: %v", err)
	}
	other := a.deletedProject.ID
	_, err = e.svc.Create(ctx, a.owner, CreateInput{TargetType: "render", TargetID: a.renderID(), StartMs: i64(1), Body: "x", Project: &other})
	wantErr(t, "another project than the render's", err, 404)
	_, err = e.svc.Create(ctx, a.owner, CreateInput{TargetType: "asset", TargetID: a.assetID(), StartMs: i64(1), Body: "x", Project: &a.project.ID})
	wantErr(t, "a project on a clip", err, 400)
}

func TestCreateTenancy(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.a, e.b

	// Every kind of target of workspace B, asked for by a member of A: not found, exactly as an id that does not exist.
	missing := map[string]string{
		"asset":            ids.Encode(ids.PrefixAsset, uuid.Must(uuid.NewV7())),
		"render":           ids.Encode(ids.PrefixRender, uuid.Must(uuid.NewV7())),
		"timeline_version": ids.Encode(ids.PrefixTimeline, uuid.Must(uuid.NewV7())),
	}
	foreign := map[string]string{"asset": b.assetID(), "render": b.renderID(), "timeline_version": b.versionID()}
	for typ, id := range foreign {
		_, ferr := e.svc.Create(ctx, a.owner, CreateInput{TargetType: typ, TargetID: id, StartMs: i64(1), Body: "x"})
		_, merr := e.svc.Create(ctx, a.owner, CreateInput{TargetType: typ, TargetID: missing[typ], StartMs: i64(1), Body: "x"})
		if ferr == nil || merr == nil || *ferr != *merr || ferr.Status != 404 {
			t.Errorf("%s of another workspace: %v; a missing one: %v", typ, ferr, merr)
		}
	}
	// A target of A named with B's project: the project check is a 404 too.
	_, err := e.svc.Create(ctx, a.owner, CreateInput{TargetType: "render", TargetID: a.renderID(), StartMs: i64(1), Body: "x", Project: &b.project.ID})
	wantErr(t, "another workspace's project", err, 404)

	// The version of a deleted project is not there.
	_, err = e.svc.Create(ctx, a.owner, CreateInput{TargetType: "timeline_version", TargetID: a.deletedProjectsVersion.ID.String(), StartMs: i64(1), Body: "x"})
	wantErr(t, "a version of a deleted project", err, 404)

	// A viewer does not comment; the role is checked before anything is looked up.
	_, err = e.svc.Create(ctx, a.viewer, CreateInput{TargetType: "render", TargetID: a.renderID(), StartMs: i64(1), Body: "x"})
	wantErr(t, "a viewer", err, 403)
	_, err = e.svc.Create(ctx, a.viewer, CreateInput{TargetType: "render", TargetID: b.renderID(), StartMs: i64(1), Body: "x"})
	wantErr(t, "a viewer on a foreign target", err, 403)

	// Nothing was written anywhere by the refused calls.
	var n int
	if qerr := e.pool.QueryRow(ctx, `SELECT count(*) FROM annotations`).Scan(&n); qerr != nil || n != 0 {
		t.Errorf("rows after the refused calls: %d, %v", n, qerr)
	}
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.a
	base := func() CreateInput {
		return CreateInput{TargetType: "render", TargetID: a.renderID(), StartMs: i64(1000), Body: "ok"}
	}
	f := func(v float64) *float64 { return &v }

	bad := func(name string, mut func(*CreateInput)) {
		t.Helper()
		in := base()
		mut(&in)
		_, err := e.svc.Create(ctx, a.owner, in)
		if err == nil || err.Status != 400 || err.Code != api.ErrorCodeInvalidRequest {
			t.Errorf("%s: %v, want 400 invalid_request", name, err)
		}
	}
	good := func(name string, mut func(*CreateInput)) Annotation {
		t.Helper()
		in := base()
		mut(&in)
		c, err := e.svc.Create(ctx, a.owner, in)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		return c
	}

	// Ranges.
	bad("a negative start", func(in *CreateInput) { in.StartMs = i64(-1) })
	bad("an end before the start", func(in *CreateInput) { in.EndMs = i64(999) })
	bad("an end past 24 hours", func(in *CreateInput) { in.EndMs = i64(MaxMs + 1) })
	bad("no start", func(in *CreateInput) { in.StartMs = nil })
	bad("past the end of the render (30 s) by more than a frame", func(in *CreateInput) { in.StartMs, in.EndMs = i64(29000), i64(30000+FrameToleranceMs+1) })
	good("up to the end of the render", func(in *CreateInput) { in.StartMs, in.EndMs = i64(29000), i64(30000) })
	good("a moment at the very start", func(in *CreateInput) { in.StartMs = i64(0) })
	bad("past the end of the clip (60 s)", func(in *CreateInput) {
		in.TargetType, in.TargetID, in.EndMs = "asset", a.assetID(), i64(60000+FrameToleranceMs+1)
	})
	good("a version has no known length", func(in *CreateInput) {
		in.TargetType, in.TargetID, in.EndMs = "timeline_version", a.versionID(), i64(900000)
	})

	// Targets and kinds.
	bad("an unknown target type", func(in *CreateInput) { in.TargetType = "project" })
	bad("a malformed target id", func(in *CreateInput) { in.TargetID = "nope" })
	bad("a target id of another kind", func(in *CreateInput) { in.TargetID = a.assetID() })
	bad("an unknown kind", func(in *CreateInput) { in.Kind = "love" })
	for _, k := range []string{"comment", "include", "exclude", "prefer", "instruction"} {
		good("kind "+k, func(in *CreateInput) { in.Kind = k })
	}

	// Bodies.
	bad("an empty comment", func(in *CreateInput) { in.Body = "  " })
	bad("an empty instruction", func(in *CreateInput) { in.Kind, in.Body = "instruction", "" })
	good("a bare exclude marker", func(in *CreateInput) { in.Kind, in.Body = "exclude", "" })
	bad("a body over 2000 characters", func(in *CreateInput) { in.Body = strings.Repeat("é", 2001) })
	good("a body of exactly 2000 characters", func(in *CreateInput) { in.Body = strings.Repeat("é", 2000) })
	if c := good("a body with a control character and a NUL byte (dropped, as in a title)", func(in *CreateInput) { in.Body = "a\u0007b\x00c" }); c.Body != "abc" {
		t.Errorf("control characters: %q", c.Body)
	}
	bad("a body that is not UTF-8", func(in *CreateInput) { in.Body = "a\xffb" })
	bad("a body of invisible characters only", func(in *CreateInput) { in.Body = "\u200b\u202e" })
	if c := good("a body with bidi and zero-width characters, and a line break", func(in *CreateInput) { in.Body = "gnp.\u202eexe \u200bok\nnext   line" }); c.Body != "gnp.exe ok next line" {
		t.Errorf("cleaned body: %q", c.Body)
	}

	// Pins.
	if c := good("a point pin", func(in *CreateInput) { in.Pin = &Pin{X: 0.25, Y: 0.75} }); !samePin(c.Pin, `{"x":0.25,"y":0.75}`) {
		t.Errorf("point pin stored as %s", c.Pin)
	}
	if c := good("a box pin on the edge", func(in *CreateInput) { in.Pin = &Pin{X: 0.5, Y: 0.5, W: f(0.5), H: f(0.5)} }); !samePin(c.Pin, `{"x":0.5,"y":0.5,"w":0.5,"h":0.5}`) {
		t.Errorf("box pin stored as %s", c.Pin)
	}
	good("a pin at the corners", func(in *CreateInput) { in.Pin = &Pin{X: 0, Y: 1} })
	bad("x above 1", func(in *CreateInput) { in.Pin = &Pin{X: 1.01, Y: 0.5} })
	bad("y below 0", func(in *CreateInput) { in.Pin = &Pin{X: 0.5, Y: -0.01} })
	bad("x is NaN", func(in *CreateInput) { in.Pin = &Pin{X: nan(), Y: 0.5} })
	bad("a box without a height", func(in *CreateInput) { in.Pin = &Pin{X: 0.1, Y: 0.1, W: f(0.2)} })
	bad("a box of no width", func(in *CreateInput) { in.Pin = &Pin{X: 0.1, Y: 0.1, W: f(0), H: f(0.2)} })
	bad("a box past the right edge", func(in *CreateInput) { in.Pin = &Pin{X: 0.8, Y: 0.1, W: f(0.3), H: f(0.1)} })
	bad("a box past the bottom edge", func(in *CreateInput) { in.Pin = &Pin{X: 0.1, Y: 0.9, W: f(0.1), H: f(0.2)} })
}

// samePin compares stored jsonb (which Postgres prints with spaces and its own key order) with the JSON it should mean.
func samePin(got []byte, want string) bool {
	var a, b map[string]float64
	return json.Unmarshal(got, &a) == nil && json.Unmarshal([]byte(want), &b) == nil && reflect.DeepEqual(a, b)
}

func nan() float64 { var z float64; return z / z }

func TestReplies(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.a, e.b
	root := e.comment(a.editor, a, CreateInput{StartMs: i64(4000), EndMs: i64(6000), Pin: &Pin{X: 0.5, Y: 0.5}, Kind: "instruction"})

	reply, err := e.svc.Create(ctx, a.owner, CreateInput{Parent: &root.ID, Body: "on it"})
	if err != nil {
		t.Fatal(err)
	}
	if reply.ParentID != root.ID || reply.TargetType != root.TargetType || reply.TargetID != root.TargetID || reply.ProjectID != root.ProjectID ||
		*reply.TimelineVersion != *root.TimelineVersion || reply.StartMs != 4000 || reply.EndMs != 6000 || reply.Kind != "comment" || reply.Pin != nil ||
		reply.AuthorID != a.owner.User.String() {
		t.Fatalf("reply: %+v", reply)
	}
	// What the request says about the target must be the comment's own.
	same := CreateInput{Parent: &root.ID, Body: "x", TargetType: "render", TargetID: a.renderID(), StartMs: i64(4000), EndMs: i64(6000)}
	if _, err := e.svc.Create(ctx, a.owner, same); err != nil {
		t.Errorf("a reply that names the comment's own target and range: %v", err)
	}
	for name, mut := range map[string]func(*CreateInput){
		"another target type": func(in *CreateInput) { in.TargetType = "asset" },
		"another target":      func(in *CreateInput) { in.TargetID = a.version2.ID.String() },
		"another range":       func(in *CreateInput) { in.StartMs = i64(1) },
		"a pin":               func(in *CreateInput) { in.Pin = &Pin{X: 0.1, Y: 0.1} },
		"another kind":        func(in *CreateInput) { in.Kind = "exclude" },
		"no text":             func(in *CreateInput) { in.Body = "" },
	} {
		in := CreateInput{Parent: &root.ID, Body: "x"}
		mut(&in)
		_, err := e.svc.Create(ctx, a.owner, in)
		wantErr(t, "a reply with "+name, err, 400)
	}
	// One level only.
	_, err = e.svc.Create(ctx, a.owner, CreateInput{Parent: &reply.ID, Body: "x"})
	wantErr(t, "a reply to a reply", err, 400)
	// The parent of another workspace, and one that is not there, are the same 404.
	foreign := e.comment(b.owner, b, CreateInput{})
	_, ferr := e.svc.Create(ctx, a.owner, CreateInput{Parent: &foreign.ID, Body: "x"})
	ghost := ids.AnnotationID{UUID: uuid.Must(uuid.NewV7())}
	_, gerr := e.svc.Create(ctx, a.owner, CreateInput{Parent: &ghost, Body: "x"})
	if ferr == nil || gerr == nil || *ferr != *gerr || ferr.Status != 404 {
		t.Errorf("reply to a foreign comment: %v; to a missing one: %v", ferr, gerr)
	}
	// A reply to a deleted comment is a 404.
	if derr := e.svc.Delete(ctx, a.editor, root.ID); derr != nil {
		t.Fatal(derr)
	}
	_, err = e.svc.Create(ctx, a.owner, CreateInput{Parent: &root.ID, Body: "x"})
	wantErr(t, "a reply to a deleted comment", err, 404)
}

func TestListByTargetAndByProject(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.a, e.b
	c1 := e.comment(a.owner, a, CreateInput{StartMs: i64(1000)})
	c2 := e.comment(a.editor, a, CreateInput{StartMs: i64(2000)})
	r1, err := e.svc.Create(ctx, a.owner, CreateInput{Parent: &c1.ID, Body: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	onV1 := e.comment(a.owner, a, CreateInput{TargetType: "timeline_version", TargetID: a.version1.ID.String(), StartMs: i64(10)})
	onClip := e.comment(a.owner, a, CreateInput{TargetType: "asset", TargetID: a.assetID(), StartMs: i64(10)})
	bs := e.comment(b.owner, b, CreateInput{})

	idsOf := func(rows []Annotation) []ids.AnnotationID {
		out := make([]ids.AnnotationID, len(rows))
		for i, r := range rows {
			out[i] = r.ID
		}
		return out
	}
	same := func(name string, got []Annotation, want ...Annotation) {
		t.Helper()
		g := idsOf(got)
		if len(g) != len(want) {
			t.Errorf("%s: %d comments, want %d", name, len(g), len(want))
			return
		}
		for i := range want {
			if g[i] != want[i].ID {
				t.Errorf("%s: comment %d is %v, want %v", name, i, g[i], want[i].ID)
			}
		}
	}

	rows, next, lerr := e.svc.List(ctx, a.viewer, ListQuery{TargetType: "render", TargetID: a.renderID()})
	if lerr != nil || next != "" {
		t.Fatal(lerr, next)
	}
	same("on the render, oldest first, with the reply", rows, c1, c2, r1)

	rows, _, _ = e.svc.List(ctx, a.viewer, ListQuery{Project: &a.project.ID})
	same("of the project (renders and versions, not clips)", rows, c1, c2, r1, onV1)
	rows, _, _ = e.svc.List(ctx, a.viewer, ListQuery{TargetType: "asset", TargetID: a.assetID()})
	same("on the clip", rows, onClip)
	rows, _, _ = e.svc.List(ctx, a.viewer, ListQuery{TargetType: "timeline_version", TargetID: a.version2.ID.String()})
	same("on a version without comments", rows)

	// Pages.
	rows, next, _ = e.svc.List(ctx, a.owner, ListQuery{Project: &a.project.ID, Limit: 2})
	same("first page", rows, c1, c2)
	if next != c2.ID.String() {
		t.Errorf("cursor = %q, want %q", next, c2.ID)
	}
	rows, next, _ = e.svc.List(ctx, a.owner, ListQuery{Project: &a.project.ID, Limit: 2, Cursor: next})
	same("second page", rows, r1, onV1)
	if next != "" {
		t.Errorf("the last page has a cursor: %q", next)
	}

	// Tenancy: B's targets and project are 404 for A, B sees its own only.
	for name, q := range map[string]ListQuery{
		"a foreign render":  {TargetType: "render", TargetID: b.renderID()},
		"a foreign clip":    {TargetType: "asset", TargetID: b.assetID()},
		"a foreign version": {TargetType: "timeline_version", TargetID: b.versionID()},
		"a foreign project": {Project: &b.project.ID},
		"a deleted project": {Project: &a.deletedProject.ID},
	} {
		_, _, err := e.svc.List(ctx, a.owner, q)
		wantErr(t, name, err, 404)
	}
	rows, _, _ = e.svc.List(ctx, b.viewer, ListQuery{Project: &b.project.ID})
	same("B's project", rows, bs)

	// Bad queries.
	for name, q := range map[string]ListQuery{
		"nothing named":              {},
		"a target and a project":     {TargetType: "render", TargetID: a.renderID(), Project: &a.project.ID},
		"a type without an id":       {TargetType: "render"},
		"an id without a type":       {TargetID: a.renderID()},
		"an id of the wrong kind":    {TargetType: "render", TargetID: a.assetID()},
		"a limit of 501":             {Project: &a.project.ID, Limit: 501},
		"a negative limit":           {Project: &a.project.ID, Limit: -1},
		"a cursor that is no id":     {Project: &a.project.ID, Cursor: "nope"},
		"a cursor of another prefix": {Project: &a.project.ID, Cursor: a.renderID()},
	} {
		_, _, err := e.svc.List(ctx, a.owner, q)
		wantErr(t, name, err, 400)
	}
}

func TestUpdateRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.a, e.b
	c := e.comment(a.editor, a, CreateInput{StartMs: i64(4000), EndMs: i64(6000), Pin: &Pin{X: 0.5, Y: 0.5}})
	reply, _ := e.svc.Create(ctx, a.editor, CreateInput{Parent: &c.ID, Body: "more"})
	resolved, open := str("resolved"), str("open")

	// The author edits anything.
	got, err := e.svc.Update(ctx, a.editor, c.ID, UpdateInput{Body: str("  tighter please  "), Kind: str("instruction"), StartMs: i64(5000), EndMs: i64(7000), Pin: &Pin{X: 0.1, Y: 0.2}})
	if err != nil || got.Body != "tighter please" || got.Kind != "instruction" || got.StartMs != 5000 || got.EndMs != 7000 || !samePin(got.Pin, `{"x":0.1,"y":0.2}`) || got.Status != "open" {
		t.Fatalf("author edit: %+v, %v", got, err)
	}
	// The replies follow the range.
	rr, _, _ := e.svc.List(ctx, a.editor, ListQuery{TargetType: "render", TargetID: a.renderID()})
	for _, r := range rr {
		if r.StartMs != 5000 || r.EndMs != 7000 {
			t.Errorf("%v (reply %v) has range %d-%d after the edit", r.ID, r.ID == reply.ID, r.StartMs, r.EndMs)
		}
	}
	// Only the start moves a moment's end with it.
	m := e.comment(a.editor, a, CreateInput{StartMs: i64(1000)})
	if got, err = e.svc.Update(ctx, a.editor, m.ID, UpdateInput{StartMs: i64(2000)}); err != nil || got.StartMs != 2000 || got.EndMs != 2000 {
		t.Errorf("moving a moment: %+v, %v", got, err)
	}
	if got, err = e.svc.Update(ctx, a.editor, c.ID, UpdateInput{RemovePin: true}); err != nil || got.Pin != nil {
		t.Errorf("remove_pin: %+v, %v", got, err)
	}

	// Another owner or editor may resolve and reopen, and change nothing else.
	for _, who := range []Actor{a.owner, a.editor2} {
		if got, err = e.svc.Update(ctx, who, c.ID, UpdateInput{Status: resolved}); err != nil || got.Status != "resolved" || got.AuthorID != a.editor.User.String() {
			t.Errorf("resolve by %s: %+v, %v", who.Role, got, err)
		}
		if got, err = e.svc.Update(ctx, who, c.ID, UpdateInput{Status: open}); err != nil || got.Status != "open" {
			t.Errorf("reopen by %s: %+v, %v", who.Role, got, err)
		}
		_, err = e.svc.Update(ctx, who, c.ID, UpdateInput{Body: str("mine now")})
		wantErr(t, "body by "+string(who.Role), err, 403)
		_, err = e.svc.Update(ctx, who, c.ID, UpdateInput{Status: resolved, Body: str("mine now")})
		wantErr(t, "status and body by "+string(who.Role), err, 403)
		_, err = e.svc.Update(ctx, who, c.ID, UpdateInput{RemovePin: true})
		wantErr(t, "remove_pin by "+string(who.Role), err, 403)
	}
	// A viewer reads only.
	_, err = e.svc.Update(ctx, a.viewer, c.ID, UpdateInput{Status: resolved})
	wantErr(t, "a viewer resolving", err, 403)

	// Tenancy: B cannot touch A's comment, and it is a 404, the same as a missing one.
	_, ferr := e.svc.Update(ctx, b.owner, c.ID, UpdateInput{Status: resolved})
	_, merr := e.svc.Update(ctx, b.owner, ids.AnnotationID{UUID: uuid.Must(uuid.NewV7())}, UpdateInput{Status: resolved})
	if ferr == nil || merr == nil || *ferr != *merr || ferr.Status != 404 {
		t.Errorf("foreign: %v; missing: %v", ferr, merr)
	}
	if again, _ := e.st.GetAnnotation(ctx, a.ws.ID, c.ID); again.Status != "open" {
		t.Errorf("the foreign attempt changed the comment: %+v", again)
	}

	// Validation.
	for name, in := range map[string]UpdateInput{
		"nothing":                    {},
		"status applied":             {Status: str("applied")},
		"status junk":                {Status: str("done")},
		"kind junk":                  {Kind: str("love")},
		"an empty body":              {Body: str(" ")},
		"a long body":                {Body: str(strings.Repeat("a", 2001))},
		"text that is not UTF-8":     {Body: str("a\xffb")},
		"an end before the start":    {StartMs: i64(9000), EndMs: i64(8000)},
		"past the end of the render": {EndMs: i64(30000 + FrameToleranceMs + 1)},
		"a pin off the frame":        {Pin: &Pin{X: 2, Y: 0}},
		"pin and remove_pin":         {Pin: &Pin{X: 0.5, Y: 0.5}, RemovePin: true},
		"kind exclude with no text":  {Kind: str("comment"), Body: str("")},
	} {
		_, err := e.svc.Update(ctx, a.editor, c.ID, in)
		wantErr(t, name, err, 400)
	}
	// An exclude marker may lose its text.
	if got, err = e.svc.Update(ctx, a.editor, c.ID, UpdateInput{Kind: str("exclude"), Body: str("")}); err != nil || got.Kind != "exclude" || got.Body != "" {
		t.Errorf("exclude marker: %+v, %v", got, err)
	}
	// Turning a bare marker into a comment needs text.
	_, err = e.svc.Update(ctx, a.editor, c.ID, UpdateInput{Kind: str("comment")})
	wantErr(t, "a marker made a comment without text", err, 400)

	// A reply: its text only, by its author.
	if got, err = e.svc.Update(ctx, a.editor, reply.ID, UpdateInput{Body: str("edited reply")}); err != nil || got.Body != "edited reply" {
		t.Errorf("edit a reply: %+v, %v", got, err)
	}
	for name, in := range map[string]UpdateInput{
		"a reply's range": {StartMs: i64(1)}, "a reply's kind": {Kind: str("include")}, "a reply's pin": {Pin: &Pin{X: 0, Y: 0}}, "a reply's status": {Status: resolved},
	} {
		_, err := e.svc.Update(ctx, a.editor, reply.ID, in)
		wantErr(t, name, err, 400)
	}
}

func TestEditingWhatTheEditorAppliedReopensIt(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.a
	c := e.comment(a.editor, a, CreateInput{})
	run := e.run(a)
	marked, err := e.applyHanded(a, run, intp(3), "Addressed in v3.")
	if err != nil || len(marked) != 1 {
		t.Fatalf("apply: %v, %v", marked, err)
	}
	got, _ := e.st.GetAnnotation(ctx, a.ws.ID, c.ID)
	if got.Status != "applied" || !got.AppliedInRunID.Valid || got.AddressedInVersion == nil || *got.AddressedInVersion != 3 {
		t.Fatalf("applied: %+v", got)
	}

	// Resolving what is applied keeps it closed and drops the run (a resolved comment names no run).
	if got, uerr := e.svc.Update(ctx, a.owner, c.ID, UpdateInput{Status: str("resolved")}); uerr != nil || got.Status != "resolved" || got.AppliedInRunID.Valid || got.AddressedInVersion != nil {
		t.Errorf("resolve an applied comment: %+v, %v", got, uerr)
	}
	// Applied again, then the author changes the text: it is open again.
	c2 := e.comment(a.editor, a, CreateInput{})
	run2 := e.run(a)
	if _, err := e.applyHanded(a, run2, nil, "Looked at this."); err != nil {
		t.Fatal(err)
	}
	got, uerr := e.svc.Update(ctx, a.editor, c2.ID, UpdateInput{Body: str("actually, the other way")})
	if uerr != nil || got.Status != "open" || got.AppliedInRunID.Valid || got.AddressedInVersion != nil || got.Body != "actually, the other way" {
		t.Errorf("edit an applied comment: %+v, %v", got, uerr)
	}
	// Reopened by hand too.
	c3 := e.comment(a.editor, a, CreateInput{})
	if _, err := e.applyHanded(a, e.run(a), intp(2), "Addressed in v2."); err != nil {
		t.Fatal(err)
	}
	if got, uerr = e.svc.Update(ctx, a.owner, c3.ID, UpdateInput{Status: str("open")}); uerr != nil || got.Status != "open" || got.AppliedInRunID.Valid {
		t.Errorf("reopen an applied comment: %+v, %v", got, uerr)
	}
}

func intp(v int) *int { return &v }

func TestTheEditorsRepliesAreNotAPersonsToChange(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.a
	c := e.comment(a.editor, a, CreateInput{})
	if _, err := e.applyHanded(a, e.run(a), intp(2), "Addressed in v2."); err != nil {
		t.Fatal(err)
	}
	rows, _, _ := e.svc.List(ctx, a.owner, ListQuery{Project: &a.project.ID})
	var agentReply Annotation
	for _, r := range rows {
		if r.AuthorKind == "agent" {
			agentReply = r
		}
	}
	if agentReply.ID.IsZero() || agentReply.ParentID != c.ID || agentReply.Body != "Addressed in v2." || agentReply.Kind != "comment" || agentReply.StartMs != c.StartMs {
		t.Fatalf("the editor's reply: %+v", agentReply)
	}
	_, err := e.svc.Update(ctx, a.owner, agentReply.ID, UpdateInput{Body: str("forged")})
	wantErr(t, "editing the editor's reply", err, 403)
	err2 := e.svc.Delete(ctx, a.owner, agentReply.ID)
	wantErr(t, "deleting the editor's reply", err2, 403)
	// The person who wrote the comment can still delete the thread.
	if derr := e.svc.Delete(ctx, a.editor, c.ID); derr != nil {
		t.Fatal(derr)
	}
	rows, _, _ = e.svc.List(ctx, a.owner, ListQuery{Project: &a.project.ID})
	if len(rows) != 0 {
		t.Errorf("after deleting the thread: %d rows", len(rows))
	}
}

func TestDelete(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.a, e.b
	c := e.comment(a.editor, a, CreateInput{})
	reply, _ := e.svc.Create(ctx, a.owner, CreateInput{Parent: &c.ID, Body: "r"})
	other := e.comment(a.editor, a, CreateInput{StartMs: i64(9000)})

	wantErr(t, "an owner who is not the author", e.svc.Delete(ctx, a.owner, c.ID), 403)
	wantErr(t, "another editor", e.svc.Delete(ctx, a.editor2, c.ID), 403)
	wantErr(t, "a viewer", e.svc.Delete(ctx, a.viewer, c.ID), 403)
	wantErr(t, "another workspace's owner", e.svc.Delete(ctx, b.owner, c.ID), 404)
	if _, err := e.st.GetAnnotation(ctx, a.ws.ID, c.ID); err != nil {
		t.Fatalf("a refused delete removed the comment: %v", err)
	}

	if err := e.svc.Delete(ctx, a.editor, c.ID); err != nil {
		t.Fatal(err)
	}
	// Soft: the row is there, marked, and so is the reply's.
	var n int
	if qerr := e.pool.QueryRow(ctx, `SELECT count(*) FROM annotations WHERE id = ANY($1) AND deleted_at IS NOT NULL`, []uuid.UUID{c.ID.UUID, reply.ID.UUID}).Scan(&n); qerr != nil || n != 2 {
		t.Errorf("soft-deleted rows: %d, %v", n, qerr)
	}
	wantErr(t, "a second delete", e.svc.Delete(ctx, a.editor, c.ID), 404)
	wantErr(t, "deleting the reply on its own after the thread went", e.svc.Delete(ctx, a.owner, reply.ID), 404)
	_, err := e.svc.Update(ctx, a.editor, c.ID, UpdateInput{Status: str("resolved")})
	wantErr(t, "updating a deleted comment", err, 404)
	rows, _, _ := e.svc.List(ctx, a.owner, ListQuery{Project: &a.project.ID})
	if len(rows) != 1 || rows[0].ID != other.ID {
		t.Errorf("list after delete: %v", rows)
	}

	// A comment on a deleted project goes with it; one on a clip goes with the clip.
	clip := e.comment(a.owner, a, CreateInput{TargetType: "asset", TargetID: a.assetID(), StartMs: i64(1)})
	if _, err := e.st.SoftDeleteAsset(ctx, a.ws.ID, a.asset.ID); err != nil {
		t.Fatal(err)
	}
	if _, gerr := e.st.GetAnnotation(ctx, a.ws.ID, clip.ID); gerr == nil {
		t.Error("the comment on a deleted clip is still readable")
	}
	if err := e.st.SoftDeleteProject(ctx, a.ws.ID, a.project.ID); err != nil {
		t.Fatal(err)
	}
	if _, gerr := e.st.GetAnnotation(ctx, a.ws.ID, other.ID); gerr == nil {
		t.Error("the comment on a deleted project is still readable")
	}
	_, err = e.svc.Update(ctx, a.editor, other.ID, UpdateInput{Status: str("resolved")})
	wantErr(t, "updating a comment of a deleted project", err, 404)
}

func TestApplyAnnotations(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.a, e.b

	note := e.comment(a.owner, a, CreateInput{StartMs: i64(1000)})
	instr := e.comment(a.owner, a, CreateInput{StartMs: i64(2000), Kind: "instruction"})
	exclude := e.comment(a.owner, a, CreateInput{StartMs: i64(3000), Kind: "exclude", Body: "cut this"})
	resolved := e.comment(a.owner, a, CreateInput{StartMs: i64(4000)})
	if _, err := e.svc.Update(ctx, a.owner, resolved.ID, UpdateInput{Status: str("resolved")}); err != nil {
		t.Fatal(err)
	}
	foreignOpen := e.comment(b.owner, b, CreateInput{})
	// Older than the run, but the run was never shown it.
	unseen := e.comment(a.owner, a, CreateInput{StartMs: i64(4500)})
	run := e.run(a)
	late := e.comment(a.owner, a, CreateInput{StartMs: i64(5000)})
	// The run was shown these (and a comment of another workspace, and a resolved one, which are not recorded or not marked).
	handed := []ids.AnnotationID{note.ID, instr.ID, exclude.ID, resolved.ID, foreignOpen.ID}
	if err := e.st.RecordAnnotationHandoffs(ctx, a.ws.ID, a.project.ID, run, handed); err != nil {
		t.Fatal(err)
	}
	var recorded int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM annotation_handoffs WHERE run_id = $1`, run).Scan(&recorded); err != nil || recorded != 4 {
		t.Fatalf("handoffs recorded: %d, %v (the other workspace's comment must not be among them)", recorded, err)
	}

	marked, err := e.st.ApplyAnnotations(ctx, a.ws.ID, a.project.ID, run, intp(3), "Addressed in v3.")
	if err != nil {
		t.Fatal(err)
	}
	if len(marked) != 2 {
		t.Fatalf("marked %d comments: %+v", len(marked), marked)
	}
	status := func(ws store.Workspace, id ids.AnnotationID) store.Annotation {
		t.Helper()
		got, err := e.st.GetAnnotation(ctx, ws.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	for _, c := range []Annotation{note, instr} {
		got := status(a.ws, c.ID)
		if got.Status != "applied" || got.AppliedInRunID.Bytes != run || got.AddressedInVersion == nil || *got.AddressedInVersion != 3 {
			t.Errorf("%s: %+v", c.Kind, got)
		}
	}
	for name, c := range map[string]Annotation{"a standing constraint": exclude, "a resolved comment": resolved, "a comment the run was never shown": unseen, "a comment written during the run": late} {
		if got := status(a.ws, c.ID); got.Status == "applied" || got.AppliedInRunID.Valid {
			t.Errorf("%s was marked: %+v", name, got)
		}
	}
	if got := status(b.ws, foreignOpen.ID); got.Status != "open" {
		t.Errorf("another workspace's comment: %+v", got)
	}
	// Each marked comment got the editor's answer, from the run.
	rows, _, _ := e.svc.List(ctx, a.owner, ListQuery{TargetType: "render", TargetID: a.renderID()})
	replies := 0
	for _, r := range rows {
		if r.AuthorKind == "agent" {
			replies++
			if r.AuthorID != ids.Encode(ids.PrefixRun, run) || r.Body != "Addressed in v3." || r.Status != "open" {
				t.Errorf("agent reply: %+v", r)
			}
		}
	}
	if replies != 2 {
		t.Errorf("%d replies", replies)
	}
	// Again: nothing left to mark, no second answer.
	if again, err := e.st.ApplyAnnotations(ctx, a.ws.ID, a.project.ID, run, intp(3), "Addressed in v3."); err != nil || len(again) != 0 {
		t.Errorf("second apply: %v, %v", again, err)
	}
	// A run of another workspace marks nothing of A's.
	if _, err := e.st.ApplyAnnotations(ctx, b.ws.ID, a.project.ID, e.run(b), intp(1), "x"); err != nil {
		t.Logf("a foreign run on A's project: %v", err)
	}
	if got := status(a.ws, late.ID); got.Status != "open" {
		t.Errorf("a foreign run changed A's comment: %+v", got)
	}
	// The open list for the editor: what is still open and not a reply.
	open, total, err := e.st.ListOpenAnnotationsForProject(ctx, a.ws.ID, a.project.ID, 0, 50)
	if err != nil || total != 3 || len(open) != 3 || open[0].ID != exclude.ID || open[1].ID != unseen.ID || open[2].ID != late.ID {
		t.Errorf("open for the editor: %d of %d, %v", len(open), total, err)
	}
	// Pages: the total is the whole, the page is a slice of it.
	page, total, err := e.st.ListOpenAnnotationsForProject(ctx, a.ws.ID, a.project.ID, 1, 1)
	if err != nil || total != 3 || len(page) != 1 || page[0].ID != unseen.ID {
		t.Errorf("page: %+v of %d, %v", page, total, err)
	}
	if page, total, err = e.st.ListOpenAnnotationsForProject(ctx, a.ws.ID, a.project.ID, 9, 5); err != nil || total != 3 || len(page) != 0 {
		t.Errorf("a page past the end: %d of %d, %v", len(page), total, err)
	}
}

// run makes an agent run of the tenant's workspace and returns its id.
func (e *env) run(tn *tenant) uuid.UUID {
	e.t.Helper()
	r, err := e.st.CreateAgentRun(context.Background(), tn.ws.ID, store.NewAgentRun{
		Agent: "editor", Stage: "editor", ProjectID: tn.project.ID.UUID, RoutingProfile: "dev-cheap", Model: "m", Effort: "low", MaxTokens: 1000,
		BudgetMicros: 1000, BudgetTokens: 1000, BudgetToolCalls: 10, BudgetWallClockMs: 1000, BilledTo: "test",
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return r.ID
}

// The worker's end-of-run hook knows only the run id: the run's own row says the workspace and project, and only a run that
// succeeded marks anything.
func TestApplyAnnotationsForRun(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.a
	c := e.comment(a.owner, a, CreateInput{StartMs: i64(1000)})
	handedOnly := e.comment(a.owner, a, CreateInput{StartMs: i64(2000)})
	run, failed := e.run(a), e.run(a)
	for _, r := range []uuid.UUID{run, failed} {
		if err := e.st.RecordAnnotationHandoffs(ctx, a.ws.ID, a.project.ID, r, []ids.AnnotationID{c.ID}); err != nil {
			t.Fatal(err)
		}
	}
	end := func(id uuid.UUID, status string) {
		t.Helper()
		reason := ""
		if status == "failed" {
			reason = "budget"
		}
		if _, err := e.pool.Exec(ctx, `UPDATE agent_runs SET status = $2, failure_reason = NULLIF($3, ''), finished_at = now() WHERE id = $1`, id, status, reason); err != nil {
			t.Fatal(err)
		}
	}

	// A run that is still going, or that failed, marks nothing.
	if n, err := e.st.ApplyAnnotationsForRun(ctx, run); err != nil || n != 0 {
		t.Errorf("a run that has not ended: %d, %v", n, err)
	}
	end(failed, "failed")
	if n, err := e.st.ApplyAnnotationsForRun(ctx, failed); err != nil || n != 0 {
		t.Errorf("a failed run: %d, %v", n, err)
	}
	if got, _ := e.st.GetAnnotation(ctx, a.ws.ID, c.ID); got.Status != "open" {
		t.Fatalf("marked by a run that did not succeed: %+v", got)
	}
	// A run id that is nobody's.
	if n, err := e.st.ApplyAnnotationsForRun(ctx, uuid.Must(uuid.NewV7())); err != nil || n != 0 {
		t.Errorf("an unknown run: %d, %v", n, err)
	}

	// The run made version 3: the comment it was handed is addressed in it; the one it was not handed stays open.
	if _, err := e.st.AddTimelineVersion(ctx, a.ws.ID, a.project.ID, store.NewTimelineVersion{
		Version: 3, Parent: 2, Spec: json.RawMessage(`{"schema":"x"}`), AuthorKind: store.AuthorAgent, AuthorID: ids.Encode(ids.PrefixRun, run),
	}); err != nil {
		t.Fatal(err)
	}
	end(run, "succeeded")
	if n, err := e.st.ApplyAnnotationsForRun(ctx, run); err != nil || n != 1 {
		t.Fatalf("the run that succeeded: %d, %v", n, err)
	}
	got, _ := e.st.GetAnnotation(ctx, a.ws.ID, c.ID)
	if got.Status != "applied" || got.AppliedInRunID.Bytes != run || got.AddressedInVersion == nil || *got.AddressedInVersion != 3 {
		t.Errorf("handed comment: %+v", got)
	}
	if got, _ := e.st.GetAnnotation(ctx, a.ws.ID, handedOnly.ID); got.Status != "open" {
		t.Errorf("a comment the run never saw: %+v", got)
	}
	if n, err := e.st.ApplyAnnotationsForRun(ctx, run); err != nil || n != 0 {
		t.Errorf("a second call: %d, %v", n, err)
	}
}

func TestEditingARangeByItsStartShiftsIt(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.a
	c := e.comment(a.owner, a, CreateInput{StartMs: i64(4000), EndMs: i64(6000)})
	got, err := e.svc.Update(ctx, a.owner, c.ID, UpdateInput{StartMs: i64(10000)})
	if err != nil || got.StartMs != 10000 || got.EndMs != 12000 {
		t.Errorf("start only: %d-%d, %v", got.StartMs, got.EndMs, err)
	}
	if got, err = e.svc.Update(ctx, a.owner, c.ID, UpdateInput{EndMs: i64(15000)}); err != nil || got.StartMs != 10000 || got.EndMs != 15000 {
		t.Errorf("end only: %d-%d, %v", got.StartMs, got.EndMs, err)
	}
	_, uerr := e.svc.Update(ctx, a.owner, c.ID, UpdateInput{EndMs: i64(5000)})
	wantErr(t, "an end before the start", uerr, 400)
	// A shift that pushes the end past the render (30 s) is refused, a frame of slack is not.
	_, uerr = e.svc.Update(ctx, a.owner, c.ID, UpdateInput{StartMs: i64(28000)})
	wantErr(t, "a shift past the end of the render", uerr, 400)
	if got, err = e.svc.Update(ctx, a.owner, c.ID, UpdateInput{StartMs: i64(20000), EndMs: i64(30000 + FrameToleranceMs)}); err != nil {
		t.Errorf("one frame past the end: %v", err)
	}
	_, uerr = e.svc.Update(ctx, a.owner, c.ID, UpdateInput{EndMs: i64(30000 + FrameToleranceMs + 1)})
	wantErr(t, "more than a frame past the end", uerr, 400)
	// The same tolerance when creating.
	_, cerr := e.svc.Create(ctx, a.owner, CreateInput{TargetType: "render", TargetID: a.renderID(), StartMs: i64(29000), EndMs: i64(30000 + FrameToleranceMs), Body: "x"})
	if cerr != nil {
		t.Errorf("create one frame past the end: %v", cerr)
	}
	_, cerr = e.svc.Create(ctx, a.owner, CreateInput{TargetType: "render", TargetID: a.renderID(), StartMs: i64(29000), EndMs: i64(30000 + FrameToleranceMs + 1), Body: "x"})
	wantErr(t, "create more than a frame past the end", cerr, 400)
}

// applyHanded ends a run that was shown every open comment of the project: they are recorded as handed, then marked.
func (e *env) applyHanded(tn *tenant, run uuid.UUID, version *int, reply string) ([]store.Annotation, error) {
	e.t.Helper()
	ctx := context.Background()
	open, _, err := e.st.ListOpenAnnotationsForProject(ctx, tn.ws.ID, tn.project.ID, 0, 500)
	if err != nil {
		e.t.Fatal(err)
	}
	list := make([]ids.AnnotationID, len(open))
	for i, a := range open {
		list[i] = a.ID
	}
	if err := e.st.RecordAnnotationHandoffs(ctx, tn.ws.ID, tn.project.ID, run, list); err != nil {
		e.t.Fatal(err)
	}
	return e.st.ApplyAnnotations(ctx, tn.ws.ID, tn.project.ID, run, version, reply)
}

func TestTheEditorsSourcePagesAndToleratesABadPin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.a
	var first Annotation
	for i := range MaxEditorComments + 5 {
		c := e.comment(a.owner, a, CreateInput{StartMs: i64(int64(i) * 10)})
		if i == 0 {
			first = c
		}
	}
	// A pin the table accepts (an object) that is not a pin.
	if _, err := e.pool.Exec(ctx, `UPDATE annotations SET pin = '{"x": "nope"}' WHERE id = $1`, first.ID.UUID); err != nil {
		t.Fatal(err)
	}
	src := &Source{Store: e.st, Logger: quiet}

	page, total, err := src.OpenForProject(ctx, a.ws.ID, a.project.ID, 0)
	if err != nil || total != MaxEditorComments+5 || len(page) != MaxEditorComments {
		t.Fatalf("first page: %d of %d, %v", len(page), total, err)
	}
	if page[0].Annotation.ID != first.ID || page[0].Pin != nil {
		t.Errorf("the comment with a bad pin: %+v", page[0])
	}
	rest, total, err := src.OpenForProject(ctx, a.ws.ID, a.project.ID, MaxEditorComments)
	if err != nil || total != MaxEditorComments+5 || len(rest) != 5 {
		t.Fatalf("second page: %d of %d, %v", len(rest), total, err)
	}
	// Reading hands nothing over; handing over is explicit, and only for what is named.
	var handed int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM annotation_handoffs`).Scan(&handed); err != nil || handed != 0 {
		t.Errorf("handoffs after reading: %d, %v", handed, err)
	}
	run := e.run(a)
	if err := src.RecordHandoff(ctx, a.ws.ID, a.project.ID, run, rest); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM annotation_handoffs WHERE run_id = $1`, run).Scan(&handed); err != nil || handed != 5 {
		t.Errorf("handoffs after recording the second page: %d, %v", handed, err)
	}

	// The API lists the comment, without the pin, instead of failing the list.
	rows, _, lerr := e.svc.List(ctx, a.viewer, ListQuery{Project: &a.project.ID, Limit: 1})
	if lerr != nil || len(rows) != 1 {
		t.Fatal(lerr)
	}
	out, pinOK := Of(rows[0])
	if pinOK || out.Pin != nil || out.Id != first.ID.String() {
		t.Errorf("Of: pinOK %v, pin %v", pinOK, out.Pin)
	}
}
