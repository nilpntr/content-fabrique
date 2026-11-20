package config

import (
	"strings"
	"testing"
	"time"
)

func TestResearchRetentionDefaultsAndSettings(t *testing.T) {
	c, err := Load(mediaEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.ResearchRetention.Default != 365*24*time.Hour || c.ResearchRetention.YouTube != 30*24*time.Hour {
		t.Fatalf("defaults: %+v", c.ResearchRetention)
	}
	c, err = Load(mediaEnv(map[string]string{"RESEARCH_RETENTION_DAYS": "180", "RESEARCH_YOUTUBE_RETENTION_DAYS": "14"}))
	if err != nil || c.ResearchRetention.Default != 180*24*time.Hour || c.ResearchRetention.YouTube != 14*24*time.Hour {
		t.Fatalf("set: %+v %v", c.ResearchRetention, err)
	}
}

// A retention out of range stops the process: third-party data must not be kept longer than the operator wrote down.
func TestResearchRetentionIsValidated(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"RESEARCH_RETENTION_DAYS":                {"RESEARCH_RETENTION_DAYS": "0"},
		"RESEARCH_RETENTION_DAYS high":           {"RESEARCH_RETENTION_DAYS": "5000"},
		"RESEARCH_RETENTION_DAYS words":          {"RESEARCH_RETENTION_DAYS": "a year"},
		"RESEARCH_YOUTUBE_RETENTION_DAYS":        {"RESEARCH_YOUTUBE_RETENTION_DAYS": "31"},
		"RESEARCH_YOUTUBE_RETENTION_DAYS 365":    {"RESEARCH_YOUTUBE_RETENTION_DAYS": "365"},
		"RESEARCH_YOUTUBE_RETENTION_DAYS negate": {"RESEARCH_YOUTUBE_RETENTION_DAYS": "-1"},
	} {
		_, err := Load(mediaEnv(env))
		if err == nil || !strings.Contains(err.Error(), strings.Fields(name)[0]) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
