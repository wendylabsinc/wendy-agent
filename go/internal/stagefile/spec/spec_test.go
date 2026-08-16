package spec

import "testing"

func TestNpmLockfileDefaultsToNpm(t *testing.T) {
	if got := NpmLockfile(""); got != "package-lock.json" {
		t.Fatalf("NpmLockfile(\"\") = %q, want %q", got, "package-lock.json")
	}
}

func TestNpmLockfileYarn(t *testing.T) {
	if got := NpmLockfile("yarn"); got != "yarn.lock" {
		t.Fatalf("NpmLockfile(\"yarn\") = %q, want %q", got, "yarn.lock")
	}
}

func TestNpmLockfilePnpm(t *testing.T) {
	if got := NpmLockfile("pnpm"); got != "pnpm-lock.yaml" {
		t.Fatalf("NpmLockfile(\"pnpm\") = %q, want %q", got, "pnpm-lock.yaml")
	}
}
