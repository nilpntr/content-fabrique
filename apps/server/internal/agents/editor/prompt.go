package editor

import (
	_ "embed" // the prompt is embedded
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/nilpntr/content-fabrique/internal/formats"
)

// PromptVersion names the system prompt below. It is stored in every run's input (Input.PromptVersion), so a run can be tied to
// the text it was given. prompts/CHANGELOG.md says what each version changed.
const PromptVersion = "editor.v2.11"

//go:embed prompts/editor.v2.11.md
var systemPromptTemplate string

// systemPrompt is the prompt for the features of this build, rendered once: the sections about an effect or a look that the build
// does not have are cut out (features.go), and nothing else varies, so it is the same for every run and the prompt cache works
// across runs and projects.
var systemPrompt = sync.OnceValue(func() string {
	s, err := SystemPromptFor(AvailableFeatures())
	if err != nil {
		panic(err) // the prompt is embedded and tested
	}
	return s
})

// SystemPrompt is the editor's system prompt (the runtime puts its preamble in front).
func SystemPrompt() string { return systemPrompt() }

// SystemPromptFor renders the prompt for a set of features (tests use it to see what turning a feature on adds).
func SystemPromptFor(f Features) (string, error) {
	return renderTemplate(PromptVersion, systemPromptTemplate, f)
}

// MaxInstructionsChars is the longest extra instruction a run may carry.
const MaxInstructionsChars = 2000

// Input is the input of an editor run (agent_runs.input).
type Input struct {
	// Instructions is the creator's extra instruction for this run, free text: untrusted data.
	Instructions string `json:"instructions,omitempty"`
	// PromptVersion is the version of the system prompt the run was started with.
	PromptVersion string `json:"prompt_version"`
	// Vibe is the energy of the edit (calm, lively or hype, P6-06); empty is the workspace's style's (lively when it has none).
	Vibe string `json:"vibe,omitempty"`
	// NewIdea says the creator asked for another idea ("Try another idea"): a new version in a different format. AvoidFormats are the
	// formats of the project's earlier versions (ProjectFormats), which this run may not use.
	NewIdea      bool     `json:"new_idea,omitempty"`
	AvoidFormats []string `json:"avoid_formats,omitempty"`
}

// EditOptions are what a run is started with: the creator's instructions and vibe, and a new idea with the formats it avoids.
type EditOptions struct {
	Instructions, Vibe string
	NewIdea            bool
	AvoidFormats       []string
}

// NewInput returns the input for a run with the workspace's vibe, and checks the instructions.
func NewInput(instructions string) (json.RawMessage, error) { return NewEditInput(instructions, "") }

// NewEditInput returns the input for a run, and checks the instructions and the vibe ("" is the workspace's).
func NewEditInput(instructions, vibe string) (json.RawMessage, error) {
	return NewEditInputWith(EditOptions{Instructions: instructions, Vibe: vibe})
}

// NewEditInputWith returns the input for a run, and checks it: the instructions, the vibe, and the formats a new idea avoids (ids of
// the library, at most MaxAvoidFormats; an id the library does not have is dropped).
func NewEditInputWith(o EditOptions) (json.RawMessage, error) {
	instructions := strings.TrimSpace(o.Instructions)
	if err := checkInstructions(instructions); err != nil {
		return nil, err
	}
	if o.Vibe != "" && !ValidVibe(o.Vibe) {
		return nil, fmt.Errorf("editor: the vibe must be one of %v", Vibes)
	}
	in := Input{Instructions: instructions, PromptVersion: PromptVersion, Vibe: o.Vibe, NewIdea: o.NewIdea}
	if o.NewIdea {
		in.AvoidFormats = knownFormats(o.AvoidFormats)
	}
	return json.Marshal(in)
}

// knownFormats keeps the ids of the library's formats, each once, at most MaxAvoidFormats.
func knownFormats(list []string) []string {
	var out []string
	for _, id := range list {
		if c, ok := formats.Default().Card(id); ok && c.Kind == formats.KindFormat && !slices.Contains(out, id) && len(out) < MaxAvoidFormats {
			out = append(out, id)
		}
	}
	return out
}

func checkInstructions(s string) error {
	switch {
	case !utf8.ValidString(s) || strings.ContainsRune(s, 0):
		return fmt.Errorf("editor: the instructions are not valid text")
	case utf8.RuneCountInString(s) > MaxInstructionsChars:
		return fmt.Errorf("editor: the instructions are over %d characters", MaxInstructionsChars)
	}
	return nil
}

func parseInput(raw json.RawMessage) (Input, error) {
	var in Input
	if len(raw) == 0 {
		return in, nil
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return Input{}, fmt.Errorf("editor: the run input is not an editor input: %w", err)
	}
	in.AvoidFormats = knownFormats(in.AvoidFormats)
	return in, checkInstructions(in.Instructions)
}
