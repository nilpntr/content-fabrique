package taxonomy

import (
	"strings"
	"unicode/utf8"
)

// MaxExerciseChars is the longest exercise name kept.
const MaxExerciseChars = 40

// Legacy is what the quick look stored before the vocabulary existed (quicklook.v3 and older): free-form tags, and a title that starts
// with the exercise ("Seated cable row, side view").
type Legacy struct {
	Tags []string
	// Title and FromQuickLook: the title counts only when the quick look wrote it (a person's title is theirs, not a label).
	Title         string
	FromQuickLook bool
}

// FromLegacy maps old free-form tags onto the vocabulary, without a model: every tag votes through the synonym map (Map), and the
// exercise name is the head of a title the quick look wrote when it names a movement ("Seated cable row" of "Seated cable row, side
// view"), else the first tag that names one in more than a word ("cable row"), else none.
func (t *Taxonomy) FromLegacy(l Legacy) (Set, string) {
	set := t.Map(l.Tags...)
	exercise := ""
	if l.FromQuickLook {
		if head := titleHead(l.Title); head != "" && t.namesMovement(head) {
			exercise = head
		}
	}
	if exercise == "" {
		for _, tag := range l.Tags {
			if len(strings.Fields(tag)) > 1 && t.namesMovement(tag) {
				exercise = strings.TrimSpace(tag)
				break
			}
		}
	}
	if utf8.RuneCountInString(exercise) > MaxExerciseChars {
		exercise = ""
	}
	if exercise != "" {
		// What the exercise says fills the facets the tags left empty (a "Lat pulldown" title with only "gym" as a tag is a pull).
		set = t.Build(set, exercise)
	}
	return set, exercise
}

// namesMovement says whether a text maps to a movement.
func (t *Taxonomy) namesMovement(text string) bool {
	for _, r := range t.Phrases(text) {
		if r.Facet == "movement" {
			return true
		}
	}
	return false
}

// titleHead is a title up to its first comma, dash or " in " ("Seated machine row, side view in a gym" is "Seated machine row").
func titleHead(title string) string {
	head := title
	for _, sep := range []string{",", " - ", " – ", " — ", " in ", " on ", " with ", ":", "("} {
		if i := strings.Index(strings.ToLower(head), sep); i > 0 {
			head = head[:i]
		}
	}
	return strings.TrimSpace(head)
}
