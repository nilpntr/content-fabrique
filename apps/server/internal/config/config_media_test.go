package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mediaEnv(extra map[string]string) Getenv {
	base := map[string]string{"DATABASE_URL": "postgres://u:p@localhost:5432/db", "APP_BASE_URL": "https://app.example.com"}
	for k, v := range extra {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func TestMediaDefaults(t *testing.T) {
	c, err := Load(mediaEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.MediaSandboxProvider != SandboxDocker || c.MediaToolsImage != "media-tools:dev" || c.MediaConcurrency != 2 || c.RenderConcurrency != 2 ||
		c.MediaTempDir != filepath.Join(os.TempDir(), "cf-media") {
		t.Fatalf("%+v", c)
	}
	// Staging and production default to the hardened (gVisor) provider.
	for _, env := range []string{EnvStaging, EnvProduction} {
		c, err := Load(mediaEnv(map[string]string{"CF_ENV": env}))
		if err != nil || c.MediaSandboxProvider != SandboxHardened {
			t.Fatalf("%s: %q %v", env, c.MediaSandboxProvider, err)
		}
	}
}

func TestMediaSettings(t *testing.T) {
	c, err := Load(mediaEnv(map[string]string{
		"MEDIA_SANDBOX_PROVIDER": " Hardened ", "MEDIA_TOOLS_IMAGE": "registry.example.com/media-tools:1", "MEDIA_TEMP_DIR": "/var/lib/cf/media", "MEDIA_CONCURRENCY": "4", "RENDER_CONCURRENCY": "3",
	}))
	if err != nil || c.RenderConcurrency != 3 || c.MediaSandboxProvider != "hardened" || c.MediaToolsImage != "registry.example.com/media-tools:1" || c.MediaTempDir != "/var/lib/cf/media" || c.MediaConcurrency != 4 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestMediaLocalProviderIsRefusedWhereItMatters(t *testing.T) {
	for _, env := range []string{EnvLocal, EnvCI, EnvBench} {
		if c, err := Load(mediaEnv(map[string]string{"CF_ENV": env, "MEDIA_SANDBOX_PROVIDER": "local"})); err != nil || c.MediaSandboxProvider != SandboxLocal {
			t.Errorf("%s: %v", env, err)
		}
	}
	for _, env := range []string{EnvStaging, EnvProduction} {
		_, err := Load(mediaEnv(map[string]string{"CF_ENV": env, "MEDIA_SANDBOX_PROVIDER": "local"}))
		if err == nil || !strings.Contains(err.Error(), "MEDIA_SANDBOX_PROVIDER") || !strings.Contains(err.Error(), "isolates nothing") {
			t.Errorf("%s: %v", env, err)
		}
	}
}

func TestMediaInvalidSettings(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"MEDIA_SANDBOX_PROVIDER": {"MEDIA_SANDBOX_PROVIDER": "firecracker"},
		"MEDIA_TEMP_DIR":         {"MEDIA_TEMP_DIR": "relative/dir"},
		"MEDIA_CONCURRENCY":      {"MEDIA_CONCURRENCY": "0"},
		"RENDER_CONCURRENCY":     {"RENDER_CONCURRENCY": "0"},
	} {
		if _, err := Load(mediaEnv(env)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, v := range []string{"many", "-1", "33", "1.5"} {
		if _, err := Load(mediaEnv(map[string]string{"MEDIA_CONCURRENCY": v})); err == nil {
			t.Errorf("MEDIA_CONCURRENCY=%q was accepted", v)
		}
		if _, err := Load(mediaEnv(map[string]string{"RENDER_CONCURRENCY": v})); err == nil {
			t.Errorf("RENDER_CONCURRENCY=%q was accepted", v)
		}
	}
}
