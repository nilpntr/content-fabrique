package main

import "testing"

func TestImageHasFonts(t *testing.T) {
	for out, want := range map[string]bool{
		"sha256:0123abcd 1\n":          true,
		"sha256:0123abcd <no value>\n": false, // an image built before the fonts were added has no label
		"sha256:0123abcd 0\n":          false,
		"sha256:0123abcd\n":            false,
		"":                             false,
	} {
		if got := imageHasFonts(out); got != want {
			t.Errorf("imageHasFonts(%q) = %v, want %v", out, got, want)
		}
	}
}

func TestImageHasMotion(t *testing.T) {
	for out, want := range map[string]bool{
		"sha256:0123abcd 1 1\n":          true,
		"sha256:0123abcd 1 <no value>\n": false, // an image built before the motion-graphics renderer was added
		"sha256:0123abcd 1\n":            false,
		"":                               false,
	} {
		if got := imageHasMotion(out); got != want {
			t.Errorf("imageHasMotion(%q) = %v, want %v", out, got, want)
		}
	}
}
