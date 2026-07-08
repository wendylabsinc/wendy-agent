package espidftoolchain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsEspIdfProject(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  bool
	}{
		{
			name:  "empty directory",
			files: nil,
			want:  false,
		},
		{
			name:  "sdkconfig present",
			files: map[string]string{"sdkconfig": "CONFIG_IDF_TARGET=\"esp32c6\"\n"},
			want:  true,
		},
		{
			name:  "sdkconfig.defaults present",
			files: map[string]string{"sdkconfig.defaults": "CONFIG_LOG_DEFAULT_LEVEL_INFO=y\n"},
			want:  true,
		},
		{
			name: "CMakeLists.txt with IDF project include",
			files: map[string]string{
				"CMakeLists.txt": "cmake_minimum_required(VERSION 3.16)\ninclude($ENV{IDF_PATH}/tools/cmake/project.cmake)\nproject(blink)\n",
			},
			want: true,
		},
		{
			name: "CMakeLists.txt with esp-idf path include",
			files: map[string]string{
				"CMakeLists.txt": "cmake_minimum_required(VERSION 3.16)\ninclude(~/esp/esp-idf/tools/cmake/project.cmake)\nproject(blink)\n",
			},
			want: true,
		},
		{
			name: "CMakeLists.txt with non-IDF project.cmake include",
			files: map[string]string{
				"CMakeLists.txt": "cmake_minimum_required(VERSION 3.16)\ninclude(cmake/project.cmake)\nproject(hello)\n",
			},
			want: false,
		},
		{
			name: "plain CMakeLists.txt",
			files: map[string]string{
				"CMakeLists.txt": "cmake_minimum_required(VERSION 3.16)\nproject(hello)\nadd_executable(hello main.c)\n",
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := IsEspIdfProject(dir); got != tt.want {
				t.Errorf("IsEspIdfProject() = %v, want %v", got, tt.want)
			}
		})
	}
}
