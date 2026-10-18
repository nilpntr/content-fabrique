package editor

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/analysis"
	"github.com/nilpntr/content-fabrique/internal/formats"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
	"github.com/nilpntr/content-fabrique/internal/subject"
)

// Formats (docs/design/formats-v1.md): the editor picks the concept of the video first, from the format library, then edits. list_formats
// ranks the formats that fit this footage (from the clips and their analysis) by fit and by the creator's own use of them (detected on
// their reference videos), marks one or two the creator has not used as "new to you", and, when the creator asked for a new idea, the
// formats of the earlier versions as not for this edit. plan_edit records the format and why; check_edit's format_followed holds the
// edit to the format's beats.

// ToolListFormats is the list_formats tool.
const ToolListFormats = "list_formats"

// MaxFormatWhyChars bounds plan_edit's format_why.
const MaxFormatWhyChars = 140

// MaxAvoidFormats is how many formats of earlier versions a new idea avoids (the newest): after that many ideas the oldest may come back.
const MaxAvoidFormats = 4

// PlanFormat is the format of a plan: the card, its name for the creator, why it fits, the device or format it borrows from, whether it
// is new to the creator, and whether the creator asked for a new idea.
type PlanFormat struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Why  string `json:"why"`
	// Mix is the card whose device the video borrows ("" for none), MixName its name.
	Mix     string `json:"mix,omitempty"`
	MixName string `json:"mix_name,omitempty"`
	// NewToCreator says the creator's reference videos show no video in this format (false when their formats are not known).
	NewToCreator bool `json:"new_to_creator"`
	// NewIdea says this run is the creator's "try another idea": the format differs from the earlier versions' (Avoided).
	NewIdea bool     `json:"new_idea,omitempty"`
	Avoided []string `json:"avoided,omitempty"`
	// Library is the version of the format library the card is from.
	Library string `json:"library"`
}

type listFormatsArgs struct {
	All bool `json:"all,omitempty"`
}

// footageFormats is what fit reads of the project: its clips (kind, words, length, reps, a pose) and the brief.
func footageFormats(rows []store.ProjectFootageRow, sums map[ids.AssetID]analysis.Summary, brief string) formats.Footage {
	f := formats.Footage{Brief: brief}
	for _, r := range rows {
		c := formats.Clip{Kind: r.Kind, Words: r.Title + "\n" + r.Description}
		if r.DurationMs != nil {
			c.DurationMs = *r.DurationMs
		}
		if s, ok := sums[r.AssetID]; ok {
			c.Analysed, c.Reps = true, s.Reps
		}
		c.Pose = r.Kind == store.AssetKindVideo && subject.ShowsOffTheBody(r.Title, strings.Fields(r.Description))
		f.Clips = append(f.Clips, c)
	}
	return f
}

// formatFits ranks the library for the run's project: by the footage, the creator's formats and this edit's vibe, without the formats a
// new idea avoids.
func (d Deps) formatFits(ctx context.Context, rc runtime.RunContext) ([]formats.Fit, *formatContext, error) {
	pid, ws, err := project(rc)
	if err != nil {
		return nil, nil, err
	}
	det, perr := d.Projects.Get(ctx, actor(ws), pid)
	if perr != nil {
		return nil, nil, projectErr(perr)
	}
	rows, err := d.Store.ListProjectFootage(ctx, ws, pid)
	if err != nil {
		return nil, nil, fmt.Errorf("list footage: %w", err)
	}
	sums, err := d.analysisSummaries(ctx, ws, rows)
	if err != nil {
		return nil, nil, err
	}
	in, err := parseInput(rc.Input)
	if err != nil {
		return nil, nil, err
	}
	st := d.styleFor(ctx, ws, rc.Input)
	fc := &formatContext{input: in, creatorKnown: false}
	opts := formats.RankOptions{ContentType: LikelyContentType(contentWords(det.Project.Brief, rows, sums)), Novel: 2}
	if st != nil {
		opts.Vibe = st.Resolved.Vibe
		if p := st.Profile; p != nil && p.Formats != nil {
			opts.Creator = p.FormatShares()
			if opts.Creator == nil {
				opts.Creator = []formats.Share{}
			}
			fc.creatorKnown, fc.references = true, p.References
		}
	} else {
		opts.Vibe = VibeOr(in.Vibe)
	}
	if in.NewIdea {
		opts.Exclude = in.AvoidFormats
	}
	return formats.Default().Rank(footageFormats(rows, sums, det.Project.Brief), opts), fc, nil
}

// formatContext is what the format text needs besides the fits.
type formatContext struct {
	input        Input
	creatorKnown bool
	references   int
}

// listFormats is the list_formats tool.
func (d Deps) listFormats(ctx context.Context, rc runtime.RunContext, in listFormatsArgs) (runtime.Result, error) {
	fits, fc, err := d.formatFits(ctx, rc)
	if err != nil {
		return runtime.Result{}, err
	}
	return runtime.Result{Content: []llm.Block{llm.TextBlock(formatsText(fits, fc, in.All))}}, nil
}

// formatsText is the ranked library as the editor reads it.
func formatsText(fits []formats.Fit, fc *formatContext, all bool) string {
	var b strings.Builder
	b.WriteString("Formats for this footage, best first (fit from the clips and their analysis")
	if fc.creatorKnown {
		b.WriteString(", then the creator's own use of each, from their reference videos")
	}
	b.WriteString("). Pick one, then put it in plan_edit (format, format_why); its beats set the structure of the edit, the playbook the craft, and the creator's style and settings the look. At most one format NEW TO THE CREATOR per edit unless the creator asked for something new.\n")
	if fc.input.NewIdea {
		var names []string
		for _, id := range fc.input.AvoidFormats {
			names = append(names, cardName(id))
		}
		if len(names) > 0 {
			fmt.Fprintf(&b, "The creator asked for a NEW IDEA: the earlier versions used %s. Choose a different format that fits; a new format is welcome here.\n", strings.Join(names, " and "))
		} else {
			b.WriteString("The creator asked for a NEW IDEA: choose a clearly different concept from the current version.\n")
		}
	}
	if !fc.creatorKnown {
		b.WriteString("The creator's own formats are not known (no reference videos with their text read): rank by fit alone, nothing is marked new.\n")
	}
	b.WriteString("\n")
	n := 0
	var misfits, excluded []string
	for _, f := range fits {
		c := f.Card
		switch {
		case f.Excluded:
			excluded = append(excluded, fmt.Sprintf("%s (%s)", c.ID, c.Name))
			continue
		case !f.Fits:
			misfits = append(misfits, fmt.Sprintf("%s (needs %s)", c.ID, strings.Join(f.Missing, ", ")))
			if !all {
				continue
			}
		}
		n++
		var tags []string
		if c.Kind == formats.KindDevice {
			tags = append(tags, "a DEVICE to mix in with format_mix, not a whole idea")
		}
		if f.CreatorUses {
			tags = append(tags, fmt.Sprintf("the creator's own: %.0f %% of their videos", f.CreatorShare*100))
		}
		if f.Novel {
			tags = append(tags, "NEW TO THE CREATOR: a fresh idea for them")
		} else if f.New && f.Fits {
			tags = append(tags, "new to the creator")
		}
		if !f.Fits {
			tags = append(tags, "DOES NOT FIT: needs "+strings.Join(f.Missing, ", "))
		}
		head := fmt.Sprintf("%d. %s, %q, fit %.2f", n, c.ID, c.Name, f.FitScore)
		if len(tags) > 0 {
			head += " [" + strings.Join(tags, "; ") + "]"
		}
		b.WriteString(head + "\n")
		if len(f.Has) > 0 {
			fmt.Fprintf(&b, "   fits because the footage has %s.\n", strings.Join(f.Has, ", "))
		}
		b.WriteString(c.Describe())
		b.WriteString("\n")
	}
	if len(excluded) > 0 {
		fmt.Fprintf(&b, "Not for this edit (an earlier version used it, and the creator asked for a new idea): %s.\n", strings.Join(excluded, ", "))
	}
	if len(misfits) > 0 && !all {
		fmt.Fprintf(&b, "Do not fit this footage: %s. (all true lists them in full.)\n", strings.Join(misfits, "; "))
	}
	return b.String()
}

// IsNewIdea says whether a run's input is a new idea (an input that cannot be read is not).
func IsNewIdea(raw json.RawMessage) bool {
	in, err := parseInput(raw)
	return err == nil && in.NewIdea
}

// newIdeaNote is the opening's word on a new idea: the creator wants a differently-concepted version, not a revision of this one.
func newIdeaNote(avoid []string) string {
	var b strings.Builder
	b.WriteString("The creator asked for a new idea (\"Try another idea\"): make a new version of this video with a different concept, not a revision of the current one.")
	if len(avoid) > 0 {
		var names []string
		for _, id := range avoid {
			names = append(names, cardName(id))
		}
		fmt.Fprintf(&b, " The earlier versions used the format %s; choose a different format that fits this footage (list_formats marks them; plan_edit refuses them).", strings.Join(names, " and "))
	} else {
		b.WriteString(" Choose a format that is clearly different from the current version's structure (list_formats).")
	}
	b.WriteString(" Keep the creator's style and settings (the look, the title style, the pace, the music); change the idea: the hook, the structure, the text pattern where the format asks for one, and the payoff. Build it from scratch: you may reuse the clips, not the old cut.")
	return b.String()
}

// cardName is a card's name, or its id when the library has no such card.
func cardName(id string) string {
	if c, ok := formats.Default().Card(id); ok {
		return fmt.Sprintf("%s (%s)", c.Name, id)
	}
	return id
}

// contentWords are the words the content-type guess reads (the same as contentTypeHint's).
func contentWords(brief string, rows []store.ProjectFootageRow, sums map[ids.AssetID]analysis.Summary) string {
	var b strings.Builder
	b.WriteString(brief)
	for _, r := range rows {
		b.WriteString("\n" + r.Title + "\n" + r.Description)
	}
	reps := 0
	for _, s := range sums {
		reps += s.Reps
	}
	if reps >= 4 {
		b.WriteString(" workout workout")
	}
	return b.String()
}

// planFormat checks the format fields of a plan_edit call against the library, the run's input (a new idea may not reuse the formats of
// the earlier versions) and the fits (a format must fit the footage; a device is only a mix). It returns the plan's format and the notes
// for the answer.
func planFormat(in planArgs, run Input, fits []formats.Fit, creatorKnown bool) (*PlanFormat, []string, error) {
	lib := formats.Default()
	fail := func(format string, a ...any) (*PlanFormat, []string, error) {
		return nil, nil, runtime.UserErrorf("the plan is not valid: "+format, a...)
	}
	if in.Format == "" {
		return fail("format is empty: call list_formats and choose the format of this video first")
	}
	card, ok := lib.Card(in.Format)
	if !ok || card.Kind != formats.KindFormat {
		return fail("format %q is not one of the formats: %s", in.Format, strings.Join(lib.IDs(formats.KindFormat), ", "))
	}
	why := strings.TrimSpace(strings.ReplaceAll(in.FormatWhy, "\x00", ""))
	switch n := utf8.RuneCountInString(why); {
	case n == 0:
		return fail("format_why is empty: say in a sentence why %s fits this footage", in.Format)
	case n > MaxFormatWhyChars:
		return fail("format_why is %d characters; at most %d", n, MaxFormatWhyChars)
	}
	if run.NewIdea && slices.Contains(run.AvoidFormats, in.Format) {
		return fail("the creator asked for a new idea, and an earlier version of this project is %s already; choose another format that fits (list_formats marks them)", cardName(in.Format))
	}
	byID := map[string]formats.Fit{}
	for _, f := range fits {
		byID[f.Card.ID] = f
	}
	var notes []string
	// The fit is read from the clips' words and analysis, which can miss what a clip holds (an old photo named by a number): a misfit is a
	// note to look again, not a refusal.
	if f, ok := byID[in.Format]; ok && !f.Fits {
		notes = append(notes, fmt.Sprintf("list_formats does not see what %s needs in this footage (%s). Use it only if you have seen that the footage carries it; otherwise choose a format that fits.", in.Format, strings.Join(f.Missing, ", ")))
	}
	pf := &PlanFormat{ID: card.ID, Name: card.Name, Why: why, NewIdea: run.NewIdea, Library: lib.Version}
	if run.NewIdea {
		pf.Avoided = slices.Clone(run.AvoidFormats)
	}
	if f, ok := byID[in.Format]; ok && creatorKnown {
		pf.NewToCreator = f.New
	}
	if in.FormatMix != "" && in.FormatMix != "none" {
		mix, ok := lib.Card(in.FormatMix)
		if !ok {
			return fail("format_mix %q is not one of the cards: none, %s", in.FormatMix, strings.Join(lib.IDs(""), ", "))
		}
		if mix.ID == card.ID {
			return fail("format_mix is the format itself; leave it out or name another card")
		}
		pf.Mix, pf.MixName = mix.ID, mix.Name
		if f, ok := byID[mix.ID]; ok && creatorKnown && f.New && pf.NewToCreator && !run.NewIdea {
			notes = append(notes, fmt.Sprintf("Both %s and %s are new to the creator. Use at most one new format in an edit unless the creator asked for something new: keep the new one as the format and mix in a device or a format they use, or drop the mix.", card.ID, mix.ID))
		}
	}
	return pf, notes, nil
}

// formatGuide is the chosen format in the plan's answer: its beats against the plan, its text and music, and what format_followed
// checks.
func formatGuide(pf *PlanFormat, p Plan) string {
	card, ok := formats.Default().Card(pf.ID)
	if !ok {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nThe format: %s (%s)", card.Name, card.ID)
	if pf.NewToCreator {
		b.WriteString(", new to this creator")
	}
	if pf.Mix != "" {
		fmt.Fprintf(&b, ", with %s mixed in", pf.MixName)
	}
	b.WriteString(". Build its beats; check_edit's format_followed holds the edit to them (the hook, the text pattern, the payoff).\n")
	b.WriteString(card.Describe())
	if pf.Mix != "" {
		if mix, ok := formats.Default().Card(pf.Mix); ok {
			fmt.Fprintf(&b, "   mixed in, %s: %s\n", mix.Name, mix.Pitch)
			if pb, ok := mix.Beat("payoff"); ok && mix.Check.Loop {
				fmt.Fprintf(&b, "   its ending: %s\n", pb.What)
			}
		}
	}
	// The plan's shots against the beats: a payoff role is needed.
	payoff, _ := card.Beat("payoff")
	middle, _ := card.Beat("middle")
	has := func(roles []string) bool {
		return slices.ContainsFunc(p.Shots, func(s PlanShot) bool { return slices.Contains(roles, s.Role) })
	}
	if !has(append(slices.Clone(payoff.Roles), middle.Roles...)) {
		fmt.Fprintf(&b, "Note: the shot list has no shot for the format's middle or payoff (roles %s): give the payoff a shot of its own.\n", strings.Join(append(slices.Clone(middle.Roles), payoff.Roles...), ", "))
	}
	if len(p.Shots) < card.Check.MinShots {
		fmt.Fprintf(&b, "Note: the format needs at least %d shots; the plan has %d.\n", card.Check.MinShots, len(p.Shots))
	}
	if t := p.TargetLengthS; t < card.Length.Min || t > card.Length.Max {
		fmt.Fprintf(&b, "Note: the format runs %d to %d s; the target is %d s. Stay in the creator's length when it says otherwise.\n", card.Length.Min, card.Length.Max, t)
	}
	return b.String()
}

// ---- the formats of a project's earlier versions -------------------------------------------------------------------

// FormatHistory reads what ProjectFormats needs. *store.Store is the real one.
type FormatHistory interface {
	ListTimelineVersions(ctx context.Context, ws ids.WorkspaceID, project ids.ProjectID, before, limit int) ([]store.TimelineVersionBrief, error)
	EventSource
}

// ProjectFormats are the formats of the project's versions that editor runs made, newest first, each once, at most MaxAvoidFormats: what
// a new idea avoids. A version made by hand, or by a run from before formats, adds none.
func ProjectFormats(ctx context.Context, st FormatHistory, ws ids.WorkspaceID, pid ids.ProjectID) ([]string, error) {
	return ProjectFormatsUpTo(ctx, st, ws, pid, 0)
}

// ProjectFormatsUpTo is ProjectFormats over the versions up to upTo (0 for all): an eval that makes several new ideas from one stored
// version (--repeat) avoids the formats of the versions before it, not those of its own earlier samples, so the samples are alike.
func ProjectFormatsUpTo(ctx context.Context, st FormatHistory, ws ids.WorkspaceID, pid ids.ProjectID, upTo int) ([]string, error) {
	before := 0
	if upTo > 0 {
		before = upTo + 1 // ListTimelineVersions lists the versions below before
	}
	versions, err := st.ListTimelineVersions(ctx, ws, pid, before, 200)
	if err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for _, v := range versions {
		if v.AuthorKind != "agent" || seen[v.AuthorID] {
			continue
		}
		seen[v.AuthorID] = true
		run, err := ids.Decode(ids.PrefixRun, v.AuthorID)
		if err != nil {
			continue
		}
		plan, ok := PlanFromEvents(ctx, st, ws, uuid.UUID(run))
		if !ok || plan.Format == nil || slices.Contains(out, plan.Format.ID) {
			continue
		}
		out = append(out, plan.Format.ID)
		if len(out) == MaxAvoidFormats || len(seen) >= 12 {
			break
		}
	}
	return out, nil
}
