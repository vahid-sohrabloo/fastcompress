package zstd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"runtime"
	"strings"
)

const (
	// MinWindowSize is the minimum Window Size, which is 1 KB.
	MinWindowSize = 1 << 10

	// MaxWindowSize is the maximum encoder window size
	// and the default decoder maximum window size.
	MaxWindowSize = 1 << 29
)

// EOption is an option for creating a encoder.
//
// Mapping to C libzstd v1.5.7 (EncodeAll is ZSTD_compress2 with
// pledgedSrcSize = len(src) and these parameters; the Writer is
// ZSTD_compressStream2 with them, see Encoder):
//
//	WithEncoderLevel             ZSTD_c_compressionLevel 1/3/7/11
//	WithEncoderCRC               ZSTD_c_checksumFlag (default on)
//	WithWindowSize               ZSTD_c_windowLog = log2(n)
//	WithNoEntropyCompression     ZSTD_c_literalCompressionMode = ZSTD_ps_disable
//	WithAllLitEntropyCompression ZSTD_c_literalCompressionMode = ZSTD_ps_enable
//	                             (false: ZSTD_ps_auto, the level's default)
//	WithEncoderConcurrency       none; each call is single-threaded (nbWorkers=0),
//	                             the value caps concurrent EncodeAll calls
//	WithConcurrentBlocks         none; accepted, encoding is single-threaded
//	WithEncoderDictDelete        removes the dictionary (no C call)
//	WithLowerEncoderMem          none; accepted, no effect on output
//	WithHugePages                none (not a klauspost option); speed only
//	WithSingleSegment            none; the frame header follows C's automatic
//	                             choice (single segment iff window >= len(src))
//	WithZeroFrames               none; decides whether empty input (or a
//	                             stream with nothing written) emits C's
//	                             empty frame or nothing
//	WithEncoderPadding           none; a skippable frame after C's frame
//	                             (after Close's frame for the Writer)
//	WithEncoderDict              ZSTD_CCtx_loadDictionary_advanced(dict,
//	                             ZSTD_dlm_byCopy, ZSTD_dct_fullDict)
//	WithEncoderDictRaw           ZSTD_CCtx_loadDictionary_advanced(content,
//	                             ZSTD_dlm_byCopy, ZSTD_dct_rawContent), but
//	                             the frame header carries the given ID
type EOption func(*encoderOptions) error

// encoderOptions holds the options an Encoder was created with.
type encoderOptions struct {
	level        EncoderLevel
	checksum     bool
	windowLog    uint32 // 0 = level default
	maxEncodeAll int    // max concurrent EncodeAll calls (contexts held at once)
	concBlocks   bool   // stored only (WithConcurrentBlocks), to reject a change in ResetWithOptions
	zeroFrames   bool
	padMultiple  int    // 0 or 1 = no padding
	litDisable   bool   // WithNoEntropyCompression: C's ZSTD_ps_disable
	litEnable    bool   // WithAllLitEntropyCompression: C's ZSTD_ps_enable
	lowerMem     bool   // stored only, to reject a change in ResetWithOptions
	hugePages    bool   // WithHugePages
	dict         []byte // dictionary (private copy); nil = none
	dictRaw      bool   // dict is raw content (WithEncoderDictRaw)
	dictID       uint32 // WithEncoderDictRaw: the ID written into frame headers
}

func (o *encoderOptions) defaults() {
	*o = encoderOptions{
		level:        SpeedDefault,
		checksum:     true,
		maxEncodeAll: runtime.GOMAXPROCS(0),
	}
}

// literalCompressionMode resolves WithNoEntropyCompression (which wins) and
// WithAllLitEntropyCompression to C's ZSTD_c_literalCompressionMode.
func (o *encoderOptions) literalCompressionMode() paramSwitch {
	switch {
	case o.litDisable:
		return psDisable
	case o.litEnable:
		return psEnable
	}
	return psAuto
}

// WithEncoderCRC will add CRC value to output.
// Output will be 4 bytes larger.
// Can be changed with ResetWithOptions.
//
// C: ZSTD_c_checksumFlag. The default is on.
func WithEncoderCRC(b bool) EOption {
	return func(o *encoderOptions) error { o.checksum = b; return nil }
}

// WithEncoderConcurrency will set the concurrency,
// meaning the maximum number of encoders to run concurrently.
// The value supplied must be at least 0.
// When a value of 0 is provided GOMAXPROCS will be used.
// For streams, setting a value of 1 will disable async compression.
// By default this will be set to GOMAXPROCS.
// Cannot be changed with ResetWithOptions.
//
// Each EncodeAll call holds one compression context and runs on its
// caller's goroutine (C: ZSTD_c_nbWorkers = 0); at most n calls hold one at
// once, and further calls wait. This bounds the memory concurrent EncodeAll
// calls use. The streaming Writer's single context is not counted, and
// streams are never async. The value never changes the output.
func WithEncoderConcurrency(n int) EOption {
	return func(o *encoderOptions) error {
		if n < 0 {
			return fmt.Errorf("zstd: concurrency must be at least 0, got %d", n)
		}
		if n == 0 {
			n = runtime.GOMAXPROCS(0)
		}
		o.maxEncodeAll = n
		return nil
	}
}

// WithConcurrentBlocks enables job-based parallel compression for streams.
// When enabled and concurrent > 1, input is split into large sections (jobs)
// that are compressed simultaneously by multiple goroutines.
// Cannot be changed with ResetWithOptions.
//
// C: none (nbWorkers = 0). The value is stored but encoding is always
// single-threaded, so it never changes the output.
func WithConcurrentBlocks(b bool) EOption {
	return func(o *encoderOptions) error { o.concBlocks = b; return nil }
}

// WithWindowSize will set the maximum allowed back-reference distance.
// The value must be a power of two between MinWindowSize and MaxWindowSize.
// A larger value will enable better compression but allocate more memory and,
// for above-default values, take considerably longer.
// The default value is determined by the compression level and max 8MB.
// Cannot be changed with ResetWithOptions.
//
// C: ZSTD_c_windowLog = log2(n). Like C, the window is still reduced to fit
// a smaller input.
func WithWindowSize(n int) EOption {
	return func(o *encoderOptions) error {
		switch {
		case n < MinWindowSize:
			return fmt.Errorf("zstd: window size must be at least %d, got %d", MinWindowSize, n)
		case n > MaxWindowSize:
			return fmt.Errorf("zstd: window size must be at most %d, got %d", MaxWindowSize, n)
		case n&(n-1) != 0:
			return fmt.Errorf("zstd: window size must be a power of two, got %d", n)
		}
		o.windowLog = uint32(bits.Len(uint(n)) - 1)
		return nil
	}
}

// WithEncoderPadding will add padding to all output so the size will be a multiple of n.
// This can be used to obfuscate the exact output size or make blocks of a certain size.
// The contents will be a skippable frame, so it will be invisible by the decoder.
// n must be > 0 and <= 1GB, 1<<30 bytes.
// The padded area will be filled with data from crypto/rand.Reader.
// If `EncodeAll` is used with data already in the destination, the total size will be multiple of this.
// Can be changed with ResetWithOptions.
//
// C: no equivalent; the skippable frame (RFC 8878 section 3.1.2) follows
// C's frame.
func WithEncoderPadding(n int) EOption {
	return func(o *encoderOptions) error {
		if n <= 0 {
			return fmt.Errorf("zstd: padding must be at least 1, got %d", n)
		}
		if n > 1<<30 {
			return fmt.Errorf("zstd: padding must be at most 1GB (1<<30 bytes), got %d", n)
		}
		o.padMultiple = n
		return nil
	}
}

// EncoderLevel predefines encoder compression levels.
// Only use the constants made available, since the actual mapping
// of these values are very likely to change and your compression could change
// unpredictably when upgrading the library.
type EncoderLevel int

const (
	levelUnset EncoderLevel = iota

	// SpeedFastest will choose the fastest reasonable compression.
	// This is roughly equivalent to the fastest Zstandard mode.
	// C level 1.
	SpeedFastest

	// SpeedDefault is the default "pretty fast" compression option.
	// This is roughly equivalent to the default Zstandard mode (level 3).
	// C level 3.
	SpeedDefault

	// SpeedBetterCompression will yield better compression than the default.
	// C level 7.
	SpeedBetterCompression

	// SpeedBestCompression will choose the best available compression option.
	// C level 11.
	SpeedBestCompression
)

// zstdLevels maps an EncoderLevel to its C compression level.
var zstdLevels = [...]int{
	SpeedFastest:           1,
	SpeedDefault:           3,
	SpeedBetterCompression: 7,
	SpeedBestCompression:   11,
}

// EncoderLevelFromString will convert a string representation of an encoding level back
// to a compression level. The compare is not case sensitive.
// If the string wasn't recognized, (false, SpeedDefault) will be returned.
func EncoderLevelFromString(s string) (bool, EncoderLevel) {
	for l := SpeedFastest; l <= SpeedBestCompression; l++ {
		if strings.EqualFold(s, l.String()) {
			return true, l
		}
	}
	return false, SpeedDefault
}

// EncoderLevelFromZstd will return an encoder level that closest matches the compression
// ratio of a specific zstd compression level.
// Many input values will provide the same compression level.
//
// A level maps to the highest EncoderLevel whose C level (1, 3, 7, 11) does
// not exceed it; levels below 1 map to SpeedFastest.
func EncoderLevelFromZstd(level int) EncoderLevel {
	l := SpeedFastest
	for l < SpeedBestCompression && level >= zstdLevels[l+1] {
		l++
	}
	return l
}

// String provides a string representation of the compression level.
//
// The names "fastest", "default", "better" and "best" (and "invalid" for
// other values) are klauspost/compress's observable behaviour, which
// EncoderLevelFromString must accept for drop-in compatibility; the user
// ruled them part of the API (see docs/superpowers/phase2-api-decisions.md,
// "Phase 1 API notes"). C has no level names.
func (e EncoderLevel) String() string {
	switch e {
	case SpeedFastest:
		return "fastest"
	case SpeedDefault:
		return "default"
	case SpeedBetterCompression:
		return "better"
	case SpeedBestCompression:
		return "best"
	default:
		return "invalid"
	}
}

// WithEncoderLevel specifies a predefined compression level.
// Cannot be changed with ResetWithOptions.
//
// C: ZSTD_c_compressionLevel, mapped SpeedFastest=1, SpeedDefault=3,
// SpeedBetterCompression=7, SpeedBestCompression=11.
func WithEncoderLevel(l EncoderLevel) EOption {
	return func(o *encoderOptions) error {
		if l < SpeedFastest || l > SpeedBestCompression {
			return fmt.Errorf("zstd: unknown encoder level %d", int(l))
		}
		o.level = l
		return nil
	}
}

// WithZeroFrames will encode 0 length input as full frames.
// This can be needed for compatibility with zstandard usage,
// but is not needed for this package.
// Can be changed with ResetWithOptions.
//
// C: no equivalent; the frame emitted for empty input is C's.
func WithZeroFrames(b bool) EOption {
	return func(o *encoderOptions) error { o.zeroFrames = b; return nil }
}

// WithAllLitEntropyCompression will apply entropy compression if no matches are found.
// Disabling this will skip incompressible data faster, but in cases with no matches but
// skewed character distribution compression is lost.
// Default value depends on the compression level selected.
// Can be changed with ResetWithOptions.
//
// C: true is ZSTD_c_literalCompressionMode = ZSTD_ps_enable; false restores
// ZSTD_ps_auto (the level's default). WithNoEntropyCompression(true) wins.
func WithAllLitEntropyCompression(b bool) EOption {
	return func(o *encoderOptions) error { o.litEnable = b; return nil }
}

// WithNoEntropyCompression will always skip entropy compression of literals.
// This can be useful if content has matches, but unlikely to benefit from entropy
// compression. Usually the slight speed improvement is not worth enabling this.
// Can be changed with ResetWithOptions.
//
// C: ZSTD_c_literalCompressionMode = ZSTD_ps_disable (sequences are still
// entropy coded, as in C).
func WithNoEntropyCompression(b bool) EOption {
	return func(o *encoderOptions) error { o.litDisable = b; return nil }
}

// WithSingleSegment will set the "single segment" flag when EncodeAll is used.
// If this flag is set, data must be regenerated within a single continuous memory segment.
// In this case, Window_Descriptor byte is skipped, but Frame_Content_Size is necessarily present.
// As a consequence, the decoder must allocate a memory segment of size equal or larger than size of your content.
// In order to preserve the decoder from unreasonable memory requirements,
// a decoder is allowed to reject a compressed frame which requests a memory size beyond decoder's authorized range.
// For broader compatibility, decoders are recommended to support memory sizes of at least 8 MB.
// This is only a recommendation, each decoder is free to support higher or lower limits, depending on local limitations.
// If this is not specified, block encodes will automatically choose this based on the input size and the window size.
// This setting has no effect on streamed encodes.
// Can be changed with ResetWithOptions.
//
// C has no parameter for it: ZSTD_compress2 sets the flag exactly when the
// window covers the whole input. The option is accepted and ignored: the
// frame header always follows C's automatic choice, so output stays
// byte-identical to C.
func WithSingleSegment(b bool) EOption {
	return func(o *encoderOptions) error { return nil }
}

// WithLowerEncoderMem will trade in some memory cases trade less memory usage for
// slower encoding speed.
// This will not change the window size which is the primary function for reducing
// memory usage. See WithWindowSize.
// Cannot be changed with ResetWithOptions.
//
// C: no equivalent. Accepted and ignored; it would not change output.
func WithLowerEncoderMem(b bool) EOption {
	return func(o *encoderOptions) error { o.lowerMem = b; return nil }
}

// WithHugePages backs the encoder's large match tables (the hash, chain or
// binary-tree, and row tag tables of 1 MiB or more, which
// SpeedBetterCompression and SpeedBestCompression use for large inputs) with transparent huge
// pages: each such table gets its own 2 MiB-aligned anonymous mapping advised
// with MADV_HUGEPAGE. This cuts the TLB misses of their random accesses.
// The default is off.
//
// It is speed only: the output is identical with it on or off. It is Linux
// only, and a no-op elsewhere; it needs the kernel's transparent huge pages
// in "always" or "madvise" mode (/sys/kernel/mm/transparent_hugepage/enabled).
// It may raise resident memory, since each table is rounded up to whole
// 2 MiB pages. The mappings are outside the Go heap: the GC does not count
// them (nor does GOMEMLIMIT), and they are released only when the encoder's
// pooled contexts are garbage collected, after a GC cycle. Reuse encoders
// rather than creating one per stream. Cannot be changed with
// ResetWithOptions.
//
// C: no equivalent; not a klauspost/compress option.
func WithHugePages(b bool) EOption {
	return func(o *encoderOptions) error { o.hugePages = b; return nil }
}

// WithEncoderDict allows to register a dictionary that will be used for the encode.
//
// The slice dict must be in the [dictionary format] produced by
// "zstd --train" from the Zstandard reference implementation.
//
// The encoder *may* choose to use no dictionary instead for certain payloads.
// Can be changed with ResetWithOptions.
//
// C: ZSTD_CCtx_loadDictionary_advanced(dict, ZSTD_dlm_byCopy,
// ZSTD_dct_fullDict): C digests the dictionary into a CDict with the
// context's parameters at the first frame and then starts every frame from
// it (attaching or copying its tables). The Encoder makes that CDict once
// and shares it between its contexts. dict is copied. A dict that is not a
// valid zstd dictionary (no magic number, corrupted entropy tables, invalid
// repcodes) returns an error, as C's ZSTD_dictionary_wrong and
// ZSTD_dictionary_corrupted. Unlike C, a repcode beyond the content a CDict
// keeps for some strategy (the last 16 MiB - 2 bytes for the fastest
// levels) is rejected too: C's behaviour with it is undefined.
//
// [dictionary format]: https://github.com/facebook/zstd/blob/dev/doc/zstd_compression_format.md#dictionary-format
func WithEncoderDict(dict []byte) EOption {
	return func(o *encoderOptions) error {
		if len(dict) < 8 || binary.LittleEndian.Uint32(dict) != dictMagic {
			return errDictionaryWrong
		}
		var bs compressedBlockState
		eSize, err := loadCEntropy(&bs, dict)
		if err != nil {
			return err
		}
		if err := checkDictReps(bs.rep, len(dict)-eSize); err != nil {
			return err
		}
		o.dict, o.dictRaw, o.dictID = bytes.Clone(dict), false, 0
		return nil
	}
}

// WithEncoderDictRaw registers a dictionary that may be used by the encoder.
//
// The slice content may contain arbitrary data. It will be used as an initial
// history.
// Can be changed with ResetWithOptions.
//
// C: ZSTD_CCtx_loadDictionary_advanced(content, ZSTD_dlm_byCopy,
// ZSTD_dct_rawContent), see WithEncoderDict. C gives raw content the
// dictionary ID 0, so its frame headers carry no ID; here they carry id
// (none if id is 0), as the option requires. That changes only the frame
// header: everything after the ID field is C's output. content is copied;
// empty content returns an error (C would compress without a dictionary,
// leaving no history to refer to).
func WithEncoderDictRaw(id uint32, content []byte) EOption {
	return func(o *encoderOptions) error {
		if len(content) == 0 {
			return errors.New("zstd: empty raw dictionary content")
		}
		o.dict, o.dictRaw, o.dictID = bytes.Clone(content), true, id
		return nil
	}
}

// WithEncoderDictDelete clears the dictionary, so no dictionary will be used.
// Should be used with ResetWithOptions.
//
// C: no call; the next frames are produced as without a dictionary.
func WithEncoderDictDelete() EOption {
	return func(o *encoderOptions) error {
		o.dict, o.dictRaw, o.dictID = nil, false, 0
		return nil
	}
}
