package config

import (
	"flag"
	"os"
	"testing"
)

func setEnv(t *testing.T, k, v string) {
	t.Helper()
	old, ok := os.LookupEnv(k)
	if err := os.Setenv(k, v); err != nil {
		t.Fatalf("setenv %s: %v", k, err)
	}
	t.Cleanup(func() {
		if ok {
			os.Setenv(k, old)
		} else {
			os.Unsetenv(k)
		}
	})
}

func resetFlags() {
	flag.CommandLine = flag.NewFlagSet("test", flag.ContinueOnError)
}

func TestBuildLabelsDefaultsHostname(t *testing.T) {
	resetFlags()
	l := buildLabels(map[string]string{})
	if l["hostname"] == "" {
		t.Fatal("expected default hostname label to be set")
	}
	if l["hostname"] == "" {
		t.Errorf("hostname label is empty")
	}
}

func TestBuildLabelsHostnameOverride(t *testing.T) {
	resetFlags()
	l := buildLabels(map[string]string{"hostname": "my-custom-host"})
	if l["hostname"] != "my-custom-host" {
		t.Errorf("hostname = %q, want my-custom-host", l["hostname"])
	}
}

func TestBuildLabelsCustomLabels(t *testing.T) {
	resetFlags()
	l := buildLabels(map[string]string{"env": "prod", "region": "ap-southeast-1"})
	if l["env"] != "prod" {
		t.Errorf("env = %q, want prod", l["env"])
	}
	if l["region"] != "ap-southeast-1" {
		t.Errorf("region = %q, want ap-southeast-1", l["region"])
	}
	if l["hostname"] == "" {
		t.Error("expected hostname to still be auto-added when not overridden")
	}
}

func TestBuildLabelsEnvMergedAndOverridden(t *testing.T) {
	resetFlags()
	setEnv(t, "RTMP_LABELS", "env=staging, region=eu, hostname=env-host")
	l := buildLabels(map[string]string{"env": "prod"})
	// CLI --label overrides env.
	if l["env"] != "prod" {
		t.Errorf("env = %q, want prod (CLI overrides env)", l["env"])
	}
	// Env-only label is kept.
	if l["region"] != "eu" {
		t.Errorf("region = %q, want eu", l["region"])
	}
	// Env hostname is kept (user set it, so no auto-detect).
	if l["hostname"] != "env-host" {
		t.Errorf("hostname = %q, want env-host", l["hostname"])
	}
}

func TestBuildLabelsIgnoresInvalidEnvKeys(t *testing.T) {
	resetFlags()
	setEnv(t, "RTMP_LABELS", "1bad=val,good=ok,")
	l := buildLabels(nil)
	if _, ok := l["1bad"]; ok {
		t.Error("invalid label key '1bad' should have been dropped")
	}
	if l["good"] != "ok" {
		t.Errorf("good = %q, want ok", l["good"])
	}
}

func TestInvalidLabelKey(t *testing.T) {
	cases := map[string]bool{
		"":          true, // empty is invalid
		"hostname":  false,
		"dest_ip":   false,
		"a":         false,
		"_priv":     false,
		"1bad":      true, // starts with digit
		"has space": true,
		"dash-key":  true,
	}
	for k, wantInvalid := range cases {
		if got := invalidLabelKey(k); got != wantInvalid {
			t.Errorf("invalidLabelKey(%q) = %v, want %v", k, got, wantInvalid)
		}
	}
}
