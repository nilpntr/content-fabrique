package main

import "testing"

func TestHelpIsNotAnError(t *testing.T) {
	for _, flagName := range []string{"-h", "--help", "-help"} {
		if err := run([]string{flagName}); err != nil {
			t.Errorf("run(%s) = %v, want nil", flagName, err)
		}
	}
}

func TestUnknownFlagIsAnError(t *testing.T) {
	if err := run([]string{"--nope"}); err == nil {
		t.Error("expected an error for an unknown flag")
	}
}
