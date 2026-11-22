package main

// Streaming benchmarks, side by side with klauspost/compress, shaped after
// kopia's use of the zstd package (repo/compression/compressor_zstd.go):
//
//   - decode: one pooled NewReader(nil, WithDecoderConcurrency(1)), then per
//     chunk Reset(bytes.NewReader(frame)) and io.Copy(dst, dec), which uses
//     the Decoder's WriteTo;
//   - encode: one NewWriter(nil, WithEncoderLevel(l)), then per chunk
//     Reset(dst), io.Copy(enc, bytes.NewReader(chunk)) (the Encoder's
//     ReadFrom) and Close.
//
// The input is FC_STREAM_DATA (for instance a slice of a Linux kernel tar or
// of silesia.tar), cut into independent chunks; the benchmarks are skipped
// when it is not set. FC_STREAM_MAX limits the bytes used (default 32 MiB).
// Every decoder decodes the same frames, made by our streaming Encoder
// (byte-identical to C), as kopia with fastcompress would store them; the
// "fcs" frames are EncodeAll's, which carry the content size.
//
//	FC_STREAM_DATA=linux.tar go test -run '^$' -bench 'Stream' -benchtime 2s

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"testing"

	kzstd "github.com/klauspost/compress/zstd"
	ozstd "github.com/vahid-sohrabloo/fastcompress/zstd"
)

var streamData = sync.OnceValues(func() ([]byte, error) {
	path := os.Getenv("FC_STREAM_DATA")
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	limit := 32 << 20
	if s := os.Getenv("FC_STREAM_MAX"); s != "" {
		if limit, err = strconv.Atoi(s); err != nil {
			return nil, err
		}
	}
	return b[:min(len(b), limit)], nil
})

func benchData(b *testing.B) []byte {
	data, err := streamData()
	if err != nil {
		b.Fatal(err)
	}
	if data == nil {
		b.Skip("FC_STREAM_DATA not set")
	}
	return data
}

func split(data []byte, chunk int) [][]byte {
	var out [][]byte
	for off := 0; off < len(data); off += chunk {
		out = append(out, data[off:min(off+chunk, len(data))])
	}
	return out
}

var ourLevels = map[int]ozstd.EncoderLevel{1: ozstd.SpeedFastest, 3: ozstd.SpeedDefault, 7: ozstd.SpeedBetterCompression, 11: ozstd.SpeedBestCompression}
var kLevels = map[int]kzstd.EncoderLevel{1: kzstd.SpeedFastest, 3: kzstd.SpeedDefault, 7: kzstd.SpeedBetterCompression, 11: kzstd.SpeedBestCompression}

// streamDecoder is the part of both Decoders the benchmark uses.
type streamDecoder interface {
	io.Reader
	io.WriterTo
	Reset(io.Reader) error
}

type kDec struct{ *kzstd.Decoder }

func newDecoder(b *testing.B, lib string, conc int) streamDecoder {
	switch lib {
	case "ours":
		var opts []ozstd.DOption
		if conc >= 0 {
			opts = append(opts, ozstd.WithDecoderConcurrency(conc))
		}
		d, err := ozstd.NewReader(nil, opts...)
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(d.Close)
		return d
	default:
		var opts []kzstd.DOption
		if conc >= 0 {
			opts = append(opts, kzstd.WithDecoderConcurrency(conc))
		}
		d, err := kzstd.NewReader(nil, opts...)
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(d.Close)
		return kDec{d}
	}
}

// encodeFrames compresses each chunk as kopia does (frames without a
// content size) or, with fcs, with EncodeAll (frames with one). multi > 1
// concatenates that many frames per decoded stream.
func encodeFrames(b *testing.B, chunks [][]byte, level int, fcs bool, multi int) [][]byte {
	enc, err := ozstd.NewWriter(nil, ozstd.WithEncoderLevel(ourLevels[level]))
	if err != nil {
		b.Fatal(err)
	}
	var frames [][]byte
	for i := 0; i < len(chunks); i += multi {
		var buf bytes.Buffer
		for _, c := range chunks[i:min(i+multi, len(chunks))] {
			if fcs {
				buf.Write(enc.EncodeAll(c, nil))
				continue
			}
			enc.Reset(&buf)
			if _, err := io.Copy(enc, bytes.NewReader(c)); err != nil {
				b.Fatal(err)
			}
			if err := enc.Close(); err != nil {
				b.Fatal(err)
			}
		}
		frames = append(frames, buf.Bytes())
	}
	return frames
}

// onlyReader hides a Reader's WriteTo, so that io.CopyBuffer uses Read.
type onlyReader struct{ r io.Reader }

func (o onlyReader) Read(p []byte) (int, error) { return o.r.Read(p) }

// onlyWriter hides a Writer's ReadFrom.
type onlyWriter struct{ w io.Writer }

func (o onlyWriter) Write(p []byte) (int, error) { return o.w.Write(p) }

type decCase struct {
	level, chunk int
	fcs          bool
	multi        int
	consume      string // writeto, writeto-discard, readN
	conc         int    // -1: the default
}

func (c decCase) name() string {
	frames := "stream"
	if c.fcs {
		frames = "fcs"
	}
	if c.multi > 1 {
		frames += fmt.Sprintf("x%d", c.multi)
	}
	conc := "cdef"
	if c.conc >= 0 {
		conc = fmt.Sprintf("c%d", c.conc)
	}
	return fmt.Sprintf("L%d/%dM/%s/%s/%s", c.level, c.chunk>>20, frames, c.consume, conc)
}

func decCases() []decCase {
	var cs []decCase
	for _, level := range []int{1, 3} {
		for _, chunk := range []int{1 << 20, 4 << 20, 8 << 20} {
			cs = append(cs, decCase{level, chunk, false, 1, "writeto", 1})
		}
	}
	for _, v := range []decCase{
		{3, 4 << 20, true, 1, "writeto", 1},
		{3, 1 << 20, false, 4, "writeto", 1},
		{3, 1 << 20, true, 4, "writeto", 1},
		{3, 4 << 20, false, 1, "writeto-discard", 1},
		{3, 4 << 20, false, 1, "read4K", 1},
		{3, 4 << 20, false, 1, "read64K", 1},
		{3, 4 << 20, false, 1, "read1M", 1},
		{3, 4 << 20, false, 1, "writeto", -1},
		{3, 4 << 20, false, 1, "writeto", 4},
	} {
		cs = append(cs, v)
	}
	return cs
}

func BenchmarkStreamDecode(b *testing.B) {
	for _, c := range decCases() {
		b.Run(c.name(), func(b *testing.B) {
			data := benchData(b)
			frames := encodeFrames(b, split(data, c.chunk), c.level, c.fcs, c.multi)
			for _, lib := range []string{"ours", "klauspost"} {
				b.Run(lib, func(b *testing.B) {
					benchDecode(b, lib, c, data, frames)
				})
			}
		})
	}
}

func benchDecode(b *testing.B, lib string, c decCase, data []byte, frames [][]byte) {
	dec := newDecoder(b, lib, c.conc)
	var out bytes.Buffer
	out.Grow(c.chunk * c.multi)
	var rbuf []byte
	if n, ok := bytes.CutPrefix([]byte(c.consume), []byte("read")); ok {
		size := 0
		switch string(n) {
		case "4K":
			size = 4 << 10
		case "64K":
			size = 64 << 10
		case "1M":
			size = 1 << 20
		}
		rbuf = make([]byte, size)
	}
	var br bytes.Reader
	one := func(fr []byte) {
		br.Reset(fr)
		if err := dec.Reset(&br); err != nil {
			b.Fatal(err)
		}
		out.Reset()
		var err error
		switch {
		case rbuf != nil:
			_, err = io.CopyBuffer(onlyWriter{&out}, onlyReader{dec}, rbuf)
		case c.consume == "writeto-discard":
			_, err = io.Copy(io.Discard, dec)
		default:
			_, err = io.Copy(&out, dec)
		}
		if err != nil {
			b.Fatal(err)
		}
	}
	// Untimed check of every stream.
	off := 0
	for _, fr := range frames {
		one(fr)
		if c.consume != "writeto-discard" {
			if !bytes.Equal(out.Bytes(), data[off:off+out.Len()]) {
				b.Fatalf("%s: decoded output differs from the input", lib)
			}
			off += out.Len()
		}
	}
	if c.consume != "writeto-discard" && off != len(data) {
		b.Fatalf("%s: decoded %d bytes, want %d", lib, off, len(data))
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for _, fr := range frames {
			one(fr)
		}
	}
}

// BenchmarkStreamEncode is kopia's Compress: Reset, io.Copy (ReadFrom) and
// Close per chunk, at each level, with the default concurrency (kopia's
// NewWriter has no WithEncoderConcurrency) and with 1. The ratio metric is
// input/output bytes.
func BenchmarkStreamEncode(b *testing.B) {
	for _, level := range []int{1, 3, 7, 11} {
		for _, chunk := range []int{1 << 20, 4 << 20, 8 << 20} {
			for _, conc := range []int{-1, 1} {
				cname := "cdef"
				if conc > 0 {
					cname = "c1"
				}
				b.Run(fmt.Sprintf("L%d/%dM/%s", level, chunk>>20, cname), func(b *testing.B) {
					data := benchData(b)
					chunks := split(data, chunk)
					for _, lib := range []string{"ours", "klauspost"} {
						b.Run(lib, func(b *testing.B) {
							benchEncode(b, lib, level, conc, data, chunks)
						})
					}
				})
			}
		}
	}
}

func benchEncode(b *testing.B, lib string, level, conc int, data []byte, chunks [][]byte) {
	var w interface {
		io.WriteCloser
		io.ReaderFrom
	}
	var reset func(io.Writer)
	switch lib {
	case "ours":
		opts := []ozstd.EOption{ozstd.WithEncoderLevel(ourLevels[level])}
		if conc > 0 {
			opts = append(opts, ozstd.WithEncoderConcurrency(conc))
		}
		e, err := ozstd.NewWriter(nil, opts...)
		if err != nil {
			b.Fatal(err)
		}
		w, reset = e, e.Reset
	default:
		opts := []kzstd.EOption{kzstd.WithEncoderLevel(kLevels[level])}
		if conc > 0 {
			opts = append(opts, kzstd.WithEncoderConcurrency(conc))
		}
		e, err := kzstd.NewWriter(nil, opts...)
		if err != nil {
			b.Fatal(err)
		}
		w, reset = e, e.Reset
	}
	var out bytes.Buffer
	var br bytes.Reader
	total := 0
	one := func(c []byte) {
		out.Reset()
		reset(&out)
		br.Reset(c)
		if _, err := io.Copy(w, &br); err != nil {
			b.Fatal(err)
		}
		if err := w.Close(); err != nil {
			b.Fatal(err)
		}
		total += out.Len()
	}
	for _, c := range chunks {
		one(c)
	}
	ratio := float64(len(data)) / float64(total)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for _, c := range chunks {
			one(c)
		}
	}
	b.ReportMetric(ratio, "ratio") // after the loop: ResetTimer drops metrics
}
