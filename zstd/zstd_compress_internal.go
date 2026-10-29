package zstd

import (
	"errors"

	"github.com/vahid-sohrabloo/fastzstd/internal/huf"
)

// Internal errors, mirroring the C error codes returned at the same points.
var (
	errDstSizeTooSmall = errors.New("zstd: destination buffer too small") // ERROR(dstSize_tooSmall)
	errGeneric         = errors.New("zstd: generic error")                // ERROR(GENERIC)
)

// strategy is the match-finding strategy. C: zstd.h:ZSTD_strategy
type strategy int

const (
	zstdFast strategy = 1 + iota
	zstdDfast
	zstdGreedy
	zstdLazy
	zstdLazy2
	zstdBtlazy2
	zstdBtopt
	zstdBtultra
	zstdBtultra2
)

// paramSwitch. C: zstd.h:ZSTD_ParamSwitch_e
type paramSwitch int

const (
	psAuto    paramSwitch = 0 // let the library choose
	psEnable  paramSwitch = 1 // force-enable the feature
	psDisable paramSwitch = 2 // do not use the feature
)

// compressionParameters. C: zstd.h:ZSTD_compressionParameters
type compressionParameters struct {
	windowLog, chainLog, hashLog, searchLog, minMatch, targetLength uint32
	strategy                                                        strategy
}

// cctxParams holds the applied compression parameters. C:
// zstd_compress_internal.h:ZSTD_CCtx_params_s. Only the fields the ported
// stages read are present; later stages add theirs.
type cctxParams struct {
	cParams                compressionParameters
	literalCompressionMode paramSwitch
}

// fseRepeat. C: fse.h:FSE_repeat
type fseRepeat int

const (
	fseRepeatNone  fseRepeat = iota // cannot use the previous table
	fseRepeatCheck                  // can use the previous table but it must be checked
	fseRepeatValid                  // can use the previous table and it is assumed to be valid
)

// Entropy table sizes. C: zstd_compress_internal.h:ZSTD_hufCTables_t and
// ZSTD_fseCTables_t (HUF_CTABLE_SIZE_ST(255), FSE_CTABLE_SIZE_U32(log, max)).
const (
	hufCTableSize     = 255 + 2
	offcodeCTableSize = 1 + 1<<(offFSELog-1) + (maxOff+1)*2
	mlCTableSize      = 1 + 1<<(mlFSELog-1) + (maxML+1)*2
	llCTableSize      = 1 + 1<<(llFSELog-1) + (maxLL+1)*2
)

// hufCTables. C: zstd_compress_internal.h:ZSTD_hufCTables_t
type hufCTables struct {
	cTable     [hufCTableSize]huf.CElt
	repeatMode huf.Repeat
}

// fseCTables. C: zstd_compress_internal.h:ZSTD_fseCTables_t
type fseCTables struct {
	offcodeCTable         [offcodeCTableSize]uint32
	matchlengthCTable     [mlCTableSize]uint32
	litlengthCTable       [llCTableSize]uint32
	offcodeRepeatMode     fseRepeat
	matchlengthRepeatMode fseRepeat
	litlengthRepeatMode   fseRepeat
}

// entropyCTables. C: zstd_compress_internal.h:ZSTD_entropyCTables_t. The
// tables are arrays so that assignment copies them like C's memcpy.
type entropyCTables struct {
	huf hufCTables
	fse fseCTables
}

const (
	maxHufHeaderSize = 128 // C: ZSTD_MAX_HUF_HEADER_SIZE
	// C: ZSTD_MAX_FSE_HEADERS_SIZE
	maxFSEHeadersSize = ((maxML+1)*mlFSELog + (maxLL+1)*llFSELog + (maxOff+1)*offFSELog + 7) / 8
)

// hufCTablesMetadata. C: zstd_compress_internal.h:ZSTD_hufCTablesMetadata_t
type hufCTablesMetadata struct {
	hType        symbolEncodingType
	hufDesBuffer [maxHufHeaderSize]byte
	hufDesSize   int
}

// fseCTablesMetadata. C: zstd_compress_internal.h:ZSTD_fseCTablesMetadata_t
type fseCTablesMetadata struct {
	llType, ofType, mlType symbolEncodingType
	fseTablesBuffer        [maxFSEHeadersSize]byte
	fseTablesSize          int
	lastCountSize          int // accounts for a bug in 1.3.4, see entropyCompressSeqStoreInternal
}

// entropyCTablesMetadata. C: zstd_compress_internal.h:ZSTD_entropyCTablesMetadata_t
type entropyCTablesMetadata struct {
	hufMetadata hufCTablesMetadata
	fseMetadata fseCTablesMetadata
}

// Workspace sizes in bytes. C: zstd_compress_internal.h
const (
	compressSequencesWorkspaceSize = 4 * (maxSeq + 2)                                   // C: COMPRESS_SEQUENCES_WORKSPACE_SIZE
	entropyWorkspaceSize           = huf.WorkspaceSize + compressSequencesWorkspaceSize // C: ENTROPY_WORKSPACE_SIZE
)

// minGain is the minimum saving needed to emit a compressed block or
// literals section. C: zstd_compress_internal.h:ZSTD_minGain
func minGain(srcSize int, strat strategy) int {
	minlog := 6
	if strat >= zstdBtultra {
		minlog = int(strat) - 1
	}
	return srcSize>>minlog + 2
}

// literalsCompressionIsDisabled.
// C: zstd_compress_internal.h:ZSTD_literalsCompressionIsDisabled
func literalsCompressionIsDisabled(p *cctxParams) bool {
	switch p.literalCompressionMode {
	case psEnable:
		return false
	case psDisable:
		return true
	default: // psAuto
		return p.cParams.strategy == zstdFast && p.cParams.targetLength > 0
	}
}
