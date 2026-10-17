package projects

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/api"
	"github.com/nilpntr/content-fabrique/internal/httpapi"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

func TestCleanText(t *testing.T) {
	cases := []struct {
		name, in, want string
		multiline, ok  bool
	}{
		{"plain", "Back day", "Back day", false, true},
		{"trimmed", "  Back day  ", "Back day", false, true},
		{"zero-width removed", "Ba\u200bck\u200d day\ufeff", "Back day", false, true},
		{"bidi override removed", "gnp.\u202eexe", "gnp.exe", false, true},
		{"bidi isolate removed", "a\u2066b\u2069", "ab", false, true},
		{"only invisible", "\u200b\u202e", "", false, true},
		{"nbsp becomes a space", "a\u00a0b", "a b", false, true},
		{"NFC", "é", "é", false, true},
		{"control refused", "a\u0007b", "", false, false},
		{"NUL refused", "a\x00b", "", false, false},
		{"line break refused in a title", "a\nb", "", false, false},
		{"line separator refused", "a\u2028b", "", true, false},
		{"line break kept in a brief", "a\nb\tc\r\n", "a\nb\tc", true, true},
		{"invalid UTF-8", "a\xffb", "", true, false},
	}
	for _, c := range cases {
		got, ok := cleanText(c.in, c.multiline)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%s: cleanText(%q) = %q, %v; want %q, %v", c.name, c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestValidateCreateText(t *testing.T) {
	asset := ids.Encode(ids.PrefixAsset, mustV7(t))
	str := func(s string) *string { return &s }
	req := func(title, brief *string) api.CreateProjectRequest {
		return api.CreateProjectRequest{Format: api.ProjectFormatN916, AssetIds: []string{asset}, Title: title, Brief: brief}
	}
	if title, brief, _, _, e := validateCreate(req(str("\u200bBack\u202e day"), str("go\u200b!"))); e != nil || title != "Back day" || brief != "go!" {
		t.Errorf("cleaned: %q %q %v", title, brief, e)
	}
	for name, r := range map[string]api.CreateProjectRequest{
		"a title that is empty once cleaned":        req(str("\u200b \u202e"), nil),
		"a blank title":                             req(str("   "), nil),
		"a title with a control character":          req(str("a\u0007"), nil),
		"a brief over 4000 characters":              req(nil, str(strings.Repeat("a", MaxBriefChars+1))),
		"a brief that is long only before cleaning": req(nil, str(strings.Repeat("a\u200b", MaxBriefChars+1))),
	} {
		if name == "a brief that is long only before cleaning" {
			// The limit counts the cleaned text: 4001 letters with invisible characters between them is still 4001 letters.
			if _, _, _, _, e := validateCreate(r); e == nil {
				t.Errorf("%s: accepted", name)
			}
			continue
		}
		if _, _, _, _, e := validateCreate(r); e == nil || e.Status != 400 {
			t.Errorf("%s: %v, want 400", name, e)
		}
	}
	if _, brief, _, _, e := validateCreate(req(nil, str(strings.Repeat("a\u200b", MaxBriefChars)))); e != nil || len(brief) != MaxBriefChars {
		t.Errorf("4000 letters with invisible characters: %d, %v", len(brief), e)
	}
}

func mustV7(t *testing.T) uuid.UUID {
	t.Helper()
	u, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestTooBig(t *testing.T) {
	spec, _ := EmptySpec("9:16")
	if is := tooBig(spec); is != nil {
		t.Fatalf("an empty timeline: %+v", is)
	}
	items := make([]timeline.VideoItem, MaxItemsPerTrack+1)
	spec.Tracks = []timeline.Track{&timeline.VideoTrack{ID: "main", Items: items}}
	if is := tooBig(spec); is == nil || is.Code != "timeline_too_large" {
		t.Fatalf("a track over the cap: %+v", is)
	}
	spec.Tracks = []timeline.Track{&timeline.VideoTrack{ID: "main", Items: items[:MaxItemsPerTrack]}}
	if is := tooBig(spec); is != nil {
		t.Fatalf("a track at the cap: %+v", is)
	}
}

// An API wired without a service (a test helper, a server that has none) answers 503 after the checks of authentication and
// role; it never panics.
func TestAPIWithoutServiceAnswers503(t *testing.T) {
	caller := func(role store.Role) httpapi.CallerFunc {
		return func(context.Context) (httpapi.Caller, bool) { return httpapi.Caller{Role: role}, true }
	}
	ctx := context.Background()
	id := ids.Encode(ids.PrefixProject, mustV7(t))
	body := &api.CreateProjectJSONRequestBody{Format: api.ProjectFormatN916, AssetIds: []string{ids.Encode(ids.PrefixAsset, mustV7(t))}}

	h := NewAPI(nil, caller(store.RoleEditor), nil)
	if r, err := h.CreateProject(ctx, api.CreateProjectRequestObject{Body: body}); err != nil {
		t.Fatal(err)
	} else if _, ok := r.(api.CreateProject503JSONResponse); !ok {
		t.Errorf("CreateProject = %T, want 503", r)
	}
	if r, err := h.GetProject(ctx, api.GetProjectRequestObject{Id: id}); err != nil {
		t.Fatal(err)
	} else if _, ok := r.(api.GetProject503JSONResponse); !ok {
		t.Errorf("GetProject = %T, want 503", r)
	}
	if r, err := h.ListProjects(ctx, api.ListProjectsRequestObject{}); err != nil {
		t.Fatal(err)
	} else if _, ok := r.(api.ListProjects503JSONResponse); !ok {
		t.Errorf("ListProjects = %T, want 503", r)
	}
	if r, err := h.ApplyTimelineOps(ctx, api.ApplyTimelineOpsRequestObject{Id: id, Body: &api.ApplyTimelineOpsJSONRequestBody{}}); err != nil {
		t.Fatal(err)
	} else if _, ok := r.(api.ApplyTimelineOps503JSONResponse); !ok {
		t.Errorf("ApplyTimelineOps = %T, want 503", r)
	}
	// Role and authentication come first.
	viewer := NewAPI(nil, caller(store.RoleViewer), nil)
	if r, _ := viewer.CreateProject(ctx, api.CreateProjectRequestObject{Body: body}); r == nil {
		t.Error("no answer")
	} else if _, ok := r.(api.CreateProject403JSONResponse); !ok {
		t.Errorf("viewer CreateProject = %T, want 403", r)
	}
	anon := NewAPI(nil, func(context.Context) (httpapi.Caller, bool) { return httpapi.Caller{}, false }, nil)
	if r, _ := anon.GetProject(ctx, api.GetProjectRequestObject{Id: id}); r == nil {
		t.Error("no answer")
	} else if _, ok := r.(api.GetProject401JSONResponse); !ok {
		t.Errorf("anonymous GetProject = %T, want 401", r)
	}
}
