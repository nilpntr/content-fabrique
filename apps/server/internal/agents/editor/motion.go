package editor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/agents/llm"
	"github.com/nilpntr/content-fabrique/internal/agents/runtime"
	"github.com/nilpntr/content-fabrique/internal/motion"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// ToolListMotionTemplates lists the motion-graphics templates (D-35, docs/design/motion-graphics-v1.md section 9).
const ToolListMotionTemplates = "list_motion_templates"

// motionPreviewPrefix is the reference of a template's still preview: ImageResolver serves it from the binary (the stills are
// rendered by the motion package's integration test), never from storage, and only for a template of the library.
const motionPreviewPrefix = "builtin/motion-previews/"

// motionPreviewRef is the image reference of a template's still.
func motionPreviewRef(template string) string { return motionPreviewPrefix + template + ".png" }

// builtinImage returns the bytes of a built-in picture reference: exactly the still of a template of the library.
func builtinImage(ref string) ([]byte, bool) {
	for _, t := range motion.Templates() {
		if ref == motionPreviewRef(t.Name) {
			return motion.PreviewPNG(t.Name)
		}
	}
	return nil, false
}

type listMotionArgs struct {
	Template string `json:"template"`
}

func (d Deps) listMotionTemplates(_ context.Context, _ runtime.RunContext, in listMotionArgs) (runtime.Result, error) {
	if in.Template != "" {
		if _, ok := motion.Lookup(in.Template); !ok {
			return runtime.Result{}, runtime.UserErrorf("there is no template %q; the templates are %s", in.Template, strings.Join(timeline.GraphicsTemplates(), ", "))
		}
	}
	blocks := []llm.Block{llm.TextBlock(motionTemplatesText())}
	if in.Template != "" {
		if _, ok := motion.PreviewPNG(in.Template); ok {
			blocks = append(blocks, llm.TextBlock(fmt.Sprintf("A still of %s, fully on screen (an example, over grey):", in.Template)),
				llm.Block{Type: llm.BlockImage, Image: &llm.Image{MediaType: "image/png", Ref: motionPreviewRef(in.Template)}})
		}
	}
	return runtime.Result{Content: blocks}, nil
}

// motionTemplatesText is the library as the editor reads it: what each template does and takes, then how to time them.
func motionTemplatesText() string {
	var b strings.Builder
	b.WriteString("Motion-graphics templates (add them with the add_graphic op, change them with update_graphic, remove them with remove_graphic).\n")
	b.WriteString("Every template takes text, and optionally style (a text style id: use the creator's title style), fill and accent (#RRGGBB), scale (0.6 to 1.4), align, shadow, backing (plate, for a busy or bright picture), energy (calm, lively the default, hype) and accents_ms (timeline times inside the item, strictly increasing: put them on beats). lower_label takes no shadow or backing. The item is placed like a text item with the same anchor or position: inside the safe zone, off the person's head, and moved off the body when it can be. Give it a duration that leaves it on screen, fully entered, for at least 1.2 s before its exit.\n\n")
	for _, t := range motion.Templates() {
		ex, _ := json.Marshal(t.Example)
		fmt.Fprintf(&b, "%s (v%d): %s\n  Use: %s\n  Params: %s\n  Accents: %s\n  Example params: %s\n  Without motion graphics it is drawn as plain text with the %s animation.\n\n",
			t.Name, t.Version, t.Description, t.Use, t.Params, t.Accents, ex, t.Fallback)
	}
	b.WriteString("Timing by energy (an element's entrance, the gap between elements that no accent places, the exit at the end of the item):\n")
	for _, e := range []string{timeline.EnergyCalm, timeline.EnergyLively, timeline.EnergyHype} {
		f := motion.Feels[e]
		fmt.Fprintf(&b, "  %s: %d ms in, %d ms apart, %d ms out\n", e, f.DurMs, f.StaggerMs, f.ExitMs)
	}
	b.WriteString("An accent places an element so that it lands (its peak) on the accent: a title_pop word lands on its beat. check_edit reports how long each graphic is readable and whether the letters stand out from the picture behind them.\n")
	return b.String()
}
