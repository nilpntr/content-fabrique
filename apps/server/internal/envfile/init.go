package envfile

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// now is the clock for the "added by" header; tests replace it.
var now = time.Now

// Secret describes one [AUTO] variable that `cfctl env init` generates (docs/06-setup-and-credentials.md §3.7).
type Secret struct {
	Name string
	// Bytes is the number of random bytes.
	Bytes int
	// Encode renders the bytes as the variable's value.
	Encode func([]byte) string
}

// Secrets are the generated variables, in the order they are reported.
var Secrets = []Secret{
	// openssl rand -base64 32
	{Name: "SECRETS_MASTER_KEY", Bytes: 32, Encode: base64.StdEncoding.EncodeToString},
	// openssl rand -hex 32
	{Name: "SANDBOX_GATEWAY_TOKEN", Bytes: 32, Encode: hex.EncodeToString},
	// openssl rand -hex 16
	{Name: "RIVER_UI_PASSWORD", Bytes: 16, Encode: hex.EncodeToString},
}

// InitResult says what Init did. It carries names only, never values.
type InitResult struct {
	// Created is true when the env file did not exist and was copied from .env.example.
	Created bool
	// Generated are the secrets that were empty and now have a value.
	Generated []string
	// AlreadySet are the secrets that already had a value and were left alone.
	AlreadySet []string
	// Added are the [AUTO] variables with a default that were absent from the env file and were appended.
	Added []string
	// Filled are the [AUTO] variables with a default that were present but empty and now have the default.
	Filled []string
}

// Init makes sure envPath exists, has every generated secret and has every [AUTO] default.
//
// A missing envPath is copied from examplePath. Each secret is filled only if it is empty: a value is never
// overwritten. Every [AUTO] variable of examplePath with a non-empty default (DATABASE_MIGRATE_URL, say, which an
// older .env lacks) is appended with its example line, under a dated header, when absent, and filled in place when
// present but empty. Variables without an [AUTO] tag are never touched. Every other line, comment and the order are preserved. The file is written with mode 0600, through a
// temporary file and a rename, so an interrupted run cannot leave a truncated file. A second run changes nothing.
// random is the entropy source; pass nil for crypto/rand.
func Init(examplePath, envPath string, random io.Reader) (InitResult, error) {
	if random == nil {
		random = rand.Reader
	}
	var res InitResult

	content, err := os.ReadFile(envPath) //nolint:gosec // G304: operator-supplied path, see ParseFile
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		content, err = os.ReadFile(examplePath) //nolint:gosec // G304: operator-supplied path, see ParseFile
		if err != nil {
			return res, fmt.Errorf("read %s: %w", examplePath, err)
		}
		res.Created = true
	default:
		return res, fmt.Errorf("read %s: %w", envPath, err)
	}

	lines := strings.SplitAfter(string(content), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	// New lines use the file's own line ending.
	eol := "\n"
	if strings.Contains(string(content), "\r\n") {
		eol = "\r\n"
	}

	changed := res.Created
	for _, s := range Secrets {
		// The last assignment of a name is the effective one.
		idx := -1
		var last assignment
		for i, l := range lines {
			if a, ok := splitAssignment(l); ok && a.name == s.Name {
				idx, last = i, a
			}
		}
		if idx >= 0 && last.value != "" {
			res.AlreadySet = append(res.AlreadySet, s.Name)
			continue
		}
		buf := make([]byte, s.Bytes)
		if _, err := io.ReadFull(random, buf); err != nil {
			return res, fmt.Errorf("generate %s: %w", s.Name, err)
		}
		value := s.Encode(buf)
		if idx >= 0 {
			lines[idx] = fillValue(lines[idx], last, value)
		} else {
			lines = appendLine(lines, s.Name+"="+value, eol)
		}
		res.Generated = append(res.Generated, s.Name)
		changed = true
	}

	defaults, err := autoDefaults(examplePath)
	if err != nil {
		return res, err
	}
	header := false
	for _, d := range defaults {
		idx := -1
		var last assignment
		for i, l := range lines {
			if a, ok := splitAssignment(l); ok && a.name == d.name {
				idx, last = i, a
			}
		}
		switch {
		case idx >= 0 && last.value != "":
			continue
		case idx >= 0:
			lines[idx] = fillValue(lines[idx], last, d.rawValue)
			res.Filled = append(res.Filled, d.name)
		default:
			if !header {
				if n := len(lines); n > 0 && strings.TrimRight(lines[n-1], "\r\n") != "" {
					lines = appendLine(lines, "", eol)
				}
				lines = appendLine(lines, "# added by cfctl env init ("+now().Format("2006-01-02")+")", eol)
				header = true
			}
			lines = appendLine(lines, d.line, eol)
			res.Added = append(res.Added, d.name)
		}
		changed = true
	}

	if !changed {
		return res, nil
	}
	if err := writeFile0600(envPath, []byte(strings.Join(lines, ""))); err != nil {
		return res, fmt.Errorf("write %s: %w", envPath, err)
	}
	return res, nil
}

// exampleDefault is one [AUTO] variable of .env.example that has a non-empty default.
type exampleDefault struct {
	name string
	// rawValue is the value as written in the example, quotes included.
	rawValue string
	// line is the whole example line without its line ending, trailing comment included.
	line string
}

// autoDefaults returns the [AUTO] variables of examplePath with a non-empty default, in file order. The generated
// secrets are left out: Init makes those.
func autoDefaults(examplePath string) ([]exampleDefault, error) {
	content, err := os.ReadFile(examplePath) //nolint:gosec // G304: operator-supplied path, see ParseFile
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", examplePath, err)
	}
	vars, err := ParseExample(strings.NewReader(string(content)))
	if err != nil {
		return nil, err
	}
	auto := map[string]bool{}
	for _, v := range vars {
		if v.Default != "" && slices.Contains(v.Tags, TagAuto) {
			auto[v.Name] = true
		}
	}
	for _, s := range Secrets {
		delete(auto, s.Name)
	}
	var out []exampleDefault
	for _, l := range strings.Split(string(content), "\n") {
		a, ok := splitAssignment(l)
		if !ok || !auto[a.name] {
			continue
		}
		l = strings.TrimRight(l, "\r\n")
		out = append(out, exampleDefault{name: a.name, rawValue: l[a.valueStart:a.valueEnd], line: strings.TrimLeft(l, " \t")})
	}
	return out, nil
}

// fillValue sets the value of an existing assignment line, keeping its trailing comment and line ending.
func fillValue(line string, a assignment, value string) string {
	eq := strings.Index(line, "=")
	after := line[a.valueEnd:]
	if after != "" && after[0] != ' ' && after[0] != '\t' && after[0] != '\n' && after[0] != '\r' {
		after = " " + after
	}
	return line[:eq+1] + value + after
}

// appendLine adds text and a line ending, first ending a last line that has none.
func appendLine(lines []string, text, eol string) []string {
	if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
		lines[n-1] += eol
	}
	return append(lines, text+eol)
}

// writeFile0600 writes data to a temporary file beside path with mode 0600 and renames it into place.
func writeFile0600(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".env-init-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
