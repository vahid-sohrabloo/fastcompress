package zstd

import (
	"errors"
	"io"
	"strconv"
)

var (
	// ErrSnappyCorrupt reports that the input is invalid.
	ErrSnappyCorrupt = errors.New("snappy: corrupt input")
	// ErrSnappyTooLarge reports that the uncompressed length is too large.
	ErrSnappyTooLarge = errors.New("snappy: decoded block is too large")
	// ErrSnappyUnsupported reports that the input isn't supported.
	ErrSnappyUnsupported = errors.New("snappy: unsupported input")
)

var (
	// ErrReservedBlockType is returned when a reserved block type is found.
	// Typically this indicates wrong or corrupted input.
	ErrReservedBlockType = errors.New("invalid input: reserved block type encountered")

	// ErrCompressedSizeTooBig is returned when a block is bigger than allowed.
	// Typically this indicates wrong or corrupted input.
	ErrCompressedSizeTooBig = errors.New("invalid input: compressed size too big")

	// ErrBlockTooSmall is returned when a block is too small to be decoded.
	// Typically returned on invalid input.
	ErrBlockTooSmall = errors.New("block too small")

	// ErrUnexpectedBlockSize is returned when a block has unexpected size.
	// Typically returned on invalid input.
	ErrUnexpectedBlockSize = errors.New("unexpected block size")

	// ErrMagicMismatch is returned when a "magic" number isn't what is expected.
	// Typically this indicates wrong or corrupted input.
	ErrMagicMismatch = errors.New("invalid input: magic number mismatch")

	// ErrWindowSizeExceeded is returned when a reference exceeds the valid window size.
	// Typically this indicates wrong or corrupted input.
	ErrWindowSizeExceeded = errors.New("window size exceeded")

	// ErrWindowSizeTooSmall is returned when no window size is specified.
	// Typically this indicates wrong or corrupted input.
	ErrWindowSizeTooSmall = errors.New("invalid input: window size was too small")

	// ErrDecoderSizeExceeded is returned if decompressed size exceeds the configured limit.
	ErrDecoderSizeExceeded = errors.New("decompressed size exceeds configured limit")

	// ErrUnknownDictionary is returned if the dictionary ID is unknown.
	ErrUnknownDictionary = errors.New("unknown dictionary")

	// ErrFrameSizeExceeded is returned if the stated frame size is exceeded.
	// This is only returned if SingleSegment is specified on the frame.
	ErrFrameSizeExceeded = errors.New("frame size exceeded")

	// ErrFrameSizeMismatch is returned if the stated frame size does not match the expected size.
	// This is only returned if SingleSegment is specified on the frame.
	ErrFrameSizeMismatch = errors.New("frame size does not match size on stream")

	// ErrCRCMismatch is returned if CRC mismatches.
	ErrCRCMismatch = errors.New("CRC check failed")

	// ErrDecoderClosed will be returned if the Decoder was used after
	// Close has been called.
	ErrDecoderClosed = errors.New("decoder used after Close")

	// ErrEncoderClosed will be returned if the Encoder was used after
	// Close has been called.
	ErrEncoderClosed = errors.New("encoder used after Close")

	// ErrDecoderNilInput is returned when a nil Reader was provided
	// and an operation other than Reset/DecodeAll/Close was attempted.
	ErrDecoderNilInput = errors.New("nil input provided as reader")
)

// dErr is a decoder error: a C ZSTD_ErrorCode (the same value), or a
// refinement of one (values from 128) where the error-mapping table
// (docs/superpowers/phase2-api-decisions.md) gives one C code different
// sentinels depending on where C raises it. 0 is no error.
type dErr uint8

const (
	dErrGeneric                      dErr = 1  // C: ZSTD_error_GENERIC
	dErrPrefixUnknown                dErr = 10 // C: ZSTD_error_prefix_unknown
	dErrFrameParameterUnsupported    dErr = 14 // C: ZSTD_error_frameParameter_unsupported
	dErrFrameParameterWindowTooLarge dErr = 16 // C: ZSTD_error_frameParameter_windowTooLarge
	dErrCorruptionDetected           dErr = 20 // C: ZSTD_error_corruption_detected
	dErrChecksumWrong                dErr = 22 // C: ZSTD_error_checksum_wrong
	dErrLiteralsHeaderWrong          dErr = 24 // C: ZSTD_error_literals_headerWrong
	dErrDictionaryCorrupted          dErr = 30 // C: ZSTD_error_dictionary_corrupted
	dErrDictionaryWrong              dErr = 32 // C: ZSTD_error_dictionary_wrong
	dErrDstSizeTooSmall              dErr = 70 // C: ZSTD_error_dstSize_tooSmall
	dErrSrcSizeWrong                 dErr = 72 // C: ZSTD_error_srcSize_wrong
	dErrNoForwardProgressDestFull    dErr = 80 // C: ZSTD_error_noForwardProgress_destFull
	dErrNoForwardProgressInputEmpty  dErr = 82 // C: ZSTD_error_noForwardProgress_inputEmpty

	// dErrReservedBlockType is corruption_detected from ZSTD_getcBlockSize
	// (block type 3).
	dErrReservedBlockType dErr = 128
	// dErrBlockTooSmall is corruption_detected from a literals or
	// sequences header that runs past the end of its block
	// (ZSTD_decodeLiteralsBlock's srcSize checks, ZSTD_buildSeqTable's RLE
	// byte).
	dErrBlockTooSmall dErr = 129
	// dErrSeqHeaderTooSmall is srcSize_wrong from ZSTD_decodeSeqHeaders: the
	// sequences header runs past the end of its block. Not truncated
	// input, so not io.ErrUnexpectedEOF.
	dErrSeqHeaderTooSmall dErr = 130
	// dErrLitSizeTooLarge is corruption_detected from
	// ZSTD_decodeLiteralsBlock: litSize > ZSTD_blockSizeMax(dctx).
	dErrLitSizeTooLarge dErr = 131
	// dErrLitDstSizeTooSmall is dstSize_tooSmall from
	// ZSTD_decodeLiteralsBlock: the literals do not fit
	// MIN(blockSizeMax, dstCapacity) (or dst is NULL).
	dErrLitDstSizeTooSmall dErr = 132
	// dErrBlockDstSizeTooSmall is dstSize_tooSmall from sequence execution
	// (ZSTD_execSequence*, the ZSTD_decompressSequences* last literals and
	// split-buffer checks), ZSTD_decompressBlock_internal (sequences with
	// no output space), ZSTD_copyRawBlock and ZSTD_setRleBlock: the block
	// regenerates more than its output space.
	dErrBlockDstSizeTooSmall dErr = 133
	// dErrOffsetBeyondHistory is corruption_detected from ZSTD_execSequence*:
	// a match offset beyond the prefix and the extDict (dictionary).
	dErrOffsetBeyondHistory dErr = 134
	// dErrCompressedSizeTooBig is srcSize_wrong from
	// ZSTD_decompressBlock_internal: compressed block size >
	// ZSTD_blockSizeMax(dctx).
	dErrCompressedSizeTooBig dErr = 135
	// dErrFrameSizeExceeded is dstSize_tooSmall from a block of a frame
	// whose output space is its frame content size (decompressMultiFrame):
	// the frame regenerates more than its header says.
	dErrFrameSizeExceeded dErr = 136
	// dErrFrameSizeMismatch is corruption_detected from
	// ZSTD_decompressFrame: the frame ends with an output size other than
	// its frame content size.
	dErrFrameSizeMismatch dErr = 137
	// dErrDecoderSizeExceeded has no C counterpart: dstSize_tooSmall from a
	// block of a frame whose output space was cut to the decoder's memory
	// limit (dctx.maxMemory), or a frame content size above that limit.
	dErrDecoderSizeExceeded dErr = 138
	// dErrDecodedBlockTooLarge is corruption_detected from
	// ZSTD_decompressContinue ("Decompressed Block Size Exceeds Maximum"):
	// a block regenerates more than blockSizeMax. Streaming C's check, which
	// decompressFrame makes too.
	dErrDecodedBlockTooLarge dErr = 139
	// dErrCBlockSizeTooLarge is corruption_detected from
	// ZSTD_decompressContinue ("Block Size Exceeds Maximum") for a
	// compressed block: its size in the block header is above
	// blockSizeMax. One-shot decoding rejects the same block with
	// dErrCompressedSizeTooBig, whose sentinel it shares. (A raw or RLE
	// block failing that check is dErrDecodedBlockTooLarge.)
	dErrCBlockSizeTooLarge dErr = 140
)

// cCode returns the C ZSTD_ErrorCode e stands for.
func (e dErr) cCode() int {
	switch e {
	case dErrReservedBlockType, dErrBlockTooSmall, dErrLitSizeTooLarge, dErrOffsetBeyondHistory,
		dErrFrameSizeMismatch, dErrDecodedBlockTooLarge, dErrCBlockSizeTooLarge:
		return int(dErrCorruptionDetected)
	case dErrSeqHeaderTooSmall, dErrCompressedSizeTooBig:
		return int(dErrSrcSizeWrong)
	case dErrLitDstSizeTooSmall, dErrBlockDstSizeTooSmall, dErrFrameSizeExceeded, dErrDecoderSizeExceeded:
		return int(dErrDstSizeTooSmall)
	}
	return int(e)
}

// cErrorNames are C's ZSTD_getErrorName texts. C: common/error_private.c
var cErrorNames = map[int]string{
	1:  "Error (generic)",
	10: "Unknown frame descriptor",
	14: "Unsupported frame parameter",
	16: "Frame requires too much memory for decoding",
	20: "Data corruption detected",
	22: "Restored data doesn't match checksum",
	24: "Header of Literals' block doesn't respect format specification",
	30: "Dictionary is corrupted",
	32: "Dictionary mismatch",
	34: "Cannot create Dictionary from provided samples",
	42: "Parameter is out of bound",
	64: "Allocation error : not enough memory",
	70: "Destination buffer is too small",
	72: "Src size is incorrect",
	80: "Operation made no progress over multiple calls, due to output buffer being full",
	82: "Operation made no progress over multiple calls, due to input being empty",
}

// corruptError is a decoder error that matches no sentinel.
type corruptError struct {
	code  dErr
	where string
}

func (e *corruptError) Error() string {
	name, ok := cErrorNames[e.code.cCode()]
	if !ok {
		name = "error code " + strconv.Itoa(e.code.cCode())
	}
	return "zstd: " + e.where + ": " + name
}

// toError returns the error the mapping table gives e: an exported
// sentinel, io.ErrUnexpectedEOF, or a *corruptError naming where (the
// operation that failed). e must not be 0.
func (e dErr) toError(where string) error {
	switch e {
	case dErrPrefixUnknown:
		return ErrMagicMismatch
	case dErrFrameParameterWindowTooLarge:
		return ErrWindowSizeExceeded
	case dErrReservedBlockType:
		return ErrReservedBlockType
	case dErrBlockTooSmall, dErrSeqHeaderTooSmall:
		return ErrBlockTooSmall
	case dErrLitSizeTooLarge, dErrLitDstSizeTooSmall, dErrBlockDstSizeTooSmall, dErrDecodedBlockTooLarge:
		return ErrUnexpectedBlockSize
	case dErrOffsetBeyondHistory:
		return ErrWindowSizeTooSmall
	case dErrCompressedSizeTooBig, dErrCBlockSizeTooLarge:
		return ErrCompressedSizeTooBig
	case dErrFrameSizeExceeded:
		return ErrFrameSizeExceeded
	case dErrFrameSizeMismatch:
		return ErrFrameSizeMismatch
	case dErrDecoderSizeExceeded:
		return ErrDecoderSizeExceeded
	case dErrChecksumWrong:
		return ErrCRCMismatch
	case dErrDictionaryWrong:
		return ErrUnknownDictionary
	case dErrSrcSizeWrong:
		return io.ErrUnexpectedEOF
	}
	return &corruptError{code: e, where: where}
}
