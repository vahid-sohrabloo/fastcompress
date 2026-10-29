package zstd

import (
	"errors"
	"io"
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
