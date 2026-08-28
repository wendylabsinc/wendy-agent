package spec

import (
	"strings"
	"testing"
)

// cudaStage builds a one-stage file so each case states only what it is about.
func cudaStage(s Stage) *File {
	s.Name = "app"
	s.From = "ubuntu:22.04"
	return &File{Version: 1, Stages: []Stage{s}}
}

func TestValidateCUDA(t *testing.T) {
	tests := []struct {
		name    string
		stage   Stage
		wantErr string
	}{
		{
			name: "gpu stage with a cuda pip group",
			stage: Stage{
				CUDA:    true,
				Install: &Install{Pip: []PipInstall{{Packages: []string{"torch"}, CUDA: true}}},
			},
		},
		{
			name: "gpu stage needs no cuda pip group",
			stage: Stage{
				CUDA:    true,
				Install: &Install{Apt: &AptInstall{Packages: []string{"python3"}}},
			},
		},
		{
			name:  "gpu stage with no install at all",
			stage: Stage{CUDA: true},
		},
		{
			name: "cuda pip group without a cuda stage",
			stage: Stage{
				Install: &Install{Pip: []PipInstall{{Packages: []string{"torch"}, CUDA: true}}},
			},
			wantErr: "install.pip[0] sets cuda: but the stage does not",
		},
		{
			name: "cuda pip group that also pins an index",
			stage: Stage{
				CUDA: true,
				Install: &Install{Pip: []PipInstall{{
					Packages: []string{"torch"},
					CUDA:     true,
					Index:    "https://pypi.example.com/",
				}}},
			},
			wantErr: "sets both cuda: and index/extraIndex",
		},
		{
			name: "cuda pip group that also pins an extra index",
			stage: Stage{
				CUDA: true,
				Install: &Install{Pip: []PipInstall{{
					Packages:   []string{"torch"},
					CUDA:       true,
					ExtraIndex: []string{"https://pypi.example.com/"},
				}}},
			},
			wantErr: "sets both cuda: and index/extraIndex",
		},
		{
			name: "gpu stage may not set the loader path itself",
			stage: Stage{
				CUDA: true,
				Env:  map[string]string{"LD_LIBRARY_PATH": "/opt/mine"},
			},
			wantErr: "must come first on that path",
		},
		{
			name: "non-gpu stage may set the loader path freely",
			stage: Stage{
				Env: map[string]string{"LD_LIBRARY_PATH": "/opt/mine"},
			},
		},
		{
			// A group without cuda: keeps its own index — the escape hatch for
			// a GPU wheel the profile doesn't cover stays open.
			name: "explicit index alongside a cuda stage is allowed",
			stage: Stage{
				CUDA: true,
				Install: &Install{Pip: []PipInstall{{
					Packages: []string{"some-wheel"},
					Index:    "https://pypi.example.com/",
				}}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := cudaStage(tc.stage).Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Validate: unexpected error: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("Validate: error = nil, want one containing %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("Validate: error = %q, want one containing %q", err, tc.wantErr)
			}
		})
	}
}
