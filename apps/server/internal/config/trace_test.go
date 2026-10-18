package config

import (
	"strings"
	"testing"
)

// OTEL_TRACE_CONTENT: content capture is on only locally; ci and bench ignore it, staging and production refuse to start with it.
func TestTraceContent(t *testing.T) {
	for _, c := range []struct {
		env, val string
		want     bool
	}{
		{"local", "1", true}, {"local", "", false}, {"local", "0", false}, {"ci", "1", false}, {"bench", "1", false},
	} {
		cfg, err := Load(env(map[string]string{"CF_ENV": c.env, "OTEL_TRACE_CONTENT": c.val}))
		if err != nil || cfg.TraceContent != c.want {
			t.Errorf("CF_ENV=%s OTEL_TRACE_CONTENT=%q: TraceContent %v, %v", c.env, c.val, cfg.TraceContent, err)
		}
	}
	for _, e := range []string{"staging", "production"} {
		_, err := Load(env(map[string]string{"CF_ENV": e, "OTEL_TRACE_CONTENT": "1"}))
		if err == nil || !strings.Contains(err.Error(), "OTEL_TRACE_CONTENT") {
			t.Errorf("%s with content capture: %v", e, err)
		}
	}
	if _, err := Load(env(map[string]string{"OTEL_TRACE_CONTENT": "yes"})); err == nil || !strings.Contains(err.Error(), "OTEL_TRACE_CONTENT") {
		t.Errorf("a bad value: %v", err)
	}
}
