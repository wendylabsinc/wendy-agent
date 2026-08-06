package optimize

import (
	"archive/tar"
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type addCopyAnalyzer struct{}

func (addCopyAnalyzer) ID() string { return "add-copy" }

func isRemoteAddSource(src string) bool {
	lower := strings.ToLower(src)
	return strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "git://") ||
		strings.HasPrefix(lower, "ssh://") ||
		strings.HasPrefix(lower, "git+ssh://") ||
		strings.HasPrefix(lower, "git@")
}

// readerContainsTar reports whether r starts with a tar archive. The explicit
// zero-block check also recognizes an empty tar, for which tar.Reader returns
// io.EOF without yielding a header.
func readerContainsTar(r io.Reader) bool {
	buffered := bufio.NewReader(r)
	if block, err := buffered.Peek(2 * 512); err == nil {
		allZero := true
		for _, b := range block {
			if b != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			return true
		}
	}
	_, err := tar.NewReader(buffered).Next()
	return err == nil
}

// isLocalTarArchive recognizes the formats Docker auto-extracts based on file
// contents, not filename. XZ is conservatively treated as special because the
// standard library has no XZ decoder with which to validate the tar payload.
func isLocalTarArchive(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true // unknown input: do not offer a semantics-changing fix
	}
	defer f.Close()

	magic := make([]byte, 6)
	n, _ := io.ReadFull(f, magic)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return true
	}

	switch {
	case n >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		gz, err := gzip.NewReader(f)
		if err != nil {
			return false
		}
		defer gz.Close()
		return readerContainsTar(gz)
	case n >= 3 && string(magic[:3]) == "BZh":
		return readerContainsTar(bzip2.NewReader(f))
	case n >= 6 && slices.Equal(magic, []byte{0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00}):
		return true
	default:
		return readerContainsTar(f)
	}
}

// isPlainLocalAddSource proves that src resolves only to local files or
// directories for which ADD and COPY have the same behavior. Unknown paths,
// dynamic paths, remote sources, and local tar archives are all skipped.
func isPlainLocalAddSource(dir, src string) bool {
	if isRemoteAddSource(src) || strings.ContainsAny(src, "$\"'\\") || filepath.IsAbs(src) {
		return false
	}

	base, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	matches, err := filepath.Glob(filepath.Join(base, filepath.FromSlash(src)))
	if err != nil || len(matches) == 0 {
		return false
	}
	for _, match := range matches {
		if !isWithinDir(base, match) {
			return false
		}
		info, err := os.Lstat(match)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if info.IsDir() {
			continue
		}
		if !info.Mode().IsRegular() || isLocalTarArchive(match) {
			return false
		}
	}
	return true
}

func addFlagsWorkWithCopy(flags []string) bool {
	for _, flag := range flags {
		name := strings.SplitN(flag, "=", 2)[0]
		switch name {
		case "--chown", "--chmod", "--link", "--exclude", "--parents":
		default:
			return false
		}
	}
	return true
}

func (a addCopyAnalyzer) Analyze(t *Target) []Finding {
	if t.Dockerfile == nil {
		return nil
	}
	var out []Finding
	for _, inst := range t.Dockerfile.Instructions {
		if inst.Cmd != "ADD" || !addFlagsWorkWithCopy(inst.Flags) {
			continue
		}
		// Skip JSON array form (ADD ["src", "dst"]) since strings.Fields can't safely
		// detect URLs/archives there.
		if strings.HasPrefix(strings.TrimSpace(inst.Args), "[") {
			continue
		}
		fields := strings.Fields(inst.Args)
		if len(fields) < 2 {
			continue
		}
		// All fields but the last are sources; skip if any relies on an
		// ADD-only behavior COPY can't reproduce.
		if slices.ContainsFunc(fields[:len(fields)-1], func(src string) bool {
			return !isPlainLocalAddSource(t.Dir, src)
		}) {
			continue
		}

		raw := t.Dockerfile.Lines[inst.Line-1]
		f := Finding{
			Analyzer: a.ID(),
			Severity: SeverityInfo,
			Title:    "ADD used where COPY would do",
			Detail: "This ADD only copies local, non-archive files — none of ADD's remote-fetch or " +
				"tar-auto-extraction behavior is in play. COPY is more explicit and avoids ADD's surprising " +
				"auto-extract semantics if a source later becomes a tar archive.",
			Location: &Loc{File: t.Dockerfile.Path, Line: inst.Line},
		}
		if strings.HasPrefix(strings.TrimLeft(raw, " \t"), "ADD ") {
			indent := raw[:len(raw)-len(strings.TrimLeft(raw, " \t"))]
			body := strings.TrimLeft(raw, " \t")
			f.Fix = &Fix{
				Description: "replace ADD with COPY",
				Op:          FixReplaceLine,
				File:        t.Dockerfile.Path,
				Line:        inst.Line,
				Old:         raw,
				New:         indent + "COPY " + strings.TrimPrefix(body, "ADD "),
			}
		}
		out = append(out, f)
	}
	return out
}
