package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
)

// minRedact is the shortest loaded value the Redactor blanks. Shorter values ("cf", "1", "eu") would blank
// ordinary words in the report.
const minRedact = 6

const redacted = "[redacted]"

// Redactor replaces every loaded variable value in a string. It is the second guard behind the rule that reasons
// are built from categories: if a value ever slipped into the output, it would not get out.
type Redactor struct {
	values []string // longest first
}

// NewRedactor collects the values to blank from vars. A value that is one of its variable's documented enum
// values (CF_ENV=staging) is not secret and is kept, so the report can name the profile and environment. The
// password and user of a URL value are blanked on their own too.
func NewRedactor(vars map[string]string) *Redactor {
	set := map[string]bool{}
	add := func(v string) {
		if len(v) >= minRedact {
			set[v] = true
		}
	}
	for name, val := range vars {
		if allowed, ok := enums[name]; ok && slices.Contains(allowed, val) {
			continue
		}
		add(val)
		if u, err := url.Parse(val); err == nil && u.User != nil {
			add(u.User.Username())
			if pw, ok := u.User.Password(); ok {
				add(pw)
			}
		}
	}
	r := &Redactor{}
	for v := range set {
		r.values = append(r.values, v)
	}
	slices.SortFunc(r.values, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	return r
}

// Redact blanks every collected value in s.
func (r *Redactor) Redact(s string) string {
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v, redacted)
	}
	return s
}

// Writer wraps w so everything written is redacted. Each Write must carry whole values, which holds for the
// one-shot renderers below.
func (r *Redactor) Writer(w io.Writer) io.Writer { return redactWriter{r: r, w: w} }

type redactWriter struct {
	r *Redactor
	w io.Writer
}

func (rw redactWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(rw.w, rw.r.Redact(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// WriteText prints the report for people.
func WriteText(w io.Writer, rep Report, envFile string) {
	_, _ = fmt.Fprintf(w, "cfctl doctor  env=%s  phase=%s  env-file=%s\n", rep.Env, rep.Phase, envFile)
	section := func(title, kind string) {
		_, _ = fmt.Fprintf(w, "\n%s\n", title)
		for _, x := range rep.Results {
			if x.Kind != kind {
				continue
			}
			_, _ = fmt.Fprintf(w, "  %-8s %-46s %s\n", x.Status, x.Name, x.Reason)
		}
	}
	section("Variables", "variable")
	section("Services", "service")
	_, _ = fmt.Fprintf(w, "\n%d ok, %d missing, %d invalid, %d skipped\n",
		rep.Count(OK), rep.Count(Missing), rep.Count(Invalid), rep.Count(Skipped))
	if rep.Failed() {
		_, _ = fmt.Fprintln(w, "FAILED")
	}
}

// WriteJSON prints the report for machines.
func WriteJSON(w io.Writer, rep Report, envFile string) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Report
		EnvFile string `json:"env_file"`
		Failed  bool   `json:"failed"`
	}{rep, envFile, rep.Failed()})
}
