package editor

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/styleprofile"
	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// The creator's title look on motion graphics. A text title gets the creator's recipe from the prompt (add_text with the recipe's
// style, fill, scale, shadow and align); a graphic has the same params, and when the model leaves them out the template draws in its
// default style (title_bold: capitals of 3.2 % of a 9:16 frame where the creator's are 4.0 %, say). So edit_timeline fills the recipe
// into every add_graphic of a title template (title_pop, blur_in, chapter_card) that names no style of its own: the hook title and the
// chapter cards then have the creator's measured title height, colour and shadow. What the model set is kept. A graphic with a style
// of its own is the model's choice and is left alone (the recipe's scale belongs to the recipe's style).

// recipeTemplates are the templates that draw a title in the creator's title look.
var recipeTemplates = map[string]bool{timeline.TemplateTitlePop: true, timeline.TemplateBlurIn: true, timeline.TemplateChapterCard: true}

// applyTitleRecipe returns the ops with the recipe filled into the title graphics that name no style, and a note per op changed. Ops that
// are not such a graphic are passed through byte for byte; ops it cannot read are left for the op validator to refuse.
func applyTitleRecipe(raw json.RawMessage, rec *styleprofile.TitleRecipe) (json.RawMessage, []string) {
	if rec == nil || rec.Style == "" || len(raw) == 0 {
		return raw, nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return raw, nil
	}
	var notes []string
	changed := false
	for i, op := range list {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(op, &m); err != nil {
			continue
		}
		var kind, tpl string
		if json.Unmarshal(m["op"], &kind) != nil || kind != "add_graphic" || json.Unmarshal(m["template"], &tpl) != nil || !recipeTemplates[tpl] {
			continue
		}
		params := map[string]json.RawMessage{}
		if p, ok := m["params"]; ok {
			if err := json.Unmarshal(p, &params); err != nil {
				continue
			}
		}
		if _, ok := params["style"]; ok {
			continue
		}
		var set []string
		put := func(key string, v any, desc string) {
			if _, ok := params[key]; ok {
				return
			}
			b, _ := json.Marshal(v)
			params[key] = b
			set = append(set, desc)
		}
		put("style", rec.Style, "style "+rec.Style)
		if rec.Fill != "" {
			put("fill", rec.Fill, "fill "+rec.Fill)
		}
		if rec.Scale != 0 {
			put("scale", rec.Scale, fmt.Sprintf("scale %g", rec.Scale))
		}
		if rec.Shadow != "" {
			put("shadow", rec.Shadow, "shadow "+rec.Shadow)
		}
		if rec.Align != "" && rec.Align != style.AlignCenter {
			put("align", rec.Align, "align "+rec.Align)
		}
		pb, err := json.Marshal(params)
		if err != nil {
			continue
		}
		m["params"] = pb
		ob, err := json.Marshal(m)
		if err != nil {
			continue
		}
		list[i], changed = ob, true
		size := ""
		if rec.CapHeightPct > 0 {
			size = fmt.Sprintf(", capitals of about %.1f %% of the frame height", rec.CapHeightPct)
		}
		notes = append(notes, fmt.Sprintf("op %d (add_graphic %s) named no style, so it has the creator's title look: %s%s", i, tpl, strings.Join(set, ", "), size))
	}
	if !changed {
		return raw, nil
	}
	out, err := json.Marshal(list)
	if err != nil {
		return raw, nil
	}
	return out, notes
}
