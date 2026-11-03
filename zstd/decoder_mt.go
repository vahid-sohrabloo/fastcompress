package zstd

import (
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"sync/atomic"
)

// The multi-threaded stream (WithDecoderConcurrency(n), n > 1): a pipeline
// of three stages, started by the first Read or WriteTo after a Reset (no
// C counterpart; C's decoder is single-threaded, see
// docs/superpowers/phase2-api-decisions.md, "Pipelined streams").
//
//   - S1, the framer (a goroutine): reads the io.Reader as the n == 1
//     stream does (readInput) and cuts the input into units with C's
//     frame structure: a frame header, a block (header and content), a
//     checksum, a skippable frame's header or content. Input it cannot cut
//     (bytes the stream will reject, or input that ends) goes in plain
//     units.
//   - S2, the entropy stage (a goroutine): decodes the literals and the
//     sequences of each compressed block (decodeBlockSeqs), with its own
//     context's entropy state.
//   - S3, the executor (the caller's goroutine, in Read or WriteTo): runs
//     the n == 1 stream (decompressStream on the stream's context) on the
//     units' bytes, except that a compressed block is executed from S2's
//     result (execBlockSeqs, see pipeExec). Everything else, frame
//     headers, the window, raw and RLE blocks, the checksum, the output
//     buffer and the errors, is the n == 1 stream's own code.
//
// So the output and the error are the n == 1 stream's, delivered the same
// way, for any input. units holds at most min(n, maxReadAhead) units
// between S1 and S2; S2 hands each unit to S3 directly (blocks is
// unbuffered), so at most two decoded blocks exist: S3's and S2's next.
//
// Each pipeline is one generation: stopping it closes done and waits for
// S2 only. S1 may be inside the io.Reader's Read, which cannot be
// interrupted: it is left to finish that read, then sees done and exits,
// touching nothing but its own generation's channels, its own input
// buffer and pooled units. So Reset and Close never block on the reader,
// and S1 outlives them only until that Read returns.
type pipeline struct {
	done   chan struct{}
	units  chan *blockUnit // S1 → S2
	blocks chan *blockUnit // S2 → S3
	wg     sync.WaitGroup  // S2
	cur    *blockUnit      // S3: the unit s.in holds
}

// maxReadAhead caps the units queued between S1 and S2: with two compute
// stages a deeper queue buys nothing, and the pipeline's memory then does
// not grow with n.
const maxReadAhead = 4

// unitKind is what a blockUnit holds.
type unitKind uint8

const (
	unitBytes unitKind = iota // input for S3 only
	unitFrame                 // a frame header: S2 begins the frame
	unitBlock                 // a compressed block (header and content): S2 decodes it
	unitEnd                   // the end of the input: err, or io.EOF when nil
)

// blockUnit is one piece of the input, in input order.
type blockUnit struct {
	kind unitKind
	data []byte // from unitPool, at most dStreamInSize bytes
	// unitFrame: the frame's header and dictionary (C:
	// ZSTD_DCtx_selectFrameDDict)
	zfh frameHeader
	dd  *ddict
	blk *decodedBlock // unitBlock, set by S2
	err error         // unitEnd
	// unitEnd: the io.Reader's Read panicked with panicVal (re-raised on
	// the caller's goroutine by readUnit, as with n == 1)
	panicked bool
	panicVal any
}

// The pipeline's buffers come from pools: units hold up to dStreamInSize
// bytes, decoded blocks up to blockSizeMax literals.
var (
	unitPool  = sync.Pool{New: func() any { return new(blockUnit) }}
	blockPool = sync.Pool{New: func() any { return new(decodedBlock) }}
)

// getUnit returns a unit holding a copy of b (at most dStreamInSize
// bytes; its buffer grows to the largest it has held).
func getUnit(kind unitKind, b []byte) *blockUnit {
	u := unitPool.Get().(*blockUnit)
	u.kind = kind
	if cap(u.data) < len(b) {
		u.data = make([]byte, 0, len(b))
	}
	u.data = append(u.data[:0], b...)
	return u
}

// putUnit returns u, and its decoded block, to their pools.
func putUnit(u *blockUnit) {
	if u.blk != nil {
		putBlock(u.blk)
	}
	*u = blockUnit{data: u.data[:0]}
	unitPool.Put(u)
}

func putBlock(b *decodedBlock) {
	*b = decodedBlock{lits: b.lits[:0], seqs: b.seqs[:0]}
	blockPool.Put(b)
}

// pipelineMemBound is the most memory a pipeline of concurrency n holds in
// its buffers, whatever the input (the stream's context and its window
// buffers, as with n == 1, aside): the units (min(n, maxReadAhead)
// queued, one S1 is sending, S2's and S3's: readUnit gives S3's back
// before it takes the next), S1's input buffer, S2's context and literal
// scratch, and the two decoded blocks (S2's and S3's), each with at most
// blockSizeMax literals and blockSizeMax/3 sequences (a block with more
// goes to S3 undecoded). A stopped generation's S1, until its Read
// returns, adds its input buffer and one unit.
func pipelineMemBound(n int) int {
	const pseqSize = 8 + intSize/8
	const block = blockSizeMax + wildcopyOverlength + blockSizeMax/3*pseqSize
	const dctxSize = 160 << 10 // entropy tables and litExtraBuffer: about 100 KiB
	return (min(n, maxReadAhead)+3)*dStreamInSize + 2*dStreamInSize + dctxSize + blockSizeMax + 2*block
}

const intSize = 32 << (^uint(0) >> 63)

// startPipeline starts the pipeline of the stream (S1 and S2).
func (d *Decoder) startPipeline() {
	s := &d.s
	p := &pipeline{
		done:   make(chan struct{}),
		units:  make(chan *blockUnit, min(d.o.concurrent, maxReadAhead)),
		blocks: make(chan *blockUnit),
	}
	if s.edc == nil {
		s.edc = newDCtx()
		s.escratch = make([]byte, blockSizeMax)
	}
	f := &framer{p: p, r: s.r, dd: s.dc.ddict, dds: s.dc.ddicts}
	p.wg.Add(1)
	go f.run()
	go p.entropy(s.edc, s.escratch)
	s.pipe = p
	s.in, s.inPos, s.inEnd = nil, 0, 0
	s.dc.pipe = &pipeExec{}
}

// stopPipeline stops the stream's pipeline, if it runs: it waits for S2
// to return, not for S1 (see pipeline).
func (d *Decoder) stopPipeline() {
	s := &d.s
	p := s.pipe
	if p == nil {
		return
	}
	close(p.done)
	p.wg.Wait()
	for n := len(p.units); n > 0; n-- {
		select {
		case u := <-p.units:
			putUnit(u)
		default:
		}
	}
	if p.cur != nil {
		putUnit(p.cur)
	}
	if s.dc != nil && s.dc.pipe != nil {
		for _, b := range s.dc.pipe.blocks {
			putBlock(b)
		}
		s.dc.pipe = nil
	}
	s.pipe = nil
	s.in, s.inPos, s.inEnd = nil, 0, 0
}

// errPipelineInput is readUnit's error for input left unconsumed before a
// unit: impossible, since decompressStream makes progress (and so is not
// asked for input) whenever it has input and room for output.
var errPipelineInput = errors.New("zstd: internal error: stream input left before a pipeline unit")

// readUnit is readInput for a pipelined stream: it makes the next unit
// the input, or sets eof, or returns the io.Reader's error (S1 read it as
// readInput does). A panic of the io.Reader's Read is raised again here.
func (s *decStream) readUnit() error {
	p := s.pipe
	if s.inPos < s.inEnd {
		return errPipelineInput
	}
	if p.cur != nil {
		putUnit(p.cur)
		p.cur, s.in, s.inPos, s.inEnd = nil, nil, 0, 0
	}
	u := <-p.blocks
	if u.kind == unitEnd {
		err, panicked, pv := u.err, u.panicked, u.panicVal
		putUnit(u)
		if panicked {
			s.err = errReaderPanicked // sticky; fill stops the pipeline
			panic(pv)
		}
		if err == nil {
			s.eof = true
		}
		return err
	}
	if u.blk != nil {
		s.dc.pipe.blocks = append(s.dc.pipe.blocks, u.blk)
		u.blk = nil
	}
	p.cur, s.in = u, u.data
	s.inPos, s.inEnd = 0, len(s.in)
	return nil
}

// errReaderPanicked is the sticky error of a stream whose io.Reader
// panicked (the panic itself goes to the caller of Read or WriteTo).
var errReaderPanicked = errors.New("zstd: io.Reader panicked")

// pipeExec is the S3 state of a stream context a pipeline feeds.
type pipeExec struct {
	// blocks are S2's results for the compressed blocks of the units fed
	// to the context, in order; decompressContinue takes the first for the
	// next compressed block.
	blocks []*decodedBlock
	// synced: the context holds the entropy state (taken from S2's after
	// a block S2 did not decode), so it decodes compressed blocks itself.
	synced bool
}

// pipeBlock decodes the compressed block src of a pipelined stream into
// dst[len(dst):len(dst)+dstCap], as decompressBlockInternal (streaming).
func (d *dctx) pipeBlock(dst []byte, dstCap int, src []byte) (int, dErr) {
	p := d.pipe
	if len(p.blocks) == 0 {
		return 0, dErrGeneric // impossible: S1 cuts every block the stream reaches
	}
	b := p.blocks[0]
	p.blocks = p.blocks[:copy(p.blocks, p.blocks[1:])]
	defer putBlock(b)
	if len(b.src) != len(src) {
		return 0, dErrGeneric // impossible: S1 and the stream cut the same blocks
	}
	if b.ectx != nil {
		d.takeEntropy(b.ectx)
		p.synced = true
	}
	if b.sync {
		if !p.synced {
			return 0, dErrGeneric // impossible: the first sync block has ectx
		}
		pipeSyncBlocks.Add(1)
		return d.decompressBlockInternal(dst, dstCap, src, isStreaming)
	}
	pipeFastBlocks.Add(1)
	return d.execBlockSeqs(dst, dstCap, b)
}

// pipeFastBlocks and pipeSyncBlocks count the compressed blocks pipelined
// streams executed from S2's result and decoded themselves (test-only: a
// valid stream has no sync block).
var pipeFastBlocks, pipeSyncBlocks atomic.Int64

// takeEntropy copies e's entropy state (tables, table pointers, flags,
// repcodes) into d. ddictIsCold is d's own: the stream's context keeps it
// as the n == 1 stream does (execBlockSeqs clears it where
// decompressBlockInternal would).
func (d *dctx) takeEntropy(e *dctx) {
	d.entropy.llTable, d.entropy.ofTable, d.entropy.mlTable = e.entropy.llTable, e.entropy.ofTable, e.entropy.mlTable
	d.entropy.hufTable, d.entropy.rep = e.entropy.hufTable, e.entropy.rep
	remap := func(p, from, to []seqSymbol) []seqSymbol {
		if len(p) > 0 && &p[0] == &from[0] {
			return to
		}
		return p // a default or dictionary table
	}
	d.llTptr = remap(e.llTptr, e.entropy.llTable[:], d.entropy.llTable[:])
	d.ofTptr = remap(e.ofTptr, e.entropy.ofTable[:], d.entropy.ofTable[:])
	d.mlTptr = remap(e.mlTptr, e.entropy.mlTable[:], d.entropy.mlTable[:])
	d.hufPtr = e.hufPtr
	if e.hufPtr == &e.entropy.hufTable {
		d.hufPtr = &d.entropy.hufTable
	}
	d.litEntropy, d.fseEntropy = e.litEntropy, e.fseEntropy
}

// entropy is S2: it begins e at each frame and decodes each compressed
// block. After a block it cannot decode, it hands e (as that block found
// it) to S3 and passes the rest on undecoded.
func (p *pipeline) entropy(e *dctx, scratch []byte) {
	defer p.wg.Done()
	synced := false
	for {
		var u *blockUnit
		select {
		case u = <-p.units:
		case <-p.done:
			return
		}
		switch u.kind {
		case unitFrame:
			if !synced {
				e.decompressBeginUsingDDict(u.dd)
				e.fParams = u.zfh
			}
		case unitBlock:
			b := blockPool.Get().(*decodedBlock)
			b.src = u.data[blockHeaderSize:]
			switch {
			case synced:
				b.sync = true
			case !e.decodeBlockSeqs(b.src, scratch, b):
				b.sync, b.ectx = true, e
				synced = true
			}
			u.blk = b
		}
		end := u.kind == unitEnd // (u is S3's once sent)
		select {
		case p.blocks <- u:
		case <-p.done:
			putUnit(u)
			return
		}
		if end {
			return
		}
	}
}

// framer is S1.
type framer struct {
	p   *pipeline
	r   io.Reader
	dd  *ddict    // the stream's dictionary (dctx.ddict)
	dds *ddictSet // the stream's dictionaries by ID (dctx.ddicts)
	// buf[pos:] is input read and not yet cut.
	buf []byte
	pos int
	eof bool
	// The frame structure (C's bufferless stages, ZSTD_nextInputType).
	stage  framerStage
	bsMax  int    // the frame's blockSizeMax
	cksum  bool   // the frame has a checksum
	remain uint64 // fsSkip: content left
}

type framerStage uint8

const (
	fsFrame       framerStage = iota // a frame starts
	fsBlockHeader                    // a block (header and content)
	fsChecksum                       // the frame's checksum
	fsSkip                           // a skippable frame's content
	fsPass                           // input the stream rejects or the framer cannot cut
)

// run cuts the input into units and sends them to S2, then a unitEnd.
func (f *framer) run() {
	defer func() {
		// Go: a panic of the io.Reader's Read (the only code here that is
		// not the decoder's) is carried to S3, which raises it on the
		// caller's goroutine, where n == 1 raises it. The one recover() of
		// the decoder files (scripts/decodercheck.sh allows it here only);
		// a decoder bug panicking in cut() is raised there too, not hidden.
		if r := recover(); r != nil {
			end := getUnit(unitEnd, nil)
			end.panicked, end.panicVal = true, r
			f.send(end)
		}
	}()
	f.buf = make([]byte, 0, 2*dStreamInSize)
	for {
		for {
			u := f.cut()
			if u == nil {
				break
			}
			if !f.send(u) {
				return
			}
		}
		var err error
		if f.eof {
			// The input ends: what is left, in plain units, then the end.
			for f.pos < len(f.buf) {
				n := min(len(f.buf)-f.pos, dStreamInSize)
				u := getUnit(unitBytes, f.buf[f.pos:f.pos+n])
				f.pos += n
				if !f.send(u) {
					return
				}
			}
		} else if err = f.read(); err == nil {
			continue
		}
		end := getUnit(unitEnd, nil)
		end.err = err
		f.send(end)
		return
	}
}

// send sends u to S2; false when the pipeline stops.
func (f *framer) send(u *blockUnit) bool {
	select {
	case f.p.units <- u:
		return true
	case <-f.p.done:
		putUnit(u)
		return false
	}
}

// read reads the io.Reader once as readInput does: at least one byte, or
// eof, or an error (the bytes of a read that returns one are dropped,
// as readInput's are never decoded).
func (f *framer) read() error {
	if f.pos > 0 {
		f.buf = f.buf[:copy(f.buf, f.buf[f.pos:])]
		f.pos = 0
	}
	for range 100 {
		select {
		case <-f.p.done:
			return io.ErrNoProgress // (not delivered: the pipeline stops)
		default:
		}
		p := f.buf[len(f.buf) : len(f.buf)+dStreamInSize]
		nr, err := f.r.Read(p)
		if nr < 0 || nr > len(p) {
			return errors.New("zstd: io.Reader returned an invalid count")
		}
		if err == io.EOF {
			f.buf = f.buf[:len(f.buf)+nr]
			f.eof = true
			return nil
		}
		if err != nil {
			return err
		}
		f.buf = f.buf[:len(f.buf)+nr]
		if nr > 0 {
			return nil
		}
	}
	return io.ErrNoProgress
}

// cut returns the next unit of the input read, or nil when more input is
// needed. It follows the stream's checks of the frame structure: at
// anything decompressStream rejects there, the rest of the input goes in
// plain units (fsPass), so that S2 never decodes a block the stream does
// not reach.
func (f *framer) cut() *blockUnit {
	in := f.buf[f.pos:]
	if len(in) == 0 {
		return nil
	}
	plain := func(n int) *blockUnit {
		f.pos += n
		return getUnit(unitBytes, in[:n])
	}
	switch f.stage {
	case fsFrame:
		if len(in) < frameIDSize {
			return nil
		}
		magic := binary.LittleEndian.Uint32(in)
		if magic&magicSkippableMask == magicSkippableStart {
			if len(in) < skippableHeaderSize {
				return nil
			}
			if _, e := readSkippableFrameSize(in[:skippableHeaderSize]); e == dErrFrameParameterUnsupported {
				f.stage = fsPass // decompressStream rejects the size
			} else {
				f.stage = fsSkip
				f.remain = uint64(binary.LittleEndian.Uint32(in[frameIDSize:]))
			}
			return plain(skippableHeaderSize)
		}
		if magic != magicNumber {
			f.stage = fsPass
			return f.cut()
		}
		if len(in) < startingInputLength {
			return nil
		}
		hs, _ := frameHeaderSizeInternal(in)
		if len(in) < hs {
			return nil
		}
		var zfh frameHeader
		if need, e := getFrameHeaderAdvanced(&zfh, in[:hs]); e != 0 || need != 0 {
			f.stage = fsPass
			return f.cut()
		}
		u := plain(hs)
		u.kind = unitFrame
		u.zfh = zfh
		// C: ZSTD_DCtx_selectFrameDDict, as decompressStream does
		u.dd = f.dd
		if f.dds != nil {
			u.dd = f.dds.selectFrameDDict(zfh.dictID)
		}
		f.stage, f.bsMax, f.cksum = fsBlockHeader, int(zfh.blockSizeMax), zfh.checksumFlag
		return u
	case fsBlockHeader:
		if len(in) < blockHeaderSize {
			return nil
		}
		cSize, bp, e := getcBlockSize(in)
		if e != 0 || cSize > f.bsMax && bp.blockType != btRLE || cSize == 0 && bp.blockType == btCompressed {
			f.stage = fsPass // decompressContinue rejects the header
			return f.cut()
		}
		if len(in) < blockHeaderSize+cSize {
			return nil
		}
		u := plain(blockHeaderSize + cSize)
		if bp.blockType == btCompressed {
			u.kind = unitBlock
		}
		switch {
		case !bp.lastBlock:
		case f.cksum:
			f.stage = fsChecksum
		default:
			f.stage = fsFrame
		}
		return u
	case fsChecksum:
		if len(in) < 4 {
			return nil
		}
		f.stage = fsFrame
		return plain(4)
	case fsSkip:
		if f.remain == 0 {
			f.stage = fsFrame
			return f.cut()
		}
		n := int(min(f.remain, uint64(len(in)), dStreamInSize))
		f.remain -= uint64(n)
		return plain(n)
	}
	return plain(min(len(in), dStreamInSize)) // fsPass
}
