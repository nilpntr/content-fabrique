package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestLoadAppOrigin(t *testing.T) {
	tests := []struct{ env, base, want string }{
		{"", "", "http://localhost:3000"}, // a developer machine needs nothing set
		{"local", "http://localhost:3000", "http://localhost:3000"},
		{"local", "http://localhost:3000/", "http://localhost:3000"},
		{"local", "HTTP://LocalHost:3000", "http://localhost:3000"},
		{"staging", "https://staging.contentfabrique.app", "https://staging.contentfabrique.app"},
		{"production", "https://contentfabrique.app:443/", "https://contentfabrique.app"},
	}
	for _, tc := range tests {
		c, err := Load(env(map[string]string{"CF_ENV": tc.env, "APP_BASE_URL": tc.base}))
		if err != nil || c.AppOrigin != tc.want {
			t.Errorf("%s %q: AppOrigin = %q, %v; want %q", tc.env, tc.base, c.AppOrigin, err, tc.want)
		}
	}

	bad := []struct{ env, base string }{
		{"staging", ""}, // never a silent default outside a developer machine
		{"production", ""},
		{"production", "http://contentfabrique.app"},
		{"local", "contentfabrique.app"},
		{"local", "ftp://localhost"},
		{"local", "http://localhost:3000/app"},
		{"local", "http://localhost:3000?x=1"},
		{"local", "http://user:pw@localhost:3000"},
	}
	for _, tc := range bad {
		_, err := Load(env(map[string]string{"CF_ENV": tc.env, "APP_BASE_URL": tc.base}))
		if err == nil || !strings.Contains(err.Error(), "APP_BASE_URL") {
			t.Errorf("%s %q: err = %v, want an APP_BASE_URL error", tc.env, tc.base, err)
		}
	}
}

func TestLoadClerkSecretsAreChecked(t *testing.T) {
	c, err := Load(env(map[string]string{"CLERK_SECRET_KEY": "sk_test_abc", "CLERK_WEBHOOK_SECRET": "whsec_abc"}))
	if err != nil || c.ClerkSecretKey != "sk_test_abc" || c.ClerkWebhookSecret != "whsec_abc" {
		t.Fatalf("clerk config = %v, %v", c, err)
	}
	// Both are optional: the ci environment holds none.
	if c, err := Load(env(nil)); err != nil || c.ClerkSecretKey != "" || c.ClerkWebhookSecret != "" {
		t.Fatalf("empty clerk config = %v, %v", c, err)
	}
	const secret = "hunter2-not-a-clerk-key"
	for _, name := range []string{"CLERK_SECRET_KEY", "CLERK_WEBHOOK_SECRET"} {
		_, err := Load(env(map[string]string{name: secret}))
		if err == nil || !strings.Contains(err.Error(), name) || strings.Contains(err.Error(), secret) {
			t.Errorf("%s: err = %v, want an error that names the variable and not the value", name, err)
		}
	}
}

func TestConfigNeverPrintsSecrets(t *testing.T) {
	c, err := Load(env(map[string]string{"CLERK_SECRET_KEY": "sk_test_SECRETVALUE", "CLERK_WEBHOOK_SECRET": "whsec_SECRETVALUE"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{fmt.Sprint(c), fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprintf("%v", &c)} {
		if strings.Contains(out, "SECRETVALUE") || strings.Contains(out, "cf:cf@") {
			t.Errorf("config output leaks a secret: %s", out)
		}
	}
}
