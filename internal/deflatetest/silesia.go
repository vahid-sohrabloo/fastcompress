package deflatetest

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

// File is one corpus file. Its contents are read only by Load.
type File struct {
	Name string
	Path string
	Size int64
}

// Load reads the whole file, failing tb (typically the subtest's own t) on
// error. Callers decide when (and whether) to pay for it; the 212 MB
// silesia.tar in particular should be streamed from Path unless a test truly
// needs it in memory.
func (f File) Load(tb testing.TB) []byte {
	tb.Helper()
	b, err := os.ReadFile(f.Path)
	if err != nil {
		tb.Fatalf("read %s: %v", f.Path, err)
	}
	return b
}

// repoRoot returns the repository root: two directories above this file
// (runtime.Caller), or, when that path does not exist (-trimpath), the nearest
// directory containing go.mod at or above the working directory.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	r := filepath.Join(filepath.Dir(file), "..", "..")
	if _, err := os.Stat(filepath.Join(r, "go.mod")); err == nil {
		return r
	}
	dir, err := os.Getwd()
	for err == nil {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return r
}

// inCI reports whether env CI is set to something other than "", "0" or "false".
func inCI() bool {
	v := os.Getenv("CI")
	return v != "" && v != "0" && v != "false"
}

// missing skips the test, or fails it in CI.
func missing(t testing.TB, msg string) {
	t.Helper()
	if inCI() {
		t.Fatal(msg)
	}
	t.Skip(msg)
}

// corpusDir returns the directory holding silesia/ and silesia.tar:
// $DEFLATETEST_CORPUS if set, else <repo root>/bench/corpus.
func corpusDir() string {
	if d := os.Getenv("DEFLATETEST_CORPUS"); d != "" {
		return d
	}
	return filepath.Join(repoRoot(), "bench", "corpus")
}

// Silesia returns the files of bench/corpus/silesia sorted by name. It skips
// the test when the corpus is missing, and fails it when env CI is set to
// something other than "", "0" or "false".
func Silesia(t testing.TB) []File {
	t.Helper()
	dir := filepath.Join(corpusDir(), "silesia")
	if _, err := os.Stat(dir); err != nil {
		missing(t, "silesia corpus not found in "+dir+" (run ref/fetch.sh or set DEFLATETEST_CORPUS)")
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var fs []File
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		fs = append(fs, File{e.Name(), filepath.Join(dir, e.Name()), fi.Size()})
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].Name < fs[j].Name })
	if len(fs) == 0 {
		t.Fatal("empty silesia directory " + dir)
	}
	return fs
}

// SilesiaTar returns bench/corpus/silesia.tar (not loaded; use Path or Load).
// It does not need the silesia/ directory. Skip/fail behaviour as for Silesia.
func SilesiaTar(t testing.TB) File {
	t.Helper()
	p := filepath.Join(corpusDir(), "silesia.tar")
	fi, err := os.Stat(p)
	if err != nil {
		missing(t, "silesia.tar not found: "+err.Error())
	}
	return File{"silesia.tar", p, fi.Size()}
}
