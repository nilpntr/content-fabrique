package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The runner's outputs are new files in a fresh directory: one that is already there, or a symlink in its place (to a file outside the
// output directory), is refused and left untouched.
func TestWriteNewRefusesWhatIsThere(t *testing.T) {
	dir := t.TempDir()
	if err := writeNew(filepath.Join(dir, "f000000.png"), []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(filepath.Join(dir, "f000000.png"), []byte("b")); err == nil {
		t.Error("an existing file was written over")
	}
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "manifest.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(link, []byte("{}")); err == nil {
		t.Error("written through a symlink")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Errorf("the symlink's target changed: %q", b)
	}
}
