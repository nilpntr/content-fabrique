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
	"strings"
)

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
}

// Init makes sure envPath exists and has every generated secret.
//
// A missing envPath is copied from examplePath. Each secret is filled only if it is empty: a value is never
// overwritten. Every other line, comment and the order are preserved. The file is written with mode 0600, through a
// temporary file and a rename, so an interrupted run cannot leave a truncated file. A second run changes nothing.
// random is the entropy source; pass nil for crypto/rand.
func Init(examplePath, envPath string, random io.Reader) (InitResult, error) {
	if random == nil {
		random = rand.Reader
	}
	var res InitResult

	content, err := os.ReadFile(envPath)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		content, err = os.ReadFile(examplePath)
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
			line := lines[idx]
			eq := strings.Index(line, "=")
			after := line[last.valueEnd:]
			if after != "" && after[0] != ' ' && after[0] != '\t' && after[0] != '\n' && after[0] != '\r' {
				after = " " + after
			}
			lines[idx] = line[:eq+1] + value + after
		} else {
			if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
				lines[n-1] += "\n"
			}
			lines = append(lines, s.Name+"="+value+"\n")
		}
		res.Generated = append(res.Generated, s.Name)
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
