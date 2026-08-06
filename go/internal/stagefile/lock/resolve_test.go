package lock

import (
	"errors"
	"testing"
)

func fakeResolver(answers map[string]string) Resolver {
	return func(ref string) (string, error) {
		d, ok := answers[ref]
		if !ok {
			return "", errors.New("no fake answer for " + ref)
		}
		return d, nil
	}
}

func TestResolveFillsMissingEntries(t *testing.T) {
	resolver := fakeResolver(map[string]string{"debian:12": "sha256:aaa"})
	result, resolved, err := Resolve(nil, "sha256:src1", []string{"debian:12"}, nil, resolver)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result.Images["debian:12"] != "sha256:aaa" {
		t.Fatalf("Images = %+v", result.Images)
	}
	if len(resolved) != 1 || resolved[0] != "debian:12" {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestResolveLeavesExistingEntryUntouchedByDefault(t *testing.T) {
	existing := &File{Version: 1, Images: map[string]string{"debian:12": "sha256:old"}}
	resolver := fakeResolver(map[string]string{"debian:12": "sha256:new"})
	result, resolved, err := Resolve(existing, "sha256:src1", []string{"debian:12"}, nil, resolver)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result.Images["debian:12"] != "sha256:old" {
		t.Fatalf("expected the existing pin to survive untouched, got %+v", result.Images)
	}
	if len(resolved) != 0 {
		t.Fatalf("expected nothing re-resolved, got %+v", resolved)
	}
}

func TestResolveForceUpdateOverridesExistingEntry(t *testing.T) {
	existing := &File{Version: 1, Images: map[string]string{"debian:12": "sha256:old"}}
	resolver := fakeResolver(map[string]string{"debian:12": "sha256:new"})
	result, resolved, err := Resolve(existing, "sha256:src1", []string{"debian:12"}, map[string]bool{"debian:12": true}, resolver)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result.Images["debian:12"] != "sha256:new" {
		t.Fatalf("expected the pin to be updated, got %+v", result.Images)
	}
	if len(resolved) != 1 || resolved[0] != "debian:12" {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestResolvePropagatesResolverError(t *testing.T) {
	resolver := fakeResolver(map[string]string{})
	if _, _, err := Resolve(nil, "sha256:src1", []string{"debian:12"}, nil, resolver); err == nil {
		t.Fatal("expected an error when the resolver fails, got nil")
	}
}
