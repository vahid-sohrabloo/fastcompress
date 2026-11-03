package zstd

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
)

// DOption is an option for creating a decoder.
//
// Mapping to C libzstd v1.5.7 (DecodeAll is ZSTD_decompressDCtx, i.e.
// ZSTD_decompressMultiFrame; the stream is ZSTD_decompressStream; both with
// these parameters, see Decoder):
//
//	IgnoreChecksum          ZSTD_d_forceIgnoreChecksum
//	WithDecoderMaxWindow    ZSTD_d_windowLogMax at byte granularity:
//	                        min(size, max memory), for DecodeAll too
//	WithDecoderMaxMemory    none; caps DecodeAll's output and the window
//	WithDecodeAllCapLimit   none; caps DecodeAll's output at cap(dst)-len(dst)
//	WithDecoderDicts        ZSTD_createDDict (ZSTD_dct_auto, but content
//	                        without the dictionary magic number is
//	                        rejected), referenced with
//	                        ZSTD_d_refMultipleDDicts
//	WithDecoderDictRaw      the same with ZSTD_dct_rawContent; the DDict
//	                        gets the given ID (C gives raw content ID 0)
//	WithDecoderDictDelete   removes dictionaries (no C call)
//	WithDecoderConcurrency  none; caps concurrent DecodeAll calls (one
//	                        DCtx each); streams are synchronous
//	WithDecodeBuffersBelow  none; such readers are decoded with DecodeAll
//	WithDecoderLowmem       none; stream buffers over 1 MiB are freed at
//	                        frame ends
type DOption func(*decoderOptions) error

// decoderOptions holds the options a Decoder was created with.
type decoderOptions struct {
	concurrent     int    // DecodeAll contexts
	maxMemory      uint64 // WithDecoderMaxMemory, as given (see Decoder.memLimit)
	maxWindow      uint64
	capLimit       bool
	ignoreChecksum bool
	lowMem         bool
	buffersBelow   int
	// dicts are the registered dictionaries by ID. A ddict is never
	// written after createDDict; ResetWithOptions copies the map before
	// changing it.
	dicts map[uint32]*ddict
}

func (o *decoderOptions) defaults() {
	*o = decoderOptions{
		concurrent:   min(4, runtime.GOMAXPROCS(0)),
		maxMemory:    defaultMaxMemory,
		maxWindow:    defaultMaxWindowSize,
		buffersBelow: 128 << 10,
	}
}

// addDict registers dd under its ID, replacing one with the same ID.
func (o *decoderOptions) addDict(dd *ddict) {
	if o.dicts == nil {
		o.dicts = map[uint32]*ddict{}
	}
	o.dicts[dd.dictID] = dd
}

// ddictSet returns the set of o's dictionaries.
// C: ZSTD_DCtx_refDDict with ZSTD_d_refMultipleDDicts, once per DDict
func (o *decoderOptions) ddictSet() *ddictSet {
	s := newDDictSet()
	for _, dd := range o.dicts {
		s.add(dd)
	}
	return s
}

// maxWindowSizeSpec is the largest window the frame format can describe:
// window descriptor exponent 31 and mantissa 7 (C: ZSTD_WINDOWLOG_MAX_64
// is 31, below it; WithDecoderMaxWindow's doc text sets this bound).
const maxWindowSizeSpec = 1<<41 + 7<<38

// IgnoreChecksum allows to forcibly ignore checksum checking.
// Can be changed with ResetWithOptions.
//
// C: ZSTD_d_forceIgnoreChecksum.
func IgnoreChecksum(b bool) DOption {
	return func(o *decoderOptions) error { o.ignoreChecksum = b; return nil }
}

// WithDecodeAllCapLimit will limit DecodeAll to decoding cap(dst)-len(dst) bytes,
// or any size set in WithDecoderMaxMemory.
// This can be used to limit decoding to a specific maximum output size.
// Disabled by default.
// Can be changed with ResetWithOptions.
//
// The limit is the smaller of the two; exceeding it returns
// ErrDecoderSizeExceeded, and the output never leaves dst's capacity.
func WithDecodeAllCapLimit(b bool) DOption {
	return func(o *decoderOptions) error { o.capLimit = b; return nil }
}

// WithDecodeBuffersBelow will fully decode readers that have a
// `Bytes() []byte` and `Len() int` interface similar to bytes.Buffer.
// This typically uses less allocations but will have the full decompressed object in memory.
// Note that DecodeAllCapLimit will disable this, as well as giving a size of 0 or less.
// Default is 128KiB.
// Cannot be changed with ResetWithOptions.
//
// Reset decodes such a reader (when Len() < size) with DecodeAll's frame
// decoding; Read and WriteTo serve the result, then its error. The reader
// is not read. When the output would exceed WithDecoderMaxMemory (DecodeAll's
// limit, not a stream's), the reader is streamed instead.
func WithDecodeBuffersBelow(size int) DOption {
	return func(o *decoderOptions) error { o.buffersBelow = size; return nil }
}

// WithDecoderConcurrency sets the number of created decoders.
// When decoding block with DecodeAll, this will limit the number
// of possible concurrently running decodes.
// When decoding streams, this will limit the number of
// inflight blocks.
// When decoding streams and setting maximum to 1,
// no async decoding will be done.
// The value supplied must be at least 0.
// When a value of 0 is provided GOMAXPROCS will be used.
// By default this will be set to 4 or GOMAXPROCS, whatever is lower.
// Cannot be changed with ResetWithOptions.
//
// Each DecodeAll call holds one decompression context and runs on its
// caller's goroutine; at most n calls hold one at once, and further calls
// wait. Streams are decoded synchronously, whatever n. The value never
// changes the output or the error.
func WithDecoderConcurrency(n int) DOption {
	return func(o *decoderOptions) error {
		if n < 0 {
			return fmt.Errorf("zstd: concurrency must be at least 0, got %d", n)
		}
		if n == 0 {
			n = runtime.GOMAXPROCS(0)
		}
		o.concurrent = n
		return nil
	}
}

// WithDecoderDictDelete removes dictionaries by ID.
// If no ids are passed, all dictionaries are deleted.
// Should be used with ResetWithOptions.
func WithDecoderDictDelete(ids ...uint32) DOption {
	return func(o *decoderOptions) error {
		if len(ids) == 0 {
			o.dicts = nil
		}
		for _, id := range ids {
			delete(o.dicts, id)
		}
		return nil
	}
}

// WithDecoderDictRaw registers a dictionary that may be used by the decoder.
// The slice content can be arbitrary data.
// Can be changed with ResetWithOptions.
//
// C: ZSTD_createDDict_advanced(content, ZSTD_dlm_byCopy,
// ZSTD_dct_rawContent), whose ID is 0 in C; here it is id, so frames whose
// header carries id use it (with id 0: frames without a dictionary ID).
// content is copied.
func WithDecoderDictRaw(id uint32, content []byte) DOption {
	return func(o *decoderOptions) error {
		dd, err := createDDict(content, true)
		if err != nil {
			return err // unreachable: raw content is not parsed
		}
		dd.dictID = id
		o.addDict(dd)
		return nil
	}
}

// WithDecoderDicts allows to register one or more dictionaries for the decoder.
//
// Each slice in dict must be in the [dictionary format] produced by
// "zstd --train" from the Zstandard reference implementation.
//
// If several dictionaries with the same ID are provided, the last one will be used.
// Can be changed with ResetWithOptions.
//
// C: ZSTD_createDDict (ZSTD_dlm_byCopy). Content that does not start with
// the dictionary magic number, which C would take as raw content, returns
// an error; so do invalid entropy tables or repcodes (C's
// dictionary_corrupted). A dictionary whose header ID is 0 is used for
// frames without a dictionary ID. Each slice is copied.
//
// [dictionary format]: https://github.com/facebook/zstd/blob/dev/doc/zstd_compression_format.md#dictionary-format
func WithDecoderDicts(dicts ...[]byte) DOption {
	return func(o *decoderOptions) error {
		for _, b := range dicts {
			if len(b) < 8 || binary.LittleEndian.Uint32(b) != dictMagic {
				return errDictionaryWrong
			}
			dd, err := createDDict(b, false)
			if err != nil {
				return err
			}
			o.addDict(dd)
		}
		return nil
	}
}

// WithDecoderLowmem will set whether to use a lower amount of memory,
// but possibly have to allocate more while running.
// Cannot be changed with ResetWithOptions.
//
// With b, the stream's buffers are freed at each frame end (and at Reset)
// when they hold more than 1 MiB; they are allocated again for the next
// frame. The output is the same.
func WithDecoderLowmem(b bool) DOption {
	return func(o *decoderOptions) error { o.lowMem = b; return nil }
}

// WithDecoderMaxMemory allows to set a maximum decoded size for in-memory
// non-streaming operations or maximum window size for streaming operations.
// This can be used to control memory usage of potentially hostile content.
// Maximum is 1 << 63 bytes. Default is 64GiB.
// Can be changed with ResetWithOptions.
//
// n must be at least 1. On 32-bit platforms the default, and the limit in
// effect, is at most 1 GiB: memory reserved for a larger output could
// exceed the address space, a fatal runtime error rather than an error.
func WithDecoderMaxMemory(n uint64) DOption {
	return func(o *decoderOptions) error {
		if n == 0 {
			return errors.New("zstd: WithDecoderMaxMemory must be at least 1")
		}
		if n > 1<<63 {
			return fmt.Errorf("zstd: WithDecoderMaxMemory must be at most 1<<63, got %d", n)
		}
		o.maxMemory = n
		return nil
	}
}

// WithDecoderMaxWindow allows to set a maximum window size for decodes.
// This allows rejecting packets that will cause big memory usage.
// The Decoder will likely allocate more memory based on the WithDecoderLowmem setting.
// If WithDecoderMaxMemory is set to a lower value, that will be used.
// Default is 512MB, Maximum is ~3.75 TB as per zstandard spec.
// Can be changed with ResetWithOptions.
//
// C: ZSTD_d_windowLogMax at byte granularity (ZSTD_DCtx_setMaxWindowSize).
// DecodeAll applies it too (C's one-shot decoding does not). size must be
// between MinWindowSize and the format's largest window.
func WithDecoderMaxWindow(size uint64) DOption {
	return func(o *decoderOptions) error {
		if size < MinWindowSize {
			return fmt.Errorf("zstd: WithDecoderMaxWindow must be at least %d, got %d", MinWindowSize, size)
		}
		if size > maxWindowSizeSpec {
			return fmt.Errorf("zstd: WithDecoderMaxWindow must be at most %d, got %d", uint64(maxWindowSizeSpec), size)
		}
		o.maxWindow = size
		return nil
	}
}
