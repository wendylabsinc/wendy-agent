package commands

import "testing"

func TestParseVolumeSpec(t *testing.T) {
	tests := []struct {
		arg        string
		wantVolume string
		wantPath   string
		wantErr    bool
	}{
		{arg: "data:models/model.onnx", wantVolume: "data", wantPath: "models/model.onnx"},
		{arg: "data:", wantVolume: "data", wantPath: ""},
		{arg: "data:/models/x.bin", wantVolume: "data", wantPath: "models/x.bin"},
		{arg: "data:./models/x.bin", wantVolume: "data", wantPath: "models/x.bin"},
		{arg: "data:models//x.bin", wantVolume: "data", wantPath: "models/x.bin"},
		{arg: "data:../escape", wantErr: true},
		{arg: "data:models/../../escape", wantErr: true},
		{arg: "no-colon", wantErr: true},
		{arg: ":path", wantErr: true},
		{arg: "sub/dir:path", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.arg, func(t *testing.T) {
			spec, err := parseVolumeSpec(tc.arg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", spec)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVolumeSpec: %v", err)
			}
			if spec.volume != tc.wantVolume || spec.path != tc.wantPath {
				t.Fatalf("got %q:%q, want %q:%q", spec.volume, spec.path, tc.wantVolume, tc.wantPath)
			}
		})
	}
}
