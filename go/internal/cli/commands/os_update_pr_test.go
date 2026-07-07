package commands

import "testing"

func TestOSUpdatePRFlag(t *testing.T) {
	cmd := newOSUpdateCmd()
	if cmd.Flags().Lookup("pr") == nil {
		t.Fatal("expected --pr flag on os update")
	}
}

func TestOSUpdatePRMutualExclusion(t *testing.T) {
	const mutexErr = "--pr cannot be combined with a local artifact path or --artifact-url"
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"pr with positional artifact path", []string{"--pr", "123", "image.wendy"}, mutexErr},
		{"pr with artifact-url", []string{"--pr", "123", "--artifact-url", "https://example.com/image.wendy"}, mutexErr},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newOSUpdateCmd()
			cmd.SetArgs(tc.args)
			err := cmd.Execute()
			if err == nil {
				t.Fatalf("expected error for args %v", tc.args)
			}
			if got := err.Error(); got != tc.wantErr {
				t.Errorf("unexpected error: %q; want %q", got, tc.wantErr)
			}
		})
	}
}
