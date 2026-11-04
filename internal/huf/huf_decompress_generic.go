// Port of libzstd v1.5.7 lib/decompress/huf_decompress.c (Copyright (c)
// Meta Platforms, Inc. and affiliates; BSD/GPLv2 dual license): the Go fast
// 4X loops (HUF_decompress4X{1,2}_usingDTable_internal_fast_c_loop), which
// run where the assembly loops of huf_decompress_amd64.s do not.

package huf

import (
	"encoding/binary"
	"math/bits"
)

// decompress4X1FastCLoop decodes 5 symbols per stream per iteration while
// no bound can be hit. C:
// huf_decompress.c:HUF_decompress4X1_usingDTable_internal_fast_c_loop, with
// offsets in place of pointers. Each iteration reads at most 7 bytes below
// ip[i] (5 symbols * 11 bits plus fewer than 8 unconsumed bits) and writes 5
// bytes at op[i]; iters bounds both against ilowest (0) and oend.
func decompress4X1FastCLoop(args *decompressFastArgs) {
	bits4 := args.bits
	ip := args.ip
	op := args.op
	dtable := args.dt
	oend := args.oend
	src := args.src
	dst := args.dst

	for {
		// Compute olimit
		// Each iteration produces 5 output symbols per stream.
		oiters := (oend - op[3]) / 5
		// Each iteration consumes up to 11 bits * 5 = 55 bits < 7 bytes per
		// stream.
		iiters := ip[0] / 7 // C: (ip[0] - ilowest) / 7
		// We can safely run iters iterations before running bounds checks.
		iters := min(oiters, iiters)
		symbols := iters * 5

		// We can simply check that op[3] < olimit, instead of checking all
		// of our bounds, since we can't hit the other bounds until we've
		// run iters iterations, which only happens when op[3] == olimit.
		olimit := op[3] + symbols

		// Exit fast decoding loop once we reach the end.
		if op[3] == olimit {
			break
		}

		// Exit the decoding loop if any input pointer has crossed the
		// previous one. This indicates corruption, and a precondition to
		// our loop is that ip[i] >= ip[0].
		if ip[1] < ip[0] || ip[2] < ip[1] || ip[3] < ip[2] {
			break // C: goto _out
		}

		for {
			// Decode 5 symbols in each of the 4 streams.
			// C: HUF_4X1_DECODE_SYMBOL decodes symbol-major across the
			// streams; the streams are independent, so stream-major is
			// equivalent.
			for stream := 0; stream < 4; stream++ {
				b := bits4[stream]
				o := dst[op[stream] : op[stream]+5]
				for symbol := 0; symbol < 5; symbol++ {
					entry := dtable[b>>53]
					b <<= entry & 0x3F
					o[symbol] = byte(entry >> 8)
				}
				bits4[stream] = b
			}

			// Reload each of the 4 the bitstreams. C: HUF_4X1_RELOAD_STREAM
			for stream := 0; stream < 4; stream++ {
				ctz := bits.TrailingZeros64(bits4[stream])
				nbBits := ctz & 7
				nbBytes := ctz >> 3
				op[stream] += 5
				ip[stream] -= nbBytes
				bits4[stream] = binary.LittleEndian.Uint64(src[ip[stream]:]) | 1
				bits4[stream] <<= nbBits
			}
			if op[3] >= olimit {
				break
			}
		}
	}

	// Save the final values of each of the state variables back to args.
	args.bits = bits4
	args.ip = ip
	args.op = op
}

// decompress4X2FastCLoop decodes 5 entries (5 to 10 bytes) per stream per
// iteration while no bound can be hit. C:
// huf_decompress.c:HUF_decompress4X2_usingDTable_internal_fast_c_loop, with
// offsets in place of pointers. Each iteration reads at most 7 bytes below
// ip[i] (5 entries * 11 bits plus fewer than 8 unconsumed bits; every
// entry of an 11-bit X2 table has nbBits <= 11) and writes at most 10
// bytes at op[i] (the last 2-byte store at op[i]+8); iters bounds both
// against ilowest (0) and each stream's oend.
func decompress4X2FastCLoop(args *decompressFastArgs) {
	bits4 := args.bits
	ip := args.ip
	op := args.op
	var oend [4]int
	dtable := args.dtX2
	src := args.src
	dst := args.dst

	oend[0] = op[1]
	oend[1] = op[2]
	oend[2] = op[3]
	oend[3] = args.oend

	for {
		// Compute olimit
		// Each loop does 5 table lookups for each of the 4 streams. Each
		// table lookup consumes up to 11 bits of input, and produces up to
		// 2 bytes of output.
		// We can consume up to 7 bytes of input per iteration per stream.
		// We also know that each input pointer is >= ip[0]. So we can run
		// iters loops before running out of input.
		iters := ip[0] / 7 // C: (ip[0] - ilowest) / 7
		// Each iteration can produce up to 10 bytes of output per stream.
		// Each output stream my advance at different rates. So take the
		// minimum number of safe iterations among all the output streams.
		for stream := 0; stream < 4; stream++ {
			oiters := (oend[stream] - op[stream]) / 10
			iters = min(iters, oiters)
		}

		// Each iteration produces at least 5 output symbols. So until
		// op[3] crosses olimit, we know we haven't executed iters
		// iterations yet. This saves us maintaining an iters counter, at
		// the expense of computing the remaining # of iterations more
		// frequently.
		olimit := op[3] + iters*5

		// Exit the fast decoding loop once we reach the end.
		if op[3] == olimit {
			break
		}

		// Exit the decoding loop if any input pointer has crossed the
		// previous one. This indicates corruption, and a precondition to
		// our loop is that ip[i] >= ip[0].
		if ip[1] < ip[0] || ip[2] < ip[1] || ip[3] < ip[2] {
			break // C: goto _out
		}

		for {
			// Decode 5 entries in each of the 4 streams, then reload it.
			// C: HUF_4X2_DECODE_SYMBOL / HUF_4X2_RELOAD_STREAM decode streams
			// 0-2 symbol-major and stream 3 interleaved with the reloads; each
			// stream still decodes its 5 entries before its own reload, and the
			// streams are independent, so stream-major is equivalent.
			for stream := 0; stream < 4; stream++ {
				b := bits4[stream]
				o := op[stream]
				for symbol := 0; symbol < 5; symbol++ {
					entry := dtable[b>>53]
					dst[o] = byte(entry) // C: MEM_write16(op, entry.sequence)
					dst[o+1] = byte(entry >> 8)
					b <<= (entry >> 16) & 0x3F
					o += int(entry >> 24)
				}
				op[stream] = o

				// C: HUF_4X2_RELOAD_STREAM
				ctz := bits.TrailingZeros64(b)
				nbBits := ctz & 7
				nbBytes := ctz >> 3
				ip[stream] -= nbBytes
				b = binary.LittleEndian.Uint64(src[ip[stream]:]) | 1
				b <<= nbBits
				bits4[stream] = b
			}
			if op[3] >= olimit {
				break
			}
		}
	}

	// Save the final values of each of the state variables back to args.
	args.bits = bits4
	args.ip = ip
	args.op = op
}
