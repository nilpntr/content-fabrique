// Package envfile reads the repository's environment files.
//
// .env.example at the repository root is the single source of truth for every variable the stack reads. Each
// line carries a tag comment (see Tag) that says who provides the value and when. This package parses that file,
// parses .env-style files (the founder's .env, .env.staging and so on), and fills the generated secrets.
//
// Nothing here ever returns or logs a value in an error message.
package envfile

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ExampleFile is the name of the file that lists every variable.
const ExampleFile = ".env.example"

// Tag is a tag comment on a line of .env.example.
type Tag string

// The tags, as written between brackets in .env.example.
const (
	TagNow      Tag = "YOU-now"
	TagStaging  Tag = "YOU-staging"
	TagP6       Tag = "YOU-P6"
	TagP7       Tag = "YOU-P7"
	TagAuto     Tag = "AUTO"
	TagOptional Tag = "optional"
	TagPublic   Tag = "public"
)

// Var is one variable declared in .env.example.
type Var struct {
	Name string
	// Default is the value written in .env.example. Empty for the variables a person or `cfctl env init` fills.
	Default string
	// Tags are the bracketed tags of the line, in order.
	Tags []Tag
	// Comment is the text after the tags on the line.
	Comment string
}

var (
	nameRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	tagRE  = regexp.MustCompile(`\[([A-Za-z0-9-]+)\]`)
)

// assignment is one NAME=value line, split into its parts.
type assignment struct {
	name    string
	value   string // unquoted
	comment string // trailing comment including the leading '#', empty if none
	// valueEnd is the byte offset in the line where the value (and its quotes) ends, valueStart where it starts.
	valueStart, valueEnd int
}

// splitAssignment parses a NAME=value line. ok is false for blank lines, comment lines and anything else. The line
// may end in "\n" or "\r\n": the ending is not part of the value, and the offsets in the result are valid for the
// original line, so the caller can keep the ending when it rewrites the line.
func splitAssignment(line string) (a assignment, ok bool) {
	line = strings.TrimRight(line, "\r\n")
	trimmed := strings.TrimLeft(line, " \t")
	trimmed = strings.TrimPrefix(trimmed, "export ")
	trimmed = strings.TrimLeft(trimmed, " \t")
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return a, false
	}
	name, rest, found := strings.Cut(trimmed, "=")
	name = strings.TrimSpace(name)
	if !found || !nameRE.MatchString(name) {
		return a, false
	}
	a.name = name
	// Offsets are relative to the original line.
	offset := len(line) - len(rest)
	skip := len(rest) - len(strings.TrimLeft(rest, " \t"))
	rest = rest[skip:]
	offset += skip
	a.valueStart = offset

	switch {
	case strings.HasPrefix(rest, `"`) || strings.HasPrefix(rest, "'"):
		q := rest[:1]
		end := strings.Index(rest[1:], q)
		if end < 0 {
			// Unterminated quote: take the rest of the line literally.
			a.value = rest[1:]
			a.valueEnd = len(line)
			return a, true
		}
		a.value = rest[1 : 1+end]
		a.valueEnd = offset + end + 2
		tail := strings.TrimLeft(rest[end+2:], " \t")
		if strings.HasPrefix(tail, "#") {
			a.comment = tail
		}
	default:
		// An unquoted value ends at whitespace followed by '#'.
		end := len(rest)
		for i := 0; i < len(rest); i++ {
			if prev := line[offset+i-1]; rest[i] == '#' && (prev == ' ' || prev == '\t') {
				end = i
				a.comment = rest[i:]
				break
			}
		}
		a.value = strings.TrimRight(rest[:end], " \t")
		a.valueEnd = offset + len(a.value)
	}
	return a, true
}

// ParseExample reads .env.example.
func ParseExample(r io.Reader) ([]Var, error) {
	var vars []Var
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		a, ok := splitAssignment(sc.Text())
		if !ok {
			continue
		}
		if seen[a.name] {
			return nil, fmt.Errorf("%s: %s is declared twice", ExampleFile, a.name)
		}
		seen[a.name] = true
		v := Var{Name: a.name, Default: a.value}
		if a.comment != "" {
			text := strings.TrimSpace(strings.TrimPrefix(a.comment, "#"))
			// Tags come first on the comment: "[YOU-now] [public] pk_test_...".
			for {
				loc := tagRE.FindStringIndex(text)
				if loc == nil || loc[0] != 0 {
					break
				}
				v.Tags = append(v.Tags, Tag(text[1:loc[1]-1]))
				text = strings.TrimSpace(text[loc[1]:])
			}
			v.Comment = text
		}
		if len(v.Tags) == 0 {
			return nil, fmt.Errorf("%s: %s has no tag comment", ExampleFile, a.name)
		}
		vars = append(vars, v)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", ExampleFile, err)
	}
	return vars, nil
}

// Parse reads a .env-style file into a map. The last assignment of a name wins. A name with an empty value is
// present in the map with the value "".
func Parse(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if a, ok := splitAssignment(sc.Text()); ok {
			out[a.name] = a.value
		}
	}
	return out, sc.Err()
}

// ParseFile is Parse on a file path. The error never contains file contents.
func ParseFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	m, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return m, nil
}

// ErrNoRoot means no directory above the start contains .env.example.
var ErrNoRoot = errors.New("repository root not found: no parent directory contains " + ExampleFile)

// FindRoot walks up from start to the first directory that contains .env.example.
func FindRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if st, err := os.Stat(filepath.Join(dir, ExampleFile)); err == nil && !st.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrNoRoot
		}
		dir = parent
	}
}
