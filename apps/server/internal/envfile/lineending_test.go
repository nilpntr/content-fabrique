package envfile

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An empty secret is empty whatever follows the "=": a bare line ending, CRLF, or trailing spaces and tabs.
func TestInitFillsEmptiesInEveryLineEnding(t *testing.T) {
	cases := map[string]string{
		"LF":             "A=1\nSECRETS_MASTER_KEY=\nSANDBOX_GATEWAY_TOKEN=\nRIVER_UI_PASSWORD=\n",
		"CRLF":           "A=1\r\nSECRETS_MASTER_KEY=\r\nSANDBOX_GATEWAY_TOKEN=\r\nRIVER_UI_PASSWORD=\r\n",
		"trailing space": "A=1\nSECRETS_MASTER_KEY=   \nSANDBOX_GATEWAY_TOKEN=\t \nRIVER_UI_PASSWORD=  ",
		"CRLF and space": "A=1\r\nSECRETS_MASTER_KEY=  \r\nSANDBOX_GATEWAY_TOKEN= # [AUTO]\r\nRIVER_UI_PASSWORD=\"\"\r\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			example := filepath.Join(dir, ExampleFile)
			envPath := filepath.Join(dir, ".env")
			if err := os.WriteFile(example, []byte("A=1 # [AUTO]\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			res, err := Init(example, envPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Generated) != 3 || len(res.AlreadySet) != 0 {
				t.Fatalf("result = %+v, want all three generated", res)
			}
			m, err := ParseFile(envPath)
			if err != nil {
				t.Fatal(err)
			}
			if key, err := base64.StdEncoding.DecodeString(m["SECRETS_MASTER_KEY"]); err != nil || len(key) != 32 {
				t.Error("SECRETS_MASTER_KEY is not base64 of 32 bytes (a stray CR or space in the value?)")
			}
			if b, err := hex.DecodeString(m["SANDBOX_GATEWAY_TOKEN"]); err != nil || len(b) != 32 {
				t.Error("SANDBOX_GATEWAY_TOKEN is not hex of 32 bytes")
			}
			if b, err := hex.DecodeString(m["RIVER_UI_PASSWORD"]); err != nil || len(b) != 16 {
				t.Error("RIVER_UI_PASSWORD is not hex of 16 bytes")
			}
			got, _ := os.ReadFile(envPath)
			// The line ending style is kept: no bare LF in a CRLF file, no CR in an LF file.
			if strings.Contains(content, "\r\n") && strings.Contains(strings.ReplaceAll(string(got), "\r\n", ""), "\n") {
				t.Error("a CRLF file gained a bare LF")
			}
			if !strings.Contains(content, "\r") && strings.Contains(string(got), "\r") {
				t.Error("an LF file gained a CR")
			}
			// A second run changes nothing.
			res2, err := Init(example, envPath, nil)
			after, _ := os.ReadFile(envPath)
			if err != nil || len(res2.Generated) != 0 || !bytes.Equal(got, after) {
				t.Errorf("second run changed the file: %+v %v", res2, err)
			}
		})
	}
}

func TestParseIgnoresLineEndings(t *testing.T) {
	m, err := Parse(strings.NewReader("A=1\r\nB=\r\nC=x  \r\nD=\"q\"\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m["A"] != "1" || m["B"] != "" || m["C"] != "x" || m["D"] != "q" {
		t.Errorf("got %q", m)
	}
}
