package editor

import (
	_ "embed" // the prompt is embedded
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// PromptVersion names the system prompt below. It is stored in every run's input (Input.PromptVersion), so a run can be tied to
// the text it was given. prompts/CHANGELOG.md says what each version changed.
const PromptVersion = "editor.v1.2"

//go:embed prompts/editor.v1.2.md
var systemPrompt string

// SystemPrompt is the editor's system prompt (the runtime puts its preamble in front). It is the same for every run, so the
// prompt cache works across runs.
func SystemPrompt() string { return strings.TrimSpace(systemPrompt) }

// MaxInstructionsChars is the longest extra instruction a run may carry.
const MaxInstructionsChars = 2000

// Input is the input of an editor run (agent_runs.input).
type Input struct {
	// Instructions is the creator's extra instruction for this run, free text: untrusted data.
	Instructions string `json:"instructions,omitempty"`
	// PromptVersion is the version of the system prompt the run was started with.
	PromptVersion string `json:"prompt_version"`
}

// NewInput returns the input for a run, and checks the instructions.
func NewInput(instructions string) (json.RawMessage, error) {
	instructions = strings.TrimSpace(instructions)
	if err := checkInstructions(instructions); err != nil {
		return nil, err
	}
	return json.Marshal(Input{Instructions: instructions, PromptVersion: PromptVersion})
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
	return in, checkInstructions(in.Instructions)
}
