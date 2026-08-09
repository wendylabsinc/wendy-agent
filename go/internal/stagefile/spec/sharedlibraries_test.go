package spec

import (
	"strings"
	"testing"
)

func TestValidateSharedLibraries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		libs    []SharedLibrary
		wantErr string
	}{
		{
			name: "valid",
			libs: []SharedLibrary{{Dir: "/opt/cuda12/lib", Collect: []string{"/usr/lib/nvidia"}}},
		},
		{
			name:    "dir is required",
			libs:    []SharedLibrary{{Collect: []string{"/usr/lib/nvidia"}}},
			wantErr: "sharedLibraries[0].dir is required",
		},
		{
			name:    "relative dir",
			libs:    []SharedLibrary{{Dir: "opt/lib", Collect: []string{"/usr/lib/nvidia"}}},
			wantErr: "must be an absolute path",
		},
		{
			name:    "root dir",
			libs:    []SharedLibrary{{Dir: "/", Collect: []string{"/usr/lib/nvidia"}}},
			wantErr: `sharedLibraries[0].dir must not be "/"`,
		},
		{
			name:    "empty collect",
			libs:    []SharedLibrary{{Dir: "/opt/lib"}},
			wantErr: "sharedLibraries[0].collect must be non-empty",
		},
		{
			name:    "relative collect entry",
			libs:    []SharedLibrary{{Dir: "/opt/lib", Collect: []string{"usr/lib"}}},
			wantErr: "must be an absolute path",
		},
		{
			name:    "newline in dir",
			libs:    []SharedLibrary{{Dir: "/opt/lib\nRUN evil", Collect: []string{"/usr/lib"}}},
			wantErr: "must not contain",
		},
		{
			name:    "whitespace in collect entry",
			libs:    []SharedLibrary{{Dir: "/opt/lib", Collect: []string{"/usr/lib nvidia"}}},
			wantErr: "must not contain",
		},
		{
			name: "second entry is checked too",
			libs: []SharedLibrary{
				{Dir: "/opt/a", Collect: []string{"/usr/lib"}},
				{Dir: "relative", Collect: []string{"/usr/lib"}},
			},
			wantErr: "sharedLibraries[1].dir",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &File{Version: 1, Stages: []Stage{{
				Name: "app", From: "ubuntu:22.04", SharedLibraries: tc.libs,
			}}}
			err := f.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}
