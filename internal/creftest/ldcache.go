package creftest

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The ldref output cache.
//
// The internal/deflate, flate, gzip and zlib differential tests send the same
// inputs to ldref on every run, and at levels 10..12 libdeflate compresses at
// a few MB/s: most of a full run's C time is recomputing outputs that cannot
// have changed. LDRun therefore keeps the stdout of the deterministic
// compressor subcommands (ldCacheable) in a content-addressed cache:
//
//	key   = SHA-256 of (cache format, libdeflate tag, SHA-256 of the ldref
//	        binary, the arguments, SHA-256 of stdin)
//	entry = <dir>/<key[:2]>/<key>: "LDC1", uint64 payload length, SHA-256 of
//	        the payload, payload
//
// The ldref binary is linked statically from ref/libdeflate (ref/ldref/Makefile),
// so its hash covers the libdeflate sources, ldref.c and the compiler flags:
// the first run after rebuilding ldref (a new tag, a changed ldref.c or
// ref/libdeflate) misses every entry and repopulates the cache; old entries
// are never consulted again (delete the directory to reclaim the space).
//
// An entry is written to a temporary file and renamed into place, so
// concurrent tests and processes never see a partial entry; an entry whose
// length or hash does not match is ignored, recomputed and rewritten. One hit
// in ldVerifyEvery is re-run in C anyway and must match the entry (a standing
// spot-check that the cache returns libdeflate's bytes).
//
// FASTCOMPRESS_LDREF_CACHE:
//
//	unset, "" or "on"  cache on, except when CI is set (CI runs uncached)
//	"off" or "0"       cache off: every call runs ldref
//	"verify"           every call runs ldref; a cached entry must equal its
//	                   output (the test fails otherwise), a missing one is
//	                   written
//
// FASTCOMPRESS_LDREF_CACHE_DIR sets the directory (default os.UserCacheDir()/fastcompress-ldref,
// i.e. $XDG_CACHE_HOME/fastcompress-ldref or ~/.cache/fastcompress-ldref, else
// os.TempDir()/fastcompress-ldref). A directory inside the repository is refused
// (the cache is then off).

// ldTag is the libdeflate release ldref is built from (LDTAG in ref/fetch.sh).
const ldTag = "v1.26"

// ldVerifyEvery: a hit whose key starts with a zero byte modulo this is
// re-run in C (1 in 64 hits).
const ldVerifyEvery = 64

// ldCacheable are the ldref subcommands whose output is cached: the
// compressor ones (whole compressions, and the script-driven matchfinder and
// near-optimal stage dumps), pure functions of the binary, the arguments and
// stdin.
var ldCacheable = map[string]bool{
	"compress": true, "compress-batch": true, "dump-hcmf": true, "dump-btmf": true, "dump-nopt": true,
}

var ldBinHashes sync.Map // ldref path -> hex SHA-256 (or error)

type ldHashResult struct {
	sum string
	err error
}

// ldBinHash returns the hex SHA-256 of the binary at path, once per process.
func ldBinHash(path string) (string, error) {
	if v, ok := ldBinHashes.Load(path); ok {
		r := v.(ldHashResult)
		return r.sum, r.err
	}
	b, err := os.ReadFile(path)
	r := ldHashResult{err: err}
	if err == nil {
		s := sha256.Sum256(b)
		r.sum = hex.EncodeToString(s[:])
	}
	v, _ := ldBinHashes.LoadOrStore(path, r)
	r = v.(ldHashResult)
	return r.sum, r.err
}

// ldCacheMode returns "off", "on" or "verify" from FASTCOMPRESS_LDREF_CACHE and CI.
func ldCacheMode() string {
	switch v := os.Getenv("FASTCOMPRESS_LDREF_CACHE"); v {
	case "off", "0", "false":
		return "off"
	case "verify":
		return "verify"
	case "", "on", "1", "true":
		if v == "" {
			if ci := os.Getenv("CI"); ci != "" && ci != "0" && ci != "false" {
				return "off"
			}
		}
		return "on"
	default:
		return "invalid"
	}
}

// ldCacheDir returns the cache directory, or "" with a reason when it may not
// be used.
func ldCacheDir() (string, string) {
	dir := os.Getenv("FASTCOMPRESS_LDREF_CACHE_DIR")
	if dir == "" {
		if d, err := os.UserCacheDir(); err == nil {
			dir = filepath.Join(d, "fastcompress-ldref")
		} else {
			dir = filepath.Join(os.TempDir(), "fastcompress-ldref")
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err.Error()
	}
	repo, err := filepath.Abs(root())
	if err != nil {
		return "", err.Error()
	}
	if rel, err := filepath.Rel(repo, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", abs + " is inside the repository"
	}
	return abs, ""
}

// ldCacheKey is the hex cache key of one ldref call.
func ldCacheKey(binHash string, args []string, in []byte) string {
	inSum := sha256.Sum256(in)
	h := sha256.New()
	h.Write([]byte("fastcompress-ldref-cache v1\n" + ldTag + "\n" + binHash + "\n"))
	for _, a := range args {
		h.Write([]byte(a))
		h.Write([]byte{0})
	}
	h.Write([]byte("\n"))
	h.Write(inSum[:])
	return hex.EncodeToString(h.Sum(nil))
}

const ldEntryMagic = "LDC1"

var errLDEntry = errors.New("bad ldref cache entry")

// ldReadEntry returns the payload of the entry at path; fs.ErrNotExist when
// there is none, errLDEntry when it is corrupt.
func ldReadEntry(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	const hdr = len(ldEntryMagic) + 8 + sha256.Size
	if len(b) < hdr || string(b[:4]) != ldEntryMagic || binary.LittleEndian.Uint64(b[4:12]) != uint64(len(b)-hdr) {
		return nil, errLDEntry
	}
	payload := b[hdr:]
	if s := sha256.Sum256(payload); !bytes.Equal(s[:], b[12:hdr]) {
		return nil, errLDEntry
	}
	return payload, nil
}

// ldWriteEntry stores payload at path atomically (temporary file + rename).
func ldWriteEntry(path string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	s := sha256.Sum256(payload)
	b := make([]byte, 0, 12+len(s))
	b = append(b, ldEntryMagic...)
	b = binary.LittleEndian.AppendUint64(b, uint64(len(payload)))
	b = append(b, s[:]...)
	_, err = f.Write(b)
	if err == nil {
		_, err = f.Write(payload)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

var ldCacheNote sync.Once

// ldRunCached is run(t, bin, in, args...) through the cache (see above).
func ldRunCached(t testing.TB, bin string, in []byte, args ...string) []byte {
	t.Helper()
	mode := ldCacheMode()
	if mode == "invalid" {
		t.Fatalf("FASTCOMPRESS_LDREF_CACHE=%q: want on, off or verify", os.Getenv("FASTCOMPRESS_LDREF_CACHE"))
	}
	if mode == "off" || len(args) == 0 || !ldCacheable[args[0]] {
		return run(t, bin, in, args...)
	}
	dir, why := ldCacheDir()
	binHash, err := ldBinHash(bin)
	if dir == "" || err != nil {
		if err != nil {
			why = err.Error()
		}
		ldCacheNote.Do(func() { t.Logf("ldref cache off: %s", why) })
		return run(t, bin, in, args...)
	}
	key := ldCacheKey(binHash, args, in)
	path := filepath.Join(dir, key[:2], key)
	cached, err := ldReadEntry(path)
	switch {
	case err == nil && mode == "on" && !ldSpotCheck(key):
		return cached
	case err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errLDEntry):
		t.Logf("ldref cache: %v (running ldref)", err)
	}
	fresh := run(t, bin, in, args...)
	if err == nil && !bytes.Equal(cached, fresh) {
		t.Fatalf("ldref cache entry %s (ldref %v) differs from a fresh ldref run (%d vs %d bytes): the cache "+
			"or ldref is broken; delete %s", path, args, len(cached), len(fresh), dir)
	}
	if err != nil {
		if werr := ldWriteEntry(path, fresh); werr != nil {
			t.Logf("ldref cache: write %s: %v", path, werr)
		}
	}
	return fresh
}

// ldSpotCheck reports whether a hit for key is re-run in C (1 in ldVerifyEvery).
func ldSpotCheck(key string) bool {
	b, err := hex.DecodeString(key[:2])
	return err == nil && int(b[0])%ldVerifyEvery == 0
}
