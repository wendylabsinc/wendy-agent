package mesh

import "testing"

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"brave-dolphin": "brave-dolphin",
		"Brave Dolphin": "brave-dolphin",
		"ACME Corp.":    "acme-corp",
		"acme_corp":     "acme-corp",
		"  spaced  ":    "spaced",
		"a--b__c":       "a-b-c",
		"Wendy Labs, Inc": "wendy-labs-inc",
		"":              "",
		"---":           "",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
