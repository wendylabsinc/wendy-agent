// Package dtb provides a device-tree compile wrapper that shells out to the
// system cpp and dtc tools to produce a compiled device-tree blob (.dtb) from
// a device-tree source (.dts) file.
//
// The two-stage pipeline mirrors the NVIDIA BCT generation tooling:
//
//  1. cpp preprocesses the DTS (macro expansion, conditional compilation).
//  2. dtc compiles the preprocessed source to a binary DTB.
//
// # Tool availability
//
// cpp is a standard compiler tool present on Linux and macOS (Xcode CLT).
// dtc is not bundled with macOS; install it with:
//
//	brew install dtc
//
// On Linux, install the dtc package (e.g. device-tree-compiler on Debian/Ubuntu).
//
// # Platform notes
//
// On Linux, cpp is invoked with the -x assembler-with-cpp flag, which matches
// the NVIDIA reference command line exactly. On macOS, Apple's clang-based cpp
// wrapper does not support -x assembler-with-cpp when producing named output,
// so the flag is omitted; standard #ifdef/#define preprocessing still works
// correctly without it.
package dtb

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// CompileOptions controls the DTS-to-DTB compilation.
type CompileOptions struct {
	// DTSPath is the path to the input .dts source file.
	DTSPath string
	// OutDir is the directory where the intermediate _cpp.dts and the final
	// _cpp.dtb are written. It must already exist.
	OutDir string
	// Defines is a list of preprocessor macro names passed to cpp as -D flags.
	Defines []string
	// IncludeDirs is a list of directories passed to cpp as -I flags.
	IncludeDirs []string
}

// Compile preprocesses opts.DTSPath with cpp and then compiles it with dtc,
// writing <stem>_cpp.dts and <stem>_cpp.dtb into opts.OutDir.
// It returns the path to the produced .dtb file.
//
// cpp and dtc are resolved from PATH. If either tool is missing a descriptive
// error is returned without running any commands.
//
// Standard error output from cpp or dtc is captured and included in the
// returned error when a command fails.
func Compile(opts CompileOptions) (dtbPath string, err error) {
	cppBin, err := exec.LookPath("cpp")
	if err != nil {
		return "", fmt.Errorf("dtb.Compile: cpp not found on PATH: %w", err)
	}

	dtcBin, err := exec.LookPath("dtc")
	if err != nil {
		return "", fmt.Errorf("dtb.Compile: dtc not found on PATH (on macOS: brew install dtc): %w", err)
	}

	// Derive the stem from the input file's base name without extension.
	base := filepath.Base(opts.DTSPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))

	cppDTS := filepath.Join(opts.OutDir, stem+"_cpp.dts")
	cppDTB := filepath.Join(opts.OutDir, stem+"_cpp.dtb")

	// Stage 1: cpp preprocessing.
	//
	// Reference (Linux):
	//   cpp -nostdinc -x assembler-with-cpp <-D defines> <-I includedirs>
	//       -o <stem>_cpp.dts <stem>.dts
	//
	// On macOS, Apple clang's cpp wrapper does not accept -x assembler-with-cpp
	// reliably; the flag is omitted there. Output is captured via cmd.Stdout
	// rather than cpp's -o flag, which avoids a macOS driver quirk where a
	// named -o argument triggers full-compilation mode instead of preprocessing.
	// cpp does not accept -D and -I as separate argv tokens on all platforms;
	// the flag and its value must be combined (e.g. "-DFLAG", "-I/dir").
	cppArgs := []string{"-nostdinc"}
	if runtime.GOOS == "linux" {
		cppArgs = append(cppArgs, "-x", "assembler-with-cpp")
	}
	for _, d := range opts.Defines {
		cppArgs = append(cppArgs, "-D"+d)
	}
	for _, inc := range opts.IncludeDirs {
		cppArgs = append(cppArgs, "-I"+inc)
	}
	cppArgs = append(cppArgs, opts.DTSPath)

	if err := runToolToFile(cppBin, cppArgs, cppDTS); err != nil {
		return "", fmt.Errorf("dtb.Compile: cpp failed: %w", err)
	}

	// Stage 2: dtc compilation.
	//
	// dtc -I dts -O dtb -o <stem>_cpp.dtb -qqq <stem>_cpp.dts
	dtcArgs := []string{"-I", "dts", "-O", "dtb", "-o", cppDTB, "-qqq", cppDTS}

	if err := runTool(dtcBin, dtcArgs); err != nil {
		return "", fmt.Errorf("dtb.Compile: dtc failed: %w", err)
	}

	return cppDTB, nil
}

// runToolToFile executes the binary at path with args, capturing stderr, and
// writes stdout to the file at outPath. If the command exits non-zero the
// stderr output is included in the returned error.
func runToolToFile(path string, args []string, outPath string) error {
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}
	defer f.Close()

	var stderr bytes.Buffer
	cmd := exec.Command(path, args...)
	cmd.Stdout = f
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w\n%s", err, msg)
		}
		return err
	}
	return nil
}

// runTool executes the binary at path with args, capturing stderr.
// If the command exits non-zero the stderr output is included in the error.
func runTool(path string, args []string) error {
	var stderr bytes.Buffer
	cmd := exec.Command(path, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w\n%s", err, msg)
		}
		return err
	}
	return nil
}
