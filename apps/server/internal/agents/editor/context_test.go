package editor

import (
	"context"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// The run context is stored as one event of at most 64 KB. Whatever the creator wrote, it is built to fit, with the creator's text
// cut harder step by step; the tools still return all of it.

const eventLimit = store.MaxEventPayloadBytes

func contextText(t *testing.T, e *env, prj ids.ProjectID) (string, int) {
	t.Helper()
	ag, err := NewAgent(e.deps)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := ag.Context(context.Background(), e.runCtx(e.wsA, prj, ""))
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, b := range blocks {
		sb.WriteString(b.Text + "\n")
	}
	return sb.String(), encodedSize(blocks)
}

func TestContextFitsFiftyClipsOfCJKText(t *testing.T) {
	e := newEnv(t)
	title := strings.Repeat("背", 250)              // the longest title the table allows, 3 bytes a character
	desc := strings.Repeat("広い体育館のラットプルダウン。", 400) // a description of thousands of characters
	brief := strings.Repeat("背中の日のショート動画。", 333)   // 3,996 characters
	var assets []ids.AssetID
	for i := range store.MaxProjectAssets {
		assets = append(assets, e.asset(e.wsA, clip{title: title, desc: desc, durMs: int64(5000 + i)}))
	}
	prj := e.project(e.wsA, "9:16", brief, assets...)
	text, size := contextText(t, e, prj)
	if size > maxContextBytes || size >= eventLimit*3/4 {
		t.Fatalf("the context is %d bytes; the budget is %d and an event holds %d", size, maxContextBytes, eventLimit)
	}
	for _, a := range assets {
		if !strings.Contains(text, a.String()) {
			t.Fatalf("clip %s is missing from the context", a)
		}
	}
	if !strings.Contains(text, "list_footage has them whole") {
		t.Error("the context does not say that text was shortened")
	}
	// The tools return all of it.
	list := textOf(e.ok(e.runCtx(e.wsA, prj, ""), ToolListFootage, `{}`))
	if !strings.Contains(list, title) || !strings.Contains(list, desc) {
		t.Error("list_footage does not return the whole title and description")
	}
	if b := textOf(e.ok(e.runCtx(e.wsA, prj, ""), ToolGetBrief, `{}`)); !strings.Contains(b, brief[:30]) {
		t.Error("get_brief does not return the brief")
	}
}

// Text that grows when it is encoded (quotes, backslashes, control characters) is counted after encoding.
func TestContextCountsEncodedBytes(t *testing.T) {
	e := newEnv(t)
	nasty := strings.Repeat("\"\\\n\t", 600)
	var assets []ids.AssetID
	for range 50 {
		assets = append(assets, e.asset(e.wsA, clip{title: "t", desc: nasty}))
	}
	prj := e.project(e.wsA, "9:16", nasty, assets...)
	_, size := contextText(t, e, prj)
	if size > maxContextBytes {
		t.Fatalf("the context is %d bytes", size)
	}
}

// A small project keeps its text: the first step is used.
func TestContextKeepsASmallProjectsText(t *testing.T) {
	e := newEnv(t)
	a := e.asset(e.wsA, clip{title: "Lat pulldown", desc: "Top set, 8 reps."})
	prj := e.project(e.wsA, "9:16", "Short brief.", a)
	text, _ := contextText(t, e, prj)
	for _, want := range []string{"Lat pulldown", "Top set, 8 reps.", "Short brief."} {
		if !strings.Contains(text, want) {
			t.Errorf("the context lacks %q", want)
		}
	}
	if strings.Contains(text, "shortened") || strings.Contains(text, "cut short") {
		t.Errorf("a small project was cut:\n%s", text)
	}
}

// Every step of the budget fits the worst case the table allows, so the last one never fails for lack of room.
func TestContextLastStepAlwaysFits(t *testing.T) {
	dur, w, h := int64(1_000_000), int32(1920), int32(1080)
	yes := true
	rows := make([]store.ProjectFootageRow, store.MaxProjectAssets)
	for i := range rows {
		rows[i] = store.ProjectFootageRow{AssetID: newAssetID(), Kind: "video", Title: strings.Repeat("背", 255), Description: strings.Repeat("\"", 100_000), DurationMs: &dur, Width: &w, Height: &h, HasAudio: &yes}
	}
	last := contextSteps[len(contextSteps)-1]
	blocks := contextBlocks("9:16", strings.Repeat("背", 4000), rows, "Timeline: none yet (version 0).", last)
	if size := encodedSize(blocks); size > maxContextBytes {
		t.Fatalf("the last step is %d bytes", size)
	}
	for i := 1; i < len(contextSteps); i++ {
		a, b := contextSteps[i-1], contextSteps[i]
		if b.briefChars >= a.briefChars || b.titleChars > a.titleChars || b.descChars > a.descChars || b.detailed > a.detailed {
			t.Errorf("step %d does not cut harder than step %d", i, i-1)
		}
	}
}
