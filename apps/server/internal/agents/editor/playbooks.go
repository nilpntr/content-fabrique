package editor

import (
	"bytes"
	"embed"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"unicode"
)

// Genre playbooks (D-28, docs/design/edit-style-v1.md sections 2 and 3): one prompt module per kind of video, in
// prompts/playbooks/<name>.v<N>.md, versioned like the system prompt (prompts/CHANGELOG.md).
//
// Prompt-cache placement. The system prompt and the tools are the first cache breakpoint, and they are the same for every run of
// this binary, so every project shares that cache entry. A playbook is not in it: putting the playbook of the most likely genre
// there would split the shared prefix into six that are each warm for a sixth of the runs, and putting all six there would make
// every call read five playbooks it does not use. The model picks the playbook with plan_edit after it has seen the footage, and
// the playbook arrives as that tool's result: part of the conversation, written to the cache once by the run's rolling
// breakpoint and read from it on every later call. The run context (the second breakpoint) holds, at most, a one-line suggestion
// of the likely content type (LikelyContentType) and never a playbook, and it is fixed for the run, so the prefix stays stable
// across a resume.

// The content types of a plan, which are also the playbooks.
const (
	TypeTalking  = "talking"
	TypeUnboxing = "unboxing_product"
	TypeWorkout  = "workout"
	TypeVlog     = "vlog"
	TypeTutorial = "tutorial"
	TypeGeneral  = "general"
)

// ContentTypes are the content types the plan may name, in the order the contract lists them.
var ContentTypes = []string{TypeTalking, TypeUnboxing, TypeWorkout, TypeVlog, TypeTutorial, TypeGeneral}

// playbookVersions maps a playbook to its current version, which is also the name of its file.
var playbookVersions = map[string]string{
	TypeTalking:  "talking.v3",
	TypeUnboxing: "unboxing_product.v3",
	TypeWorkout:  "workout.v7",
	TypeVlog:     "vlog.v3",
	TypeTutorial: "tutorial.v3",
	TypeGeneral:  "general.v3",
}

//go:embed prompts/playbooks/*.md
var playbookFS embed.FS

// PlaybookVersion is the version of a playbook ("workout.v5"), or "" for a name that is not a playbook.
func PlaybookVersion(name string) string { return playbookVersions[name] }

// ValidContentType reports whether name is one of the content types.
func ValidContentType(name string) bool { return slices.Contains(ContentTypes, name) }

// Playbook returns the text of a playbook with the sections of the features that do not exist cut out.
func Playbook(name string, f Features) (string, error) {
	v, ok := playbookVersions[name]
	if !ok {
		return "", fmt.Errorf("editor: %q is not a playbook", name)
	}
	raw, err := playbookFS.ReadFile("prompts/playbooks/" + v + ".md")
	if err != nil {
		return "", fmt.Errorf("editor: playbook %s: %w", v, err)
	}
	return renderTemplate(v, string(raw), f)
}

// renderTemplate runs a prompt file as a template over the features and tidies the whitespace the cut sections leave.
func renderTemplate(name, text string, f Features) (string, error) {
	t, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("editor: %s: %w", name, err)
	}
	var b bytes.Buffer
	if err := t.Execute(&b, f); err != nil {
		return "", fmt.Errorf("editor: %s: %w", name, err)
	}
	return tidy(b.String()), nil
}

var (
	trailingSpace = regexp.MustCompile(`[ \t]+\n`)
	blankRuns     = regexp.MustCompile(`\n{3,}`)
)

func tidy(s string) string {
	s = trailingSpace.ReplaceAllString(s, "\n")
	s = blankRuns.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// LikelyContentType guesses the content type from the words of the brief and the clips' titles and descriptions: a hint for the
// model, which decides. It returns "" unless one type is a clear winner. The text is the creator's, so it is only ever counted
// for words, never read as an instruction.
func LikelyContentType(text string) string {
	// Whole words only ("day in" is not in "today in"), and a phrase does not run across a clause: the text is cut at punctuation and
	// line ends first.
	var clauses []string
	for _, clause := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return strings.ContainsRune(".,;:!?()/\n\r\"\u2014\u2013\u2026\u201c\u201d\u2018\u2019", r)
	}) {
		words := strings.FieldsFunc(clause, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' })
		clauses = append(clauses, " "+strings.Join(words, " ")+" ")
	}
	best, bestScore, second := "", 0, 0
	for _, t := range ContentTypes {
		score := 0
		for _, kw := range typeKeywords[t] {
			for _, padded := range clauses {
				score += strings.Count(padded, " "+kw+" ") + strings.Count(padded, " "+kw+"s ")
			}
		}
		switch {
		case score > bestScore:
			best, bestScore, second = t, score, bestScore
		case score > second:
			second = score
		}
	}
	if bestScore < 2 || bestScore < 2*second {
		return ""
	}
	return best
}

var typeKeywords = map[string][]string{
	TypeWorkout: {"workout", "gym", "lift", "rep", "pulldown", "deadlift", "squat", "bench", "curl", "row", "pose", "posing", "physique",
		"bodybuilding", "training", "fitness", "exercise", "cable", "dumbbell", "barbell", "set of"},
	TypeUnboxing: {"unbox", "unboxing", "review", "product", "packaging", "reveal", "haul", "first look", "gadget"},
	TypeTalking:  {"talking", "opinion", "rant", "to camera", "story time", "storytime", "monologue", "podcast", "interview", "speech"},
	TypeVlog:     {"vlog", "day in", "trip", "travel", "morning routine", "grwm", "weekend", "holiday", "lifestyle"},
	TypeTutorial: {"tutorial", "how to", "how-to", "step by step", "recipe", "diy", "guide", "lesson", "ingredients"},
}
