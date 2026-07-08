package espidftoolchain

import (
	"bytes"
	"os"
	"path/filepath"
)

// IsEspIdfProject reports whether dir contains an ESP-IDF project.
//
// A directory is considered an ESP-IDF project when it contains an sdkconfig
// (or sdkconfig.defaults) file, or a top-level CMakeLists.txt that includes
// the IDF build system entry point (project.cmake resolved via IDF_PATH or
// an esp-idf directory). A plain CMakeLists.txt without such an include is
// not enough, so generic CMake projects are not misclassified.
func IsEspIdfProject(dir string) bool {
	for _, name := range []string{"sdkconfig", "sdkconfig.defaults"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	content, err := os.ReadFile(filepath.Join(dir, "CMakeLists.txt"))
	if err != nil {
		return false
	}
	if !bytes.Contains(content, []byte("project.cmake")) {
		return false
	}
	// Require the include path to reference an IDF install, e.g.
	// include($ENV{IDF_PATH}/tools/cmake/project.cmake) or a path
	// containing an esp-idf directory.
	return bytes.Contains(content, []byte("IDF_PATH")) ||
		bytes.Contains(content, []byte("esp-idf"))
}
