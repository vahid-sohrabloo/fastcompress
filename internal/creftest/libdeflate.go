package creftest

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"testing"
)

// Container formats understood by ldref's -format option.
const (
	LDRaw  = "raw"
	LDGzip = "gzip"
	LDZlib = "zlib"
)

// libdeflate result codes reported by LDDecompressEx.
const (
	LDOK                = 0
	LDBadData           = 1
	LDShortOutput       = 2
	LDInsufficientSpace = 3
)

// LDBin returns the path of the ldref binary (libdeflate v1.26 driver).
func LDBin(t testing.TB) string {
	t.Helper()
	return tool(t, "ref/ldref/ldref", "run ref/fetch.sh --no-corpus && make -C ref/ldref")
}

// LDRun feeds in to `ldref args...` and returns its stdout. The compress
// subcommands go through the ldref output cache (ldcache.go).
func LDRun(t testing.TB, in []byte, args ...string) []byte {
	t.Helper()
	return ldRunCached(t, LDBin(t), in, args...)
}

// LDCompress compresses in with libdeflate at level (0..12) in the given
// format (LDRaw, LDGzip or LDZlib).
func LDCompress(t testing.TB, in []byte, level int, format string) []byte {
	t.Helper()
	return LDRun(t, in, "compress", "-l", strconv.Itoa(level), "-format", format)
}

// LDDecompressEx decompresses in with libdeflate into an outSize-byte buffer
// (ldref decompress-ex, any output of at most outSize bytes succeeds). It
// returns the libdeflate result code (LDOK, LDBadData, ...), the input bytes
// consumed, the output bytes produced and the output; nIn, nOut and out are
// zero/empty unless result is LDOK.
func LDDecompressEx(t testing.TB, in []byte, outSize int, format string) (result, nIn, nOut int, out []byte) {
	t.Helper()
	got := run(t, LDBin(t), in, "decompress-ex", "-format", format, "-out-size", strconv.Itoa(outSize))
	nl := bytes.IndexByte(got, '\n')
	if nl < 0 {
		t.Fatalf("ldref decompress-ex: no header line in %q", got)
	}
	if _, err := fmt.Sscanf(string(got[:nl]), "result=%d in=%d out=%d", &result, &nIn, &nOut); err != nil {
		t.Fatalf("ldref decompress-ex: bad header %q: %v", got[:nl], err)
	}
	out = got[nl+1:]
	if len(out) != nOut {
		t.Fatalf("ldref decompress-ex: header says %d output bytes, got %d", nOut, len(out))
	}
	return result, nIn, nOut, out
}

// LDVersion returns ldref's `version` output.
func LDVersion(t testing.TB) string {
	t.Helper()
	cmd := exec.Command(LDBin(t), "version")
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("ldref version: %v", err)
	}
	return string(b)
}
