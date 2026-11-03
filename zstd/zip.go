package zstd

import (
	"errors"
	"io"
	"strconv"
	"sync"
)

// ZipMethodWinZip is the method for Zstandard compressed data inside Zip files
// for WinZip.
// See https://www.winzip.com/win/en/comp_info.html
const ZipMethodWinZip = 93

// ZipMethodPKWare is the original method number used by PKWARE to indicate
// Zstandard compression.
// Deprecated: This has been deprecated by PKWARE, use ZipMethodWinZip instead
// for compression.
// See https://pkware.cachefly.net/webdocs/APPNOTE/APPNOTE-6.3.9.TXT
const ZipMethodPKWare = 20

// ZipCompressor returns a compressor that can be registered with zip
// libraries. The provided encoder options will be used on all encodes.
//
// Each entry is one streaming frame, exactly what an Encoder created with
// opts writes after Reset. An error in opts is returned by the function
// when it is called for an entry.
func ZipCompressor(opts ...EOption) func(w io.Writer) (io.WriteCloser, error) {
	var pool sync.Pool
	return func(w io.Writer) (io.WriteCloser, error) {
		e, _ := pool.Get().(*Encoder)
		if e == nil {
			var err error
			if e, err = NewWriter(w, opts...); err != nil {
				return nil, err
			}
		} else {
			e.Reset(w)
		}
		return &zipWriter{enc: e, pool: &pool}, nil
	}
}

// zipWriter hands its Encoder back to the pool when closed. The Encoder is a
// named field so that no other Encoder method (Flush, ReadFrom, ...) is
// promoted: after Close it would act on a nil or re-pooled Encoder.
type zipWriter struct {
	enc  *Encoder
	pool *sync.Pool
}

func (z *zipWriter) Close() error {
	if z.enc == nil {
		return nil
	}
	err := z.enc.Close()
	z.enc.Reset(nil) // do not keep the entry's writer alive
	z.pool.Put(z.enc)
	z.enc = nil
	return err
}

func (z *zipWriter) Write(p []byte) (int, error) {
	if z.enc == nil {
		return 0, errZipClosed
	}
	return z.enc.Write(p)
}

var errZipClosed = errors.New("zstd: write to closed zip entry")

// zipMaxWindow is ZipDecompressor's default max window (doc text: 128MB).
const zipMaxWindow = 128 << 20

// zipPoolSize is the number of idle Decoders a ZipDecompressor keeps:
// 8 on 64-bit platforms, 4 on 32-bit ones. Each Decoder keeps its stream
// buffers (up to the window size plus a block) while idle.
const zipPoolSize = strconv.IntSize / 8

// ZipDecompressor returns a decompressor that can be registered with zip libraries.
// See ZipCompressor for example.
// Options can be specified. WithDecoderConcurrency(1) is forced,
// and by default a 128MB maximum decompression window is specified.
// The window size can be overridden if required.
//
// Each open entry decodes with its own Decoder, so its own memory limits;
// Close hands the Decoder back to a pool that keeps at most 8 idle
// Decoders (4 on 32-bit platforms) and closes the others. An error in opts
// is returned by every Read of the entry.
func ZipDecompressor(opts ...DOption) func(r io.Reader) io.ReadCloser {
	all := make([]DOption, 0, len(opts)+2)
	all = append(all, WithDecoderMaxWindow(zipMaxWindow))
	all = append(all, opts...)
	all = append(all, WithDecoderConcurrency(1))
	pool := make(chan *Decoder, zipPoolSize)
	return func(r io.Reader) io.ReadCloser {
		var d *Decoder
		select {
		case d = <-pool:
		default:
			var err error
			if d, err = NewReader(nil, all...); err != nil {
				return &zipReader{err: err}
			}
		}
		if err := d.Reset(r); err != nil {
			d.Close()
			return &zipReader{err: err}
		}
		return &zipReader{dec: d, pool: pool}
	}
}

// zipReader hands its Decoder back to the pool when closed. The Decoder is
// a named field so that no other Decoder method is promoted (see
// zipWriter). err, when set, is returned by every Read.
type zipReader struct {
	dec  *Decoder
	pool chan *Decoder
	err  error
}

func (z *zipReader) Read(p []byte) (int, error) {
	if z.err != nil {
		return 0, z.err
	}
	if z.dec == nil {
		return 0, errZipReadClosed
	}
	return z.dec.Read(p)
}

func (z *zipReader) Close() error {
	d := z.dec
	if d == nil {
		return nil
	}
	z.dec = nil
	if d.Reset(nil) != nil { // do not keep the entry's reader alive
		d.Close()
		return nil
	}
	select {
	case z.pool <- d:
	default:
		d.Close()
	}
	return nil
}

var errZipReadClosed = errors.New("zstd: read from closed zip entry")
