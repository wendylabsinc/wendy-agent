package buildargs

import (
	"slices"
	"testing"
)

func TestValidatePair_RejectsFlagInjectionValue(t *testing.T) {
	if err := ValidatePair("FOO", "-rm-rf"); err == nil {
		t.Fatal("a value starting with '-' must be rejected: it could be read as a flag")
	}
}

func TestValidatePair_RejectsControlCharacters(t *testing.T) {
	if err := ValidatePair("FOO", "line1\nline2"); err == nil {
		t.Fatal("control characters must be rejected: they can inject lines into the streamed build log")
	}
}

func TestValidatePair_RejectsBadKey(t *testing.T) {
	if err := ValidatePair("9FOO", "bar"); err == nil {
		t.Fatal("a key not matching [A-Za-z_][A-Za-z0-9_]* must be rejected")
	}
}

// Values legitimately hold spaces and punctuation — a log path, an MOTD.
func TestValidatePair_AcceptsPunctuationAndSpaces(t *testing.T) {
	if err := ValidatePair("MOTD", "Hello, world / welcome!"); err != nil {
		t.Fatalf("an ordinary user-authored value was rejected: %v", err)
	}
}

func TestSortedValidatedKeys_IsSorted(t *testing.T) {
	got, err := SortedValidatedKeys(map[string]string{"FOO": "bar", "ABC": "1"})
	if err != nil {
		t.Fatalf("SortedValidatedKeys: %v", err)
	}
	if !slices.Equal(got, []string{"ABC", "FOO"}) {
		t.Fatalf("got %v, want [ABC FOO] — order must be deterministic so build commands are reproducible", got)
	}
}

func TestSortedValidatedKeys_PropagatesValidationError(t *testing.T) {
	if _, err := SortedValidatedKeys(map[string]string{"FOO": "-x"}); err == nil {
		t.Fatal("an invalid value must fail the whole set, not be silently dropped")
	}
}
