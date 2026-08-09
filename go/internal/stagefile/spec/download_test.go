package spec

import (
	"strings"
	"testing"
)

var validDigest = strings.Repeat("ab", 32)

func fileWithDownloads(ds ...Download) *File {
	return &File{Version: 1, Stages: []Stage{{Name: "app", From: "debian:12", Download: ds}}}
}

func TestValidateAcceptsAMinimalDownload(t *testing.T) {
	f := fileWithDownloads(Download{URL: "https://example.com/m.onnx", Dest: "/app/m.onnx"})
	if err := f.Validate(); err != nil {
		t.Fatalf("an unpinned download is valid source (the lockfile pins it): %v", err)
	}
}

func TestValidateAcceptsBothDigestForms(t *testing.T) {
	for _, sha := range []string{validDigest, "sha256:" + validDigest, strings.ToUpper(validDigest)} {
		f := fileWithDownloads(Download{URL: "https://example.com/m", SHA256: sha, Dest: "/m"})
		if err := f.Validate(); err != nil {
			t.Fatalf("sha256 %q: %v", sha, err)
		}
	}
}

func TestValidateRejectsBadDownloads(t *testing.T) {
	tests := []struct {
		name string
		d    Download
		want string
	}{
		{"no url", Download{Dest: "/m"}, "url"},
		{"non-http url", Download{URL: "ftp://example.com/m", Dest: "/m"}, "http"},
		{"url with whitespace", Download{URL: "https://example.com/a b", Dest: "/m"}, "whitespace"},
		{"short sha256", Download{URL: "https://example.com/m", SHA256: "abc123", Dest: "/m"}, "64-hex-digit"},
		{"non-hex sha256", Download{URL: "https://example.com/m", SHA256: strings.Repeat("zz", 32), Dest: "/m"}, "64-hex-digit"},
		{"no dest", Download{URL: "https://example.com/m"}, "dest is required"},
		{"root dest", Download{URL: "https://example.com/m", Dest: "/"}, `dest must not be "/"`},
		{"dest with whitespace", Download{URL: "https://example.com/m", Dest: "/a b"}, "whitespace"},
		{"unknown extract", Download{URL: "https://example.com/m", Dest: "/m", Extract: "tar.xz"}, "not one of"},
		{"bad mode", Download{URL: "https://example.com/m", Dest: "/m", Mode: "0999"}, "octal"},
		{"mode with extract", Download{URL: "https://example.com/m", Dest: "/m", Extract: "zip", Mode: "0644"}, "do not apply with extract"},
		{"owner with extract", Download{URL: "https://example.com/m", Dest: "/m", Extract: "zip", Owner: "1000:1000"}, "do not apply with extract"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := fileWithDownloads(tt.d).Validate()
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestValidateRejectsTwoDownloadsWithTheSameDest(t *testing.T) {
	err := fileWithDownloads(
		Download{URL: "https://example.com/a", Dest: "/app/model"},
		Download{URL: "https://example.com/b", Dest: "/app/model"},
	).Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate dest") {
		t.Fatalf("got %v, want a duplicate-dest error", err)
	}
}

func TestValidateAllowsTheSameURLTwiceAtDifferentDests(t *testing.T) {
	err := fileWithDownloads(
		Download{URL: "https://example.com/a", Dest: "/app/one"},
		Download{URL: "https://example.com/a", Dest: "/app/two"},
	).Validate()
	if err != nil {
		t.Fatalf("one url may legitimately land in two places: %v", err)
	}
}

// The digest check is shared with install.apt.repositories; this pins that it
// still rejects there, so a refactor cannot loosen one path silently.
func TestValidateStillRejectsAShortAptKeyDigest(t *testing.T) {
	f := &File{Version: 1, Stages: []Stage{{Name: "app", From: "debian:12", Install: &Install{
		Apt: &AptInstall{Packages: []string{"curl"}, Repositories: []AptRepository{{
			Name: "vendor", URL: "https://apt.example.com", Suites: []string{"stable"},
			Components: []string{"main"},
			Key:        AptRepositoryKey{URL: "https://apt.example.com/key.gpg", SHA256: "abc123"},
		}}},
	}}}}
	err := f.Validate()
	if err == nil || !strings.Contains(err.Error(), "64-hex-digit") {
		t.Fatalf("got %v, want a digest-length error", err)
	}
}
