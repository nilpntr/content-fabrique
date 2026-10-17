package auth

import (
	"net/http"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/api"
)

// A track holds at most 500 items: the 501st clip is refused, and nothing of its batch is stored.
func TestTimelineItemCap(t *testing.T) {
	e := newAssetsEnv(t)
	a1 := e.readyAsset("ada", "one.mp4")
	p := e.newProject("ada", "9:16", a1)
	base := 0
	for batch := range 5 {
		batchOps := make([]map[string]any, 100)
		for i := range batchOps {
			batchOps[i] = addClip(a1, 0, 300) // 300 ms is the shortest shot: 500 of them are 150 s
		}
		e.call(http.MethodPost, projectPath(p, "/timelines"), "ada", map[string]any{"base_version": base, "ops": batchOps}, http.StatusCreated, nil)
		base = batch + 1
	}
	rec := e.do(http.MethodPost, projectPath(p, "/timelines"), "ada", map[string]any{"base_version": base, "ops": []map[string]any{addClip(a1, 0, 300)}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "timeline_too_large") {
		t.Fatalf("501st clip: %d %s", rec.Code, rec.Body)
	}
	var vs api.ListTimelineVersionsResponse
	e.call(http.MethodGet, projectPath(p, "/timelines"), "ada", nil, http.StatusOK, &vs)
	if len(vs.Items) != 5 {
		t.Fatalf("versions: %d, want 5", len(vs.Items))
	}
}
