package zstd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// headerPrefixMax is how many prefix lengths of each input the frame header
// tests try (0..headerPrefixMax): every cut through a frame header (at most
// 18 bytes) and the first block header.
const headerPrefixMax = 32

// headerPrefixes returns in's prefixes of length 0..min(len, headerPrefixMax)
// and in itself.
func headerPrefixes(in []byte) [][]byte {
	var ps [][]byte
	for n := 0; n <= min(len(in), headerPrefixMax); n++ {
		ps = append(ps, in[:n])
	}
	if len(in) > headerPrefixMax {
		ps = append(ps, in)
	}
	return ps
}

// cErrCode formats a dErr the way cref fheader prints an error code.
func cErrCode(e dErr) string { return strconv.Itoa(e.cCode()) }

// checkFHeaderCref compares getFrameHeaderAdvanced, findFrameSizeInfo,
// decompressBound and getcBlockSize on in with `cref fheader`.
func checkFHeaderCref(t *testing.T, in []byte) {
	t.Helper()
	m := creftest.FHeaderCRef(t, in)
	want := func(k, got string) {
		t.Helper()
		if m[k] != got {
			t.Errorf("%d bytes: %s=%s, C %s (C: %v)", len(in), k, got, m[k], m)
		}
	}

	var zfh frameHeader
	need, err := getFrameHeaderAdvanced(&zfh, in)
	if err != 0 {
		want("err", cErrCode(err))
	} else {
		want("needed", strconv.Itoa(need))
	}
	ok := err == 0 && need == 0
	if ok {
		want("frameContentSize", strconv.FormatUint(zfh.frameContentSize, 10))
		want("windowSize", strconv.FormatUint(zfh.windowSize, 10))
		want("blockSizeMax", strconv.FormatUint(uint64(zfh.blockSizeMax), 10))
		want("frameType", strconv.Itoa(int(zfh.frameType)))
		want("headerSize", strconv.Itoa(int(zfh.headerSize)))
		want("dictID", strconv.FormatUint(uint64(zfh.dictID), 10))
		want("checksumFlag", map[bool]string{false: "0", true: "1"}[zfh.checksumFlag])
		if zfh.frameType == zstdFrame && zfh.singleSegment != (in[4]&0x20 != 0) {
			t.Errorf("%d bytes: singleSegment %v", len(in), zfh.singleSegment)
		}
	}

	fsi := findFrameSizeInfo(in)
	if fsi.err != 0 {
		want("compressedSize", "err:"+cErrCode(fsi.err))
	} else {
		want("compressedSize", strconv.Itoa(fsi.compressedSize))
	}
	want("decompressBound", strconv.FormatUint(decompressBound(in), 10))

	if ok && zfh.frameType == zstdFrame {
		cSize, bp, err := getcBlockSize(in[zfh.headerSize:])
		if err != 0 {
			want("blockSize", "err:"+cErrCode(err))
		} else {
			want("blockType", strconv.Itoa(bp.blockType))
			want("blockSize", strconv.Itoa(cSize))
			want("blockOrigSize", strconv.FormatUint(uint64(bp.origSize), 10))
			want("lastBlock", map[bool]string{false: "0", true: "1"}[bp.lastBlock])
		}
	}
}

// TestFrameHeaderCref checks the frame header functions against C on every
// prefix (up to headerPrefixMax bytes) of every decode corpus entry and
// malicious corpus case, and on each whole input.
func TestFrameHeaderCref(t *testing.T) {
	seen := map[string]bool{}
	check := func(t *testing.T, in []byte) {
		for _, p := range headerPrefixes(in) {
			if !seen[string(p)] {
				seen[string(p)] = true
				checkFHeaderCref(t, p)
			}
		}
	}
	t.Run("corpus", func(t *testing.T) {
		for _, name := range creftest.FrameNames(t) {
			check(t, creftest.LoadFrame(t, name).Data)
		}
	})
	t.Run("malicious", func(t *testing.T) {
		for _, c := range maliciousCases(t) {
			check(t, c.data)
		}
	})
	t.Run("handmade", func(t *testing.T) {
		for _, in := range headerFuzzSeeds() {
			check(t, in)
		}
	})
}

// headerFuzzSeeds are hand-made inputs for the header edge cases: window
// mantissas, every FCS and dictID field size, reserved bits, the largest
// window logs, skippable frames and reserved and RLE first blocks.
func headerFuzzSeeds() [][]byte {
	hdr := func(fhd byte, rest ...byte) []byte {
		return append([]byte{0x28, 0xB5, 0x2F, 0xFD, fhd}, rest...)
	}
	blk := func(h []byte, typ, size int, last bool) []byte {
		v := typ<<1 | size<<3
		if last {
			v |= 1
		}
		return append(h, byte(v), byte(v>>8), byte(v>>16))
	}
	return [][]byte{
		blk(hdr(0x00, 0x00), 0, 5, true),                                           // 1 KiB window, raw block
		blk(hdr(0x00, 0x07), 1, 1000, false),                                       // window mantissa 7, RLE block
		blk(hdr(0x00, 21<<3|3), 3, 7, true),                                        // window log 31, reserved block
		hdr(0x00, 22<<3),                                                           // window log 32: too large
		hdr(0x08, 0x00),                                                            // reserved bit
		blk(hdr(0x20, 0x05), 0, 5, true),                                           // single segment, 1-byte FCS
		blk(hdr(0x60, 0xFF, 0xFF), 2, 100, true),                                   // single segment, 2-byte FCS (65791)
		blk(hdr(0xA4, 0x01, 0x02, 0x03, 0x04), 2, 1<<17, true),                     // single, 4-byte FCS, checksum
		blk(hdr(0xE0, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF), 0, 0, true), // single, FCS all ones
		blk(hdr(0xC3, 0x50, 1, 2, 3, 4, 0, 0, 0, 0, 0, 0, 0, 0), 0, 0, true),       // 4-byte dictID, 8-byte FCS 0
		blk(hdr(0x81, 0x50, 0xFE, 0x10, 0, 0, 0), 0, 3, true),                      // 1-byte dictID, 4-byte FCS 16
		blk(hdr(0x42, 0x50, 0x34, 0x12, 0x00, 0x01), 0, 3, true),                   // 2-byte dictID, 2-byte FCS
		{0x50, 0x2A, 0x4D, 0x18, 0x03, 0x00, 0x00, 0x00, 'a', 'b', 'c'},            // skippable, variant 0
		{0x5F, 0x2A, 0x4D, 0x18, 0xF7, 0xFF, 0xFF, 0xFF},                           // skippable, size 2^32-9
		{0x5F, 0x2A, 0x4D, 0x18, 0xFF, 0xFF, 0xFF, 0xFF},                           // skippable, size overflows
		{0x5A, 0x2A, 0x4D},                                                         // short skippable magic
		{0x28, 0xB5, 0x2F},                                                         // short zstd magic
		{0x27, 0xB5, 0x2F, 0xFD, 0x00, 0x00},                                       // bad magic
		{0x60, 0x2A, 0x4D, 0x18, 0x00, 0x00, 0x00, 0x00},                           // magic next to the skippable range
	}
}

// checkHeader compares h, decoded from in, with the frame header zfh of in
// and with the first block header that follows it.
func checkHeader(t *testing.T, in []byte, h *Header, zfh frameHeader) {
	t.Helper()
	var want Header
	want.HeaderSize = int(zfh.headerSize)
	if zfh.frameType == zstdSkippableFrame {
		want.Skippable = true
		want.SkippableID = int(zfh.dictID)
		want.SkippableSize = uint32(zfh.frameContentSize)
	} else {
		want.SingleSegment = zfh.singleSegment
		if !zfh.singleSegment {
			want.WindowSize = zfh.windowSize
		}
		want.DictionaryID = zfh.dictID
		want.HasFCS = zfh.frameContentSize != contentSizeUnknown
		if want.HasFCS {
			want.FrameContentSize = zfh.frameContentSize
		}
		want.HasCheckSum = zfh.checksumFlag
		if len(in) >= want.HeaderSize+blockHeaderSize {
			cSize, bp, _ := getcBlockSize(in[want.HeaderSize:])
			fb := &want.FirstBlock
			fb.OK, fb.Last = true, bp.lastBlock
			switch bp.blockType {
			case btCompressed:
				fb.Compressed, fb.CompressedSize = true, cSize
			default:
				fb.DecompressedSize, fb.CompressedSize = int(bp.origSize), int(bp.origSize)
			}
		}
	}
	if *h != want {
		t.Errorf("%d bytes: Header\n%+v, want\n%+v", len(in), *h, want)
	}
}

// cFrameHeader parses cref fheader's fields for in into a frameHeader.
func cFrameHeader(t *testing.T, in []byte, m map[string]string) frameHeader {
	t.Helper()
	u := func(k string) uint64 {
		v, err := strconv.ParseUint(m[k], 10, 64)
		if err != nil {
			t.Fatalf("cref fheader %s=%q: %v", k, m[k], err)
		}
		return v
	}
	zfh := frameHeader{
		frameContentSize: u("frameContentSize"),
		windowSize:       u("windowSize"),
		blockSizeMax:     uint32(u("blockSizeMax")),
		frameType:        frameType(u("frameType")),
		headerSize:       uint32(u("headerSize")),
		dictID:           uint32(u("dictID")),
		checksumFlag:     u("checksumFlag") == 1,
	}
	if zfh.frameType == zstdFrame {
		zfh.singleSegment = in[4]&0x20 != 0
	}
	return zfh
}

// TestHeaderDecode checks Header.Decode and DecodeAndStrip on every decode
// corpus entry against C's frame header, and on its prefixes: fewer bytes
// than the frame header give io.ErrUnexpectedEOF, and FirstBlock.OK is
// false exactly when the first block header is cut.
func TestHeaderDecode(t *testing.T) {
	for _, name := range creftest.FrameNames(t) {
		data := creftest.LoadFrame(t, name).Data
		m := creftest.FHeaderCRef(t, data)
		if m["needed"] != "0" {
			t.Fatalf("%s: cref fheader %v", name, m)
		}
		zfh := cFrameHeader(t, data, m)
		hs := int(zfh.headerSize)
		for _, in := range headerPrefixes(data) {
			var h Header
			err := h.Decode(in)
			var hs2 Header
			remain, err2 := hs2.DecodeAndStrip(in)
			if hs2 != h || !errors.Is(err2, err) {
				t.Errorf("%s, %d bytes: DecodeAndStrip %+v, %v; Decode %+v, %v", name, len(in), hs2, err2, h, err)
			}
			if len(in) < hs {
				if err != io.ErrUnexpectedEOF {
					t.Errorf("%s, %d bytes: err %v, want io.ErrUnexpectedEOF", name, len(in), err)
				}
				if remain != nil {
					t.Errorf("%s, %d bytes: DecodeAndStrip returned %d bytes on error", name, len(in), len(remain))
				}
				continue
			}
			if err != nil {
				t.Errorf("%s, %d bytes: %v", name, len(in), err)
				continue
			}
			checkHeader(t, in, &h, zfh)
			if h.FirstBlock.OK != (!h.Skippable && len(in) >= hs+blockHeaderSize) {
				t.Errorf("%s, %d bytes: FirstBlock.OK %v", name, len(in), h.FirstBlock.OK)
			}
			if !bytes.Equal(remain, in[hs:]) || len(remain) > 0 && &remain[0] != &in[hs] {
				t.Errorf("%s, %d bytes: DecodeAndStrip remain is not in[%d:]", name, len(in), hs)
			}
		}
	}
}

// TestHeaderAppendToRoundTrip checks that Decode then AppendTo reproduces
// the header bytes of every decode corpus entry's first frame, skippable
// frames included.
func TestHeaderAppendToRoundTrip(t *testing.T) {
	for _, name := range creftest.FrameNames(t) {
		data := creftest.LoadFrame(t, name).Data
		var h Header
		if err := h.Decode(data); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		prefix := []byte("prefix")
		got, err := h.AppendTo(bytes.Clone(prefix))
		if err != nil {
			t.Fatalf("%s: AppendTo: %v", name, err)
		}
		if !bytes.HasPrefix(got, prefix) || !bytes.Equal(got[len(prefix):], data[:h.HeaderSize]) {
			t.Errorf("%s: AppendTo\n%x, want\n%x", name, got[len(prefix):], data[:h.HeaderSize])
		}
	}
}

// TestHeaderShape checks Header's fields, in declaration order, against the
// API snapshot, including the anonymous FirstBlock struct.
func TestHeaderShape(t *testing.T) {
	type field struct {
		name string
		typ  reflect.Kind
	}
	check := func(ty reflect.Type, want []field) {
		t.Helper()
		if ty.NumField() != len(want) {
			t.Fatalf("%v: %d fields, want %d", ty, ty.NumField(), len(want))
		}
		for i, w := range want {
			f := ty.Field(i)
			if f.Name != w.name || f.Type.Kind() != w.typ || f.Anonymous {
				t.Errorf("%v field %d: %s %v, want %s %v", ty, i, f.Name, f.Type, w.name, w.typ)
			}
		}
	}
	ht := reflect.TypeOf(Header{})
	check(ht, []field{
		{"SingleSegment", reflect.Bool},
		{"WindowSize", reflect.Uint64},
		{"DictionaryID", reflect.Uint32},
		{"HasFCS", reflect.Bool},
		{"FrameContentSize", reflect.Uint64},
		{"Skippable", reflect.Bool},
		{"SkippableID", reflect.Int},
		{"SkippableSize", reflect.Uint32},
		{"HeaderSize", reflect.Int},
		{"FirstBlock", reflect.Struct},
		{"HasCheckSum", reflect.Bool},
	})
	fb, _ := ht.FieldByName("FirstBlock")
	if fb.Type.Name() != "" {
		t.Errorf("FirstBlock is the named type %v, want an anonymous struct", fb.Type)
	}
	check(fb.Type, []field{
		{"OK", reflect.Bool},
		{"Last", reflect.Bool},
		{"Compressed", reflect.Bool},
		{"DecompressedSize", reflect.Int},
		{"CompressedSize", reflect.Int},
	})
}

// FuzzHeaderDecode checks that Header.Decode never panics, agrees with
// getFrameHeaderAdvanced and getcBlockSize, and that AppendTo's output
// decodes back to the same Header (without FirstBlock; HeaderSize is that
// of AppendTo's shortest encoding).
func FuzzHeaderDecode(f *testing.F) {
	for _, in := range headerFuzzSeeds() {
		f.Add(in)
	}
	ents, _ := os.ReadDir(seedsDir)
	for _, e := range ents {
		if b, err := os.ReadFile(filepath.Join(seedsDir, e.Name())); err == nil {
			f.Add(b[:min(len(b), HeaderMaxSize+8)])
		}
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		var h Header
		err := h.Decode(in)

		var zfh frameHeader
		need, e := getFrameHeaderAdvanced(&zfh, in)
		var wantErr error
		switch {
		case e != 0:
			wantErr = e.toError("")
		case need > 0:
			wantErr = io.ErrUnexpectedEOF
		case zfh.frameType == zstdFrame && len(in) >= int(zfh.headerSize)+blockHeaderSize:
			if _, _, e := getcBlockSize(in[zfh.headerSize:]); e != 0 {
				wantErr = e.toError("")
			}
		}
		if wantErr != nil {
			var ce, wce *corruptError
			if errors.As(wantErr, &wce) && !(errors.As(err, &ce) && ce.code == wce.code) ||
				wce == nil && err != wantErr {
				t.Fatalf("Decode: %v, want %v", err, wantErr)
			}
			return
		}
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		checkHeader(t, in, &h, zfh)

		out, err := h.AppendTo(nil)
		if err != nil {
			t.Fatal(err)
		}
		var h2 Header
		if err := h2.Decode(out); err != nil {
			t.Fatalf("Decode(AppendTo %x): %v", out, err)
		}
		want := h
		want.FirstBlock = h2.FirstBlock // out has no block header
		want.HeaderSize = len(out)
		if h2 != want || h2.FirstBlock.OK {
			t.Fatalf("AppendTo %x decodes to\n%+v, want\n%+v", out, h2, want)
		}
	})
}
