package lz4

// FrameCompressor is a reusable frame compression context with C's API (C:
// LZ4F_cctx with LZ4F_compressBegin, LZ4F_compressUpdate, LZ4F_flush and
// LZ4F_compressEnd). Reusing it across frames keeps its buffers and tables,
// as C does; the output is the same as with a fresh context. Each output
// buffer dst must hold CompressBoundFrame of the input given (maxFHSize
// bytes for Begin). The zero value is ready to use.
type FrameCompressor struct {
	c frameCCtx
}

// Begin starts a frame and writes its header into dst (C:
// LZ4F_compressBegin). p may be nil.
func (f *FrameCompressor) Begin(dst []byte, p *Preferences) (int, error) {
	return f.c.begin(dst, nil, nil, p)
}

// BeginUsingDict is Begin with dict as history (C:
// LZ4F_compressBegin_usingDict). dict must stay unchanged until End.
func (f *FrameCompressor) BeginUsingDict(dst, dict []byte, p *Preferences) (int, error) {
	if dict == nil {
		dict = []byte{}
	}
	return f.c.begin(dst, dict, nil, p)
}

// BeginUsingCDict is Begin with a digested dictionary (C:
// LZ4F_compressBegin_usingCDict).
func (f *FrameCompressor) BeginUsingCDict(dst []byte, cdict *CDict, p *Preferences) (int, error) {
	return f.c.begin(dst, nil, cdict, p)
}

func (f *FrameCompressor) checkDst(dst []byte, n int) error {
	if len(dst) < CompressBoundFrame(n, &f.c.prefs) {
		return ErrDstTooSmallFrame
	}
	return nil
}

// Update compresses src, buffering what does not fill a block (C:
// LZ4F_compressUpdate, unstable input: src may change after the call).
func (f *FrameCompressor) Update(dst, src []byte) (int, error) {
	if err := f.checkDst(dst, len(src)); err != nil {
		return 0, err
	}
	return f.c.update(dst, src, false, compressedMode)
}

// UpdateStable is Update for input that stays unchanged until the next
// Flush or End (C: LZ4F_compressUpdate with stableSrc =
// LZ4F_srcStableUntilFlush): linked blocks reference it in place instead of
// a copy, and the jobs of a frame with NbWorkers read it in place. With
// linked blocks and no NbWorkers the frame can differ from Update's (C's
// does too).
func (f *FrameCompressor) UpdateStable(dst, src []byte) (int, error) {
	if err := f.checkDst(dst, len(src)); err != nil {
		return 0, err
	}
	return f.c.update(dst, src, true, compressedMode)
}

// UpdateUncompressed stores src as uncompressed blocks (C:
// LZ4F_compressUpdate with the uncompressed option).
func (f *FrameCompressor) UpdateUncompressed(dst, src []byte) (int, error) {
	if err := f.checkDst(dst, len(src)); err != nil {
		return 0, err
	}
	return f.c.update(dst, src, false, uncompressedMode)
}

// Flush compresses the buffered input (C: LZ4F_flush).
func (f *FrameCompressor) Flush(dst []byte) (int, error) {
	if err := f.checkDst(dst, 0); err != nil {
		return 0, err
	}
	return f.c.flush(dst)
}

// End flushes and ends the frame (C: LZ4F_compressEnd).
func (f *FrameCompressor) End(dst []byte) (int, error) {
	if err := f.checkDst(dst, 0); err != nil {
		return 0, err
	}
	return f.c.end(dst)
}

// CompressFrame is the package's CompressFrame on this context (C:
// LZ4F_compressFrame_usingCDict with a NULL dictionary).
func (f *FrameCompressor) CompressFrame(dst, src []byte, p *Preferences) (int, error) {
	return f.c.compressFrame(dst, src, nil, nil, p)
}
