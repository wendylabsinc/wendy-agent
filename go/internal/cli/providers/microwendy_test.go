package providers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEspIdfBinaryPath(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		product string
		wantBin string // basename of the expected binary; "" means an error is expected
	}{
		{
			name: "binary named after CMake project name",
			files: map[string]string{
				"CMakeLists.txt": "cmake_minimum_required(VERSION 3.16)\ninclude($ENV{IDF_PATH}/tools/cmake/project.cmake)\nproject(myfw)\n",
				"build/myfw.bin": "firmware",
			},
			product: "my-folder",
			wantBin: "myfw.bin",
		},
		{
			name: "falls back to product without a project() declaration",
			files: map[string]string{
				"build/my-folder.bin": "firmware",
			},
			product: "my-folder",
			wantBin: "my-folder.bin",
		},
		{
			name: "missing binary",
			files: map[string]string{
				"CMakeLists.txt": "include($ENV{IDF_PATH}/tools/cmake/project.cmake)\nproject(myfw)\n",
			},
			product: "my-folder",
			wantBin: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tt.files {
				path := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			binPath, err := espIdfBinaryPath(dir, tt.product)
			if tt.wantBin == "" {
				if err == nil {
					t.Fatal("espIdfBinaryPath() expected an error for a missing binary")
				}
				return
			}
			if err != nil {
				t.Fatalf("espIdfBinaryPath() error = %v", err)
			}
			if want := filepath.Join(dir, "build", tt.wantBin); binPath != want {
				t.Errorf("espIdfBinaryPath() = %q, want %q", binPath, want)
			}
		})
	}
}

func TestBoardToTarget(t *testing.T) {
	// Boards currently map to targets one-to-one by name; this pins the
	// identity mapping until real board names diverge from SoC names.
	if got := boardToTarget("esp32c6"); got != "esp32c6" {
		t.Errorf("boardToTarget(esp32c6) = %q, want %q", got, "esp32c6")
	}
}
