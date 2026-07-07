package commands

import (
	"testing"
)

func TestResolveAppStoreAPIBase(t *testing.T) {
	t.Setenv("WENDY_APPSTORE_API", "")
	if got := resolveAppStoreAPIBase("https://example.com/"); got != "https://example.com" {
		t.Errorf("flag precedence: got %q", got)
	}
	t.Setenv("WENDY_APPSTORE_API", "https://env.example.com/")
	if got := resolveAppStoreAPIBase(""); got != "https://env.example.com" {
		t.Errorf("env precedence: got %q", got)
	}
	t.Setenv("WENDY_APPSTORE_API", "")
	if got := resolveAppStoreAPIBase(""); got != defaultAppStoreAPIBase {
		t.Errorf("default: got %q", got)
	}
}
