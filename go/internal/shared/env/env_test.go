package env

import "testing"

func TestIsCI_NoEnvVars(t *testing.T) {
	for _, key := range CIEnvVars {
		t.Setenv(key, "")
	}
	if IsCI() {
		t.Error("IsCI should be false when no CI env vars are set")
	}
}

func TestIsCI_DetectsEachKnownVar(t *testing.T) {
	for _, key := range CIEnvVars {
		t.Run(key, func(t *testing.T) {
			for _, other := range CIEnvVars {
				t.Setenv(other, "")
			}
			t.Setenv(key, "1")
			if !IsCI() {
				t.Errorf("IsCI should be true when %s is set", key)
			}
		})
	}
}

func TestIsCI_IgnoresWhitespaceOnlyValues(t *testing.T) {
	for _, key := range CIEnvVars {
		t.Setenv(key, "")
	}
	t.Setenv("CI", "   ")
	if IsCI() {
		t.Error("IsCI should be false for whitespace-only CI value")
	}
}

func TestCrashReportDefaultsTrue(t *testing.T) {
	t.Setenv("WENDY_CRASHREPORT", "")
	if !CrashReport() {
		t.Error("CrashReport should default to true")
	}
}

func TestCrashReportDisabled(t *testing.T) {
	t.Setenv("WENDY_CRASHREPORT", "false")
	if CrashReport() {
		t.Error("CrashReport should be false when WENDY_CRASHREPORT=false")
	}
}

func TestNoBanner(t *testing.T) {
	t.Setenv("WENDY_NO_BANNER", "")
	if NoBanner() {
		t.Error("NoBanner should be false when unset")
	}
	t.Setenv("WENDY_NO_BANNER", "1")
	if !NoBanner() {
		t.Error("NoBanner should be true when set")
	}
}
