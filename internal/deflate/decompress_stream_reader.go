// StreamReader: the io.Reader side of the streaming decoder used by packages
// flate, gzip and zlib. New design (libdeflate has no streaming API, see
// decompress_stream.go and the T3.4 design note); no C counterpart.

package deflate

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

const (
	// streamWindow is the history kept for matches: DEFLATE's 32 KiB
	// window (libdeflate's largest decodable offset is 32768 too).
	streamWindow = deflateMaxMatchOffset
	// streamWinSize is the size of the window buffer: the history plus
	// 96 KiB of output decoded per call, so sliding the history to the
	// front costs one 32 KiB copy per 96 KiB of output.
	streamWinSize = streamWindow + 96<<10
	// streamWinStart and streamInStart are the sizes of the window and
	// input buffers first allocated: both grow to their full size
	// (streamWinSize, streamInSize) when the first window is full, so a
	// Reader of small streams stays small.
	streamWinStart = 16 << 10
	streamInStart  = 4 << 10
	// streamMinRoom: with less output space than this the history slides
	// first, so the fast loop can run (and any symbol fits).
	streamMinRoom = fastloopMaxBytesWritten
	// streamInSize is the size of the input buffer.
	streamInSize = 32 << 10
	// maxEmptyReads is the number of consecutive (0, nil) reads of the
	// source after which io.ErrNoProgress is returned (as bufio does).
	maxEmptyReads = 100
)

// errStreamRerun: the generic-loop rerun after ErrBadData did not fail
// again (unreachable: accept/reject decisions do not depend on the loop).
var errStreamRerun = errors.New("deflate: internal error: the rerun of invalid data did not fail")

// streamRerun is the rerun's decoder (a variable for the test of the
// errStreamRerun check).
var streamRerun = (*Decompressor).decompressStream

// How the source is read (StreamReader.mode).
const (
	srcBulk = iota // no io.ByteReader: bulk Read, may read past the stream
	srcPeek        // *bufio.Reader: Peek ahead, Discard what is consumed
	srcBuf         // *bytes.Buffer: copy from Bytes, Next what is consumed
	srcSeek        // io.ByteReader and io.Seeker: bulk Read, seek back at the end
	srcByte        // other io.ByteReader: one ReadByte at a time, when needed
)

// A StreamReader decompresses a raw DEFLATE stream read from an io.Reader,
// and gives the container formats raw access to the bytes around it (the
// gzip and zlib headers and trailers). If the source implements
// io.ByteReader, no byte after the end of what was asked for (the DEFLATE
// stream, ReadFull, ReadByte) is consumed from it, once Unread is called
// at the end of the data: a *bufio.Reader is peeked ahead and only consumed
// bytes are discarded; a *bytes.Buffer is copied from (Bytes) and only
// consumed bytes are taken out (Next); a source that implements io.Seeker
// is read ahead and Unread seeks back over the unconsumed bytes; any other
// io.ByteReader is read one byte at a time (the body of a stored block in
// one Read). Without io.ByteReader the source is read ahead in pieces of up
// to 32 KiB and bytes after the end are lost (as with the standard
// library, which wraps such sources in a bufio.Reader).
//
// The zero value needs Reset (or ResetSource and ResetStream) before use.
// Buffers (a 16 KiB window and 4 KiB of input, grown to 128 KiB and 32 KiB
// when the first window is full) are allocated by the first Reset and
// kept.
type StreamReader struct {
	d Decompressor
	s streamState

	// win[:wpos] is the output so far (its last 32 KiB at least, or the
	// preset dictionary and everything after it); win[rpos:wpos] has not
	// been returned by Read yet.
	win        []byte
	rpos, wpos int

	// in[ipos:iend] is input read from the source and not consumed yet; in
	// peek mode its first ndisc bytes were already discarded from the
	// source (the others are still buffered there; in buffer mode all are
	// still in the source). base is the number of source bytes before
	// in[0], start that before the DEFLATE stream.
	in         []byte
	ipos, iend int
	ndisc      int
	base       int64
	start      int64

	r      io.Reader
	br     io.ByteReader
	pk     *bufio.Reader
	bb     *bytes.Buffer
	sk     io.Seeker
	mode   int
	srcErr error // sticky error of the source

	err        error // sticky result of the DEFLATE stream (io.EOF at its end)
	corruptOff int64 // input offset reported for ErrBadData
	calls      int   // decoder calls since ResetSource (for tests)
}

// Reset is ResetSource(r) followed by ResetStream(dict).
func (z *StreamReader) Reset(r io.Reader, dict []byte) {
	z.ResetSource(r)
	z.ResetStream(dict)
}

// ResetSource makes z read from r, dropping any buffered input and the
// stream state. ResetStream must be called before Read.
func (z *StreamReader) ResetSource(r io.Reader) {
	z.r, z.br, z.pk, z.bb, z.sk = r, nil, nil, nil, nil
	z.mode = srcBulk
	if br, ok := r.(io.ByteReader); ok {
		z.br = br
		z.mode = srcByte
		// Only bufio's and bytes' own types: other types with the same
		// methods need not have their exact semantics.
		switch v := r.(type) {
		case *bufio.Reader:
			z.pk, z.mode = v, srcPeek
		case *bytes.Buffer:
			z.bb, z.mode = v, srcBuf
		case io.Seeker:
			z.sk, z.mode = v, srcSeek
		}
	}
	if z.in == nil {
		z.in = make([]byte, streamInStart)
	}
	z.ipos, z.iend, z.ndisc, z.base = 0, 0, 0, 0
	z.srcErr = nil
	z.err = nil
	z.calls = 0
}

// ResetStream starts a new DEFLATE stream at the current input position,
// with the preset dictionary dict (only its last 32 KiB are used; nil for
// none).
func (z *StreamReader) ResetStream(dict []byte) {
	dict = dict[max(0, len(dict)-streamWindow):]
	if len(z.win) < len(dict)+streamMinRoom {
		// First use, or a dictionary larger than the first window.
		size := streamWinStart
		if len(dict)+streamMinRoom > size {
			size = streamWinSize
			z.in = append(z.in, make([]byte, streamInSize-len(z.in))...)
		}
		z.win = make([]byte, size)
	}
	z.s = streamState{}
	n := copy(z.win, dict)
	z.rpos, z.wpos = n, n
	z.start = z.base + int64(z.ipos)
	z.err = nil
}

// Read reads decompressed data. At the end of the stream it returns io.EOF
// (with the last data, if they are returned by the same call); invalid data
// gives ErrBadData (see CorruptOffset), a stream cut short
// io.ErrUnexpectedEOF, and an error of the source is returned as it is.
// Errors are sticky. Read with an empty p decodes until some output is
// available or an error occurs, and returns 0 and that error (nil if
// output is pending).
func (z *StreamReader) Read(p []byte) (int, error) {
	for {
		if z.rpos < z.wpos {
			n := copy(p, z.win[z.rpos:z.wpos])
			z.rpos += n
			if z.rpos == z.wpos && z.err != nil {
				return n, z.err
			}
			return n, nil
		}
		if z.err != nil {
			return 0, z.err
		}
		z.step()
	}
}

// step decodes until some output is produced, the stream ends, or an error
// occurs (z.err). All output must have been read (rpos == wpos).
func (z *StreamReader) step() {
	if len(z.win)-z.wpos < streamMinRoom {
		if len(z.win) < streamWinSize {
			z.grow()
		} else {
			copy(z.win, z.win[z.wpos-streamWindow:z.wpos])
			z.rpos, z.wpos = streamWindow, streamWindow
		}
	}
	produced := false
	for {
		prev := z.s
		z.calls++
		res, nIn, nOut := z.d.decompressStream(&z.s, z.in[z.ipos:z.iend], z.win, z.wpos)
		if res != libdeflateSuccess {
			// The output before the invalid data is returned with the
			// error. The fast loop does not report it: decode this
			// input again with the generic loop only, from the last
			// block start the call reached (the decode tables are that
			// block's) or else from the call's start.
			s, in, out := prev, z.ipos, z.wpos
			if z.s.blkSet {
				s = streamState{bitbuf: z.s.blkBitbuf, bitsleft: z.s.blkBitsleft}
				in += z.s.blkIn
				out = z.s.blkOut
			}
			s.slow = true
			// The same input fails again (accept/reject decisions do not
			// depend on the loop), and reports the input position just
			// after the invalid item.
			res, nIn, nOut = streamRerun(&z.d, &s, z.in[in:z.iend], z.win, out)
			if res != libdeflateBadData {
				z.err = errStreamRerun
				return
			}
			z.wpos = nOut
			z.corruptOff = z.base + int64(in+nIn) - z.start
			z.err = ErrBadData
			return
		}
		z.consume(nIn)
		produced = produced || nOut != z.wpos
		z.wpos = nOut
		switch {
		case z.s.stop == stopEnd:
			z.err = io.EOF
			return
		case z.s.stop == stopOutputFull:
			if len(z.win) < streamWinSize {
				// Decode on in the full window: Read returns the
				// same amounts as without the small first window.
				z.grow()
				continue
			}
			// (Always with output: there were at least streamMinRoom
			// bytes of space.)
			return
		case produced && z.s.state == stHeader:
			// At a block boundary the output is returned before more
			// input is read, so the data before a sync flush can be
			// read without more input. Inside a block more input is
			// read first, as the standard library does (a stream cut
			// there returns its last data with io.ErrUnexpectedEOF).
			return
		}
		need := 1
		if z.s.state == stStored {
			need = z.s.storedLeft // all needed: the body of a stored block
		}
		if err := z.fill(need); err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			z.err = err
			return
		}
	}
}

// grow is called when the first window is full (a large stream): the
// buffers grow to their full size.
func (z *StreamReader) grow() {
	z.win = append(z.win, make([]byte, streamWinSize-len(z.win))...)
	z.in = append(z.in, make([]byte, streamInSize-len(z.in))...)
}

// Err returns the sticky result of the stream: nil while it is being
// decoded, io.EOF after its end, else the error Read returns.
func (z *StreamReader) Err() error { return z.err }

// CorruptOffset returns the input offset to report with ErrBadData, counted
// from the start of the DEFLATE stream (the input position at
// ResetStream): the number of input bytes up to the last bit read of the
// invalid block header or match, an upper bound of the position of the
// invalid data that does not extend past the end of the DEFLATE stream.
func (z *StreamReader) CorruptOffset() int64 { return z.corruptOff }

// consume marks n bytes of in[ipos:] as consumed (in peek and buffer mode
// taking them out of the source).
func (z *StreamReader) consume(n int) {
	z.ipos += n
	switch z.mode {
	case srcPeek:
		k := min(n, z.ndisc)
		z.ndisc -= k
		if n > k {
			// Cannot fail: the bytes are buffered (they were peeked).
			z.pk.Discard(n - k)
		}
	case srcBuf:
		z.bb.Next(n)
	}
}

// fill reads more input into in[iend:] (at least one byte) or returns the
// source's (sticky) error. need (at least 1) is the number of bytes the
// caller is sure to consume: in byte mode that many are read at once.
func (z *StreamReader) fill(need int) error {
	if z.srcErr != nil {
		return z.srcErr
	}
	if z.ipos > 0 {
		z.iend = copy(z.in, z.in[z.ipos:z.iend])
		z.base += int64(z.ipos)
		z.ipos = 0
	}
	end := len(z.in)
	switch z.mode {
	case srcByte:
		if need <= 1 {
			return z.fillByte()
		}
		// One Read of at most the bytes needed (a Read never returns
		// more than asked for, so nothing past them is consumed).
		end = min(end, z.iend+need)
	case srcPeek:
		return z.fillPeek()
	case srcBuf:
		return z.fillBuf()
	}
	for range maxEmptyReads {
		n, err := z.r.Read(z.in[z.iend:end])
		z.iend += n
		if err != nil {
			z.srcErr = err
			if n > 0 {
				return nil
			}
			return err
		}
		if n > 0 {
			return nil
		}
	}
	z.srcErr = io.ErrNoProgress
	return z.srcErr
}

// fillByte reads one byte with ReadByte (consuming it from the source).
func (z *StreamReader) fillByte() error {
	c, err := z.br.ReadByte()
	if err != nil {
		z.srcErr = err
		return err
	}
	z.in[z.iend] = c
	z.iend++
	if z.mode == srcPeek {
		z.ndisc++
	}
	return nil
}

// fillPeek copies the bytes buffered in the source after the ones already
// in in (waiting for at least one), without consuming them.
func (z *StreamReader) fillPeek() error {
	have := z.iend - z.ipos - z.ndisc // in in and still buffered in the source
	want := max(z.pk.Buffered(), have+1)
	want = min(want, have+len(z.in)-z.iend)
	p, err := z.pk.Peek(want)
	if len(p) > have {
		z.iend += copy(z.in[z.iend:], p[have:])
		return nil
	}
	if err == bufio.ErrBufferFull {
		// The source's buffer is too small to look further ahead: the
		// decoder needs all of these bytes, so consume them and read
		// on byte by byte.
		z.pk.Discard(have)
		z.ndisc += have
		return z.fillByte()
	}
	z.srcErr = err
	return err
}

// fillBuf copies the bytes of the *bytes.Buffer source after the ones
// already in in, without taking them out of it (io.EOF if there are none;
// io.ErrUnexpectedEOF if the caller took bytes out of the buffer that were
// not consumed yet).
func (z *StreamReader) fillBuf() error {
	k := z.iend - z.ipos
	if k > z.bb.Len() {
		z.srcErr = io.ErrUnexpectedEOF
		return z.srcErr
	}
	b := z.bb.Bytes()[k:]
	if len(b) == 0 {
		z.srcErr = io.EOF
		return io.EOF
	}
	z.iend += copy(z.in[z.iend:], b)
	return nil
}

// ReadFull reads exactly len(p) raw bytes following the consumed input (a
// header or trailer), with io.ReadFull's errors: io.EOF if none could be
// read, io.ErrUnexpectedEOF if some, or the source's error.
func (z *StreamReader) ReadFull(p []byte) error {
	n := 0
	for n < len(p) {
		if z.ipos == z.iend {
			if err := z.fill(len(p) - n); err != nil {
				if n > 0 && err == io.EOF {
					err = io.ErrUnexpectedEOF
				}
				return err
			}
		}
		k := copy(p[n:], z.in[z.ipos:z.iend])
		z.consume(k)
		n += k
	}
	return nil
}

// ReadByte reads one raw byte following the consumed input.
func (z *StreamReader) ReadByte() (byte, error) {
	if z.ipos == z.iend {
		if err := z.fill(1); err != nil {
			return 0, err
		}
	}
	c := z.in[z.ipos]
	z.consume(1)
	return c, nil
}

// Unread marks the end of the data: a seekable io.ByteReader source is
// moved back over the bytes read ahead but not consumed, which are dropped
// from the input buffer. For other sources it does nothing (a
// *bufio.Reader or *bytes.Buffer never had them consumed; a byte mode
// source has none). A
// Seek error is returned and becomes the sticky error of Read.
func (z *StreamReader) Unread() error {
	if z.mode != srcSeek || z.ipos == z.iend {
		return nil
	}
	if _, err := z.sk.Seek(int64(z.ipos-z.iend), io.SeekCurrent); err != nil {
		z.err = err
		return err
	}
	z.base += int64(z.ipos)
	z.ipos, z.iend = 0, 0
	z.srcErr = nil
	return nil
}
