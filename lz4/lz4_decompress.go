package lz4

import (
	"errors"
	"math/bits"
)

// The block decoder of lz4.c (lz4 v1.11.1, Copyright (c) Yann Collet,
// BSD 2-Clause): LZ4_decompress_generic and the LZ4_decompress_safe*
// entry points. This file is a decoder file: no unsafe, no recover
// (scripts/decodercheck.sh).

// Errors of the block decoder, one per negative result of C's
// LZ4_decompress_safe.
var (
	// ErrCorrupt reports malformed input (C: -1).
	ErrCorrupt = errors.New("lz4: corrupt input")
	// ErrDstTooSmall reports output that does not fit into dst while
	// input remains (C: -2).
	ErrDstTooSmall = errors.New("lz4: output buffer too small")
	// ErrOffset reports a match reaching before the start of the history
	// (C: -3).
	ErrOffset = errors.New("lz4: match offset beyond history")
)

// errorCode returns C's result for a decoder error.
func errorCode(err error) int {
	switch err {
	case ErrDstTooSmall:
		return -2
	case ErrOffset:
		return -3
	}
	return -1
}

const maxInt = int(^uint(0) >> 1)

// readVariableLength is C's read_variable_length: the extension bytes of a
// length, read below ilimit. ok is false at ilimit or on overflow (32-bit).
func readVariableLength(src []byte, ip, ilimit int) (length, nip int, ok bool) {
	if ip >= ilimit {
		return 0, ip, false
	}
	s := int(src[ip])
	ip++
	length = s
	if s != 255 {
		return length, ip, true
	}
	for {
		if ip >= ilimit {
			return 0, ip, false
		}
		s = int(src[ip])
		ip++
		length += s
		if bits.UintSize < 64 && length > maxInt/2 {
			return 0, ip, false
		}
		if s != 255 {
			return length, ip, true
		}
	}
}

// copyMatch copies length bytes from out[match:] to out[op:] with LZ77
// semantics (match < op: overlapping bytes repeat; match == op, the invalid
// offset 0, writes zeros as C does). It writes nothing beyond op+length.
func copyMatch(out []byte, op, match, length int) int {
	if match == op {
		clear(out[op : op+length])
		return op + length
	}
	end := op + length
	for op < end {
		n := copy(out[op:end], out[match:op])
		op += n
	}
	return end
}

// copyExtDictMatch is C's LZ4_copyExtDictMatch: a match starting in the
// external dictionary dict (at dict index len(dict)-(lowPrefix-match)),
// possibly continuing at the start of the prefix.
func copyExtDictMatch(out []byte, op, match, length, lowPrefix int, dict []byte) int {
	back := lowPrefix - match
	if length <= back {
		s := len(dict) - back
		copy(out[op:op+length], dict[s:s+length])
		return op + length
	}
	copy(out[op:op+back], dict[len(dict)-back:])
	return copyMatch(out, op+back, lowPrefix, length-back)
}

// copy18 copies the 18 bytes of a short match (offset >= 8) in the order
// C's three memcpy calls do.
func copy18(out []byte, op, match int) {
	copy(out[op:op+8], out[match:match+8])
	copy(out[op+8:op+16], out[match+8:match+16])
	copy(out[op+16:op+18], out[match+16:match+18])
}

// decompressGeneric is C's LZ4_decompress_generic. The output is
// out[op0:op0+outputSize]; the prefix (decoded history in the same buffer)
// is out[lowPrefix:op0]; dict is the external dictionary (usingExtDict).
// It returns the number of bytes decoded, or an error with C's class.
func decompressGeneric(src, out []byte, op0, outputSize int, partial bool, dd dictDirective,
	lowPrefix int, dict []byte) (int, error) {
	if outputSize < 0 {
		return 0, ErrCorrupt
	}
	ip := 0
	iend := len(src)
	op := op0
	oend := op0 + outputSize
	dictSize := len(dict)
	checkOffset := dd != withPrefix64k && dictSize < 64*kb
	shortiend := iend - 14 - 2
	shortoend := oend - 14 - 18

	var (
		token, ll, ml, offset, match int
		ok                           bool
		addl                         int
	)
	pendingMatch := false

	if outputSize == 0 {
		if partial {
			return 0, nil
		}
		if len(src) == 1 && src[0] == 0 {
			return 0, nil
		}
		if len(src) > 1 {
			return 0, ErrDstTooSmall
		}
		return 0, ErrCorrupt
	}
	if len(src) <= 0 {
		return 0, ErrCorrupt
	}

	if oend-op < fastloopSafeDistance {
		goto safeDecode
	}
	// C also leaves the fast loop when an external dictionary lies just
	// ahead of the output (a ring buffer): dict must not overlap out here.

	if useDecAsm && dd == noDict && oend <= len(out) && lowPrefix >= 0 && lowPrefix <= op {
		var st int
		ip, op, offset, ml, st = decodeFastAsm(src, out, ip, op, oend, lowPrefix)
		switch st {
		case 1:
			match = op - offset
			pendingMatch = true
		case 2:
			return 0, ErrCorrupt
		case 3:
			return 0, ErrOffset
		}
		goto safeDecode
	}

	// Fast loop
	for {
		token = int(src[ip])
		ip++
		ll = token >> mlBits
		if ip > iend-(16+1) {
			ip--
			break
		}
		if ll == runMask {
			tokenPos := ip - 1
			addl, ip, ok = readVariableLength(src, ip, iend-runMask)
			if !ok {
				return 0, ErrCorrupt
			}
			ll += addl
			if ll > oend-64-op || ll > iend-64-ip {
				ip = tokenPos
				break
			}
			copy(out[op:op+ll], src[ip:ip+ll])
			ip += ll
			op += ll
		} else {
			copy(out[op:op+ll], src[ip:ip+ll])
			ip += ll
			op += ll
		}

		offset = int(le16(src[ip:]))
		ip += 2
		match = op - offset

		ml = token & mlMask
		if ml == mlMask {
			addl, ip, ok = readVariableLength(src, ip, iend-(1+lastLiterals))
			if !ok {
				return 0, ErrCorrupt
			}
			ml += addl
		}
		ml += minMatch

		if ml >= oend-fastloopSafeDistance-op {
			pendingMatch = true
			goto safeDecode
		}

		if token&mlMask != mlMask && offset >= 8 && (dd == withPrefix64k || match >= lowPrefix) {
			copy18(out, op, match)
			op += ml
			continue
		}

		if checkOffset && match+dictSize < lowPrefix {
			return 0, ErrOffset
		}
		if dd == usingExtDict && match < lowPrefix {
			op = copyExtDictMatch(out, op, match, ml, lowPrefix, dict)
			continue
		}
		op = copyMatch(out, op, match, ml)
	}

safeDecode:
	for {
		if pendingMatch {
			pendingMatch = false
			goto safeMatchCopy
		}
		token = int(src[ip])
		ip++
		ll = token >> mlBits

		if ll != runMask && ip < shortiend && op <= shortoend {
			copy(out[op:op+16], src[ip:ip+16])
			op += ll
			ip += ll

			offset = int(le16(src[ip:]))
			ip += 2
			match = op - offset
			ml = token & mlMask
			if ml != mlMask && offset >= 8 && (dd == withPrefix64k || match >= lowPrefix) {
				copy18(out, op, match)
				op += ml + minMatch
				continue
			}
		} else {
			if ll == runMask {
				addl, ip, ok = readVariableLength(src, ip, iend-runMask)
				if !ok {
					return 0, ErrCorrupt
				}
				ll += addl
			}
			if ll > oend-mfLimit-op || ll > iend-(2+1+lastLiterals)-ip {
				if partial {
					ll = min(ll, iend-ip, oend-op)
				} else if ll != iend-ip || ll > oend-op {
					if ll <= iend-ip && ll > oend-mfLimit-op {
						return 0, ErrDstTooSmall
					}
					return 0, ErrCorrupt
				}
				copy(out[op:op+ll], src[ip:ip+ll])
				ip += ll
				op += ll
				if !partial || op == oend || ip >= iend-2 {
					break
				}
			} else {
				copy(out[op:op+ll], src[ip:ip+ll])
				ip += ll
				op += ll
			}

			offset = int(le16(src[ip:]))
			ip += 2
			match = op - offset
			ml = token & mlMask
		}

		if ml == mlMask {
			addl, ip, ok = readVariableLength(src, ip, iend-(1+lastLiterals))
			if !ok {
				return 0, ErrCorrupt
			}
			ml += addl
		}
		ml += minMatch

	safeMatchCopy:
		if checkOffset && match+dictSize < lowPrefix {
			return 0, ErrOffset
		}
		if ml > oend-lastLiterals-op {
			if partial {
				ml = min(ml, oend-op)
			} else {
				return 0, ErrDstTooSmall
			}
		}
		if dd == usingExtDict && match < lowPrefix {
			op = copyExtDictMatch(out, op, match, ml, lowPrefix, dict)
			continue
		}
		op = copyMatch(out, op, match, ml)
		if op == oend {
			break
		}
	}
	return op - op0, nil
}

// DecompressSafe decodes the block src into dst (C: LZ4_decompress_safe).
// dst's length is the capacity. It returns the decoded size. Corrupt,
// truncated or hostile input returns an error, never reads or writes out
// of bounds.
func DecompressSafe(dst, src []byte) (int, error) {
	return decompressGeneric(src, dst, 0, len(dst), false, noDict, 0, nil)
}

// DecompressSafePartial decodes the start of the block src into dst, at
// least targetOutputSize bytes when the block has them (C:
// LZ4_decompress_safe_partial). It returns the size decoded, which may be
// larger than targetOutputSize (up to len(dst)).
func DecompressSafePartial(dst, src []byte, targetOutputSize int) (int, error) {
	return decompressGeneric(src, dst, 0, min(targetOutputSize, len(dst)), true, noDict, 0, nil)
}

// DecompressSafeUsingDict decodes the block src, compressed with dict as
// history, into dst (C: LZ4_decompress_safe_usingDict). dict must not
// overlap dst. When dict and dst are adjacent parts of one slice (dst
// starts where dict ends, within dict's capacity), dict is used in place as
// a prefix.
func DecompressSafeUsingDict(dst, src, dict []byte) (int, error) {
	return decompressUsingDict(dst, src, dict, len(dst), false)
}

// DecompressSafePartialUsingDict is DecompressSafePartial with a dictionary
// (C: LZ4_decompress_safe_partial_usingDict).
func DecompressSafePartialUsingDict(dst, src, dict []byte, targetOutputSize int) (int, error) {
	return decompressUsingDict(dst, src, dict, min(targetOutputSize, len(dst)), true)
}

func decompressUsingDict(dst, src, dict []byte, outputSize int, partial bool) (int, error) {
	if len(dict) == 0 {
		return decompressGeneric(src, dst, 0, outputSize, partial, noDict, 0, nil)
	}
	if len(dst) > 0 && cap(dict) > len(dict) && cap(dict)-len(dict) >= len(dst) &&
		&dict[:len(dict)+1][len(dict)] == &dst[0] {
		out := dict[:len(dict)+len(dst)]
		return decompressGeneric(src, out, len(dict), outputSize, partial, noDict, 0, nil)
	}
	return decompressGeneric(src, dst, 0, outputSize, partial, usingExtDict, 0, dict)
}

// decompressBlock decodes a whole block (no partial decoding) into
// out[op0:op0+outputSize]; it is decompressGeneric, or the assembly loop
// where there is one.
func decompressBlock(src, out []byte, op0, outputSize int, dd dictDirective, lowPrefix int, dict []byte) (int, error) {
	return decompressGeneric(src, out, op0, outputSize, false, dd, lowPrefix, dict)
}
