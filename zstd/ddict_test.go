package zstd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
	"github.com/vahid-sohrabloo/fastzstd/internal/fse"
	"github.com/vahid-sohrabloo/fastzstd/internal/huf"
)

// decDict is a dictionary of the decoder tests: a zstd/testdata file,
// loaded as raw content or as ZSTD_dct_auto.
type decDict struct {
	file string
	raw  bool
}

var decDicts = []decDict{{"train.dict", false}, {"train-12k.dict", false}, {"dickens-32k.raw", true}}

// path returns the dictionary file's absolute path (cref runs elsewhere).
func (dc decDict) path(t testing.TB) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", dc.file))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// crefArgs returns the cref options loading the dictionary.
func (dc decDict) crefArgs(t testing.TB) []string {
	args := []string{"-dict", dc.path(t)}
	if dc.raw {
		args = append(args, "-raw-dict", "1")
	}
	return args
}

func mustDDict(t testing.TB, dict []byte, raw bool) *ddict {
	t.Helper()
	dd, err := createDDict(dict, raw)
	if err != nil {
		t.Fatal(err)
	}
	return dd
}

// dictContent returns the content of a dictionary as the encoder sees it:
// all of it for raw content, what follows the entropy tables otherwise.
func dictContent(t testing.TB, dict []byte, raw bool) []byte {
	t.Helper()
	if raw {
		return dict
	}
	var e entropyDTables
	e.hufTable.Init(hufDTableCapacityLog)
	n, de := loadDEntropy(&e, dict)
	if de != 0 {
		t.Fatalf("loadDEntropy: %d", de)
	}
	return dict[n:]
}

// firstLitType returns the literals block type of the first block of
// frame (a single frame), or -1 when that block is not compressed.
func firstLitType(t testing.TB, frame []byte) int {
	t.Helper()
	hs, e := frameHeaderSizeInternal(frame)
	if e != 0 || len(frame) < hs+blockHeaderSize+1 {
		t.Fatalf("bad frame")
	}
	_, bp, e := getcBlockSize(frame[hs:])
	if e != 0 || bp.blockType != btCompressed {
		return -1
	}
	return int(frame[hs+blockHeaderSize] & 3)
}

// TestDecompressDictCref decodes C frames made with each test dictionary
// (cref compress -dict, ZSTD_dct_auto or raw content) at levels 1, 3, 7
// and 11, for 100 B, 4 KiB, 64 KiB and 1 MiB of text and two inputs that
// repeat the dictionary's content (matches from the dictionary into the
// frame's own output). Each frame is decoded with the dictionary given
// (C's single ZSTD_DCtx_refDDict), through a set of dictionaries, and
// twice on one context (the second time the dictionary is "hot"). Matches
// spanning the dictionary/prefix boundary and first blocks reusing the
// dictionary's Huffman table (treeless literals) must occur.
func TestDecompressDictCref(t *testing.T) {
	names, inputs := dictInputs(t)
	var texts []string
	for _, n := range names {
		switch n {
		case "text100", "text4096", "text65536", "text1048576":
			texts = append(texts, n)
		}
	}
	if len(texts) != 4 {
		t.Fatalf("inputs %v", texts)
	}
	spans0 := extDictSpans.Load()
	var treeless, frames int
	t.Run("group", func(t *testing.T) {
		for _, dc := range decDicts {
			dict := readTestdata(t, dc.file)
			dd := mustDDict(t, dict, dc.raw)
			set := newDDictSet()
			set.add(mustDDict(t, readTestdata(t, "train.dict"), false))
			set.add(dd)
			content := dictContent(t, dict, dc.raw)
			in := map[string][]byte{
				"twice": slices.Concat(content, content),
				"tail":  bytes.Repeat(content[len(content)-1000:], 8),
			}
			for _, n := range texts {
				in[n] = inputs[n]
			}
			for _, lv := range []string{"1", "3", "7", "11"} {
				for name, src := range in {
					frame := creftest.Run(t, src, append([]string{"compress", "-level", lv, "-checksum", "1"}, dc.crefArgs(t)...)...)
					frames++
					if firstLitType(t, frame) == int(setRepeat) {
						treeless++
					}
					t.Run(dc.file+"/L"+lv+"/"+name, func(t *testing.T) {
						t.Parallel()
						d := newDCtx()
						for i := range 2 {
							got, err := d.decompressMultiFrame(nil, frame, dd)
							if err != nil || !bytes.Equal(got, src) {
								t.Fatalf("pass %d: err %v, equal %v", i, err, bytes.Equal(got, src))
							}
						}
						s := newDCtx()
						s.ddicts = set
						got, err := s.decompressMultiFrame([]byte("x"), slices.Concat(frame, frame), nil)
						if err != nil || !bytes.Equal(got, slices.Concat([]byte("x"), src, src)) {
							t.Fatalf("set: err %v", err)
						}
					})
				}
			}
		}
	})
	if extDictSpans.Load() == spans0 {
		t.Error("no match spanned the dictionary/prefix boundary")
	}
	if treeless == 0 {
		t.Error("no first block reused the dictionary's Huffman table")
	}
	t.Logf("%d frames, %d with treeless first literals, %d spanning matches", frames, treeless, extDictSpans.Load()-spans0)
}

// TestDecompressFramesDictExtDict pins decompressFrames passing the
// frame's output space (C's dstCapacity), not the reserved buffer's length
// (always 0), to checkContinuity: only then does the dictionary become the
// extDict. Frames whose first match is in the dictionary are decoded with
// the whole space reserved (a plausible content size) and with a smaller
// reservation (more than frameTrustRatio times the frame size).
func TestDecompressFramesDictExtDict(t *testing.T) {
	dc := decDict{"dickens-32k.raw", true}
	dict := readTestdata(t, dc.file)
	dd := mustDDict(t, dict, true)
	for _, n := range []int{1000, 4 << 20} {
		src := bytes.Repeat(dict[len(dict)-1000:], n/1000)
		frame := creftest.Run(t, src, append([]string{"compress", "-level", "3"}, dc.crefArgs(t)...)...)
		trusted := uint64(len(src))/frameTrustRatio <= uint64(len(frame))
		got, err := newDCtx().decompressMultiFrame(nil, frame, dd)
		if err != nil || !bytes.Equal(got, src) {
			t.Fatalf("%d bytes (whole space reserved: %v): err %v, equal %v", n, trusted, err, bytes.Equal(got, src))
		}
		if trusted != (n == 1000) {
			t.Fatalf("%d bytes: whole space reserved %v", n, trusted)
		}
	}
}

// TestDictIDMismatch: a frame whose dictionary ID is not the loaded
// dictionary's fails with ErrUnknownDictionary, as C fails with
// dictionary_wrong; that check comes before any output space is reserved
// or the memory limit is checked.
func TestDictIDMismatch(t *testing.T) {
	train := decDict{"train.dict", false}
	src := dictInputsText(t, 64<<10)
	frame := creftest.Run(t, src, append([]string{"compress", "-level", "3"}, train.crefArgs(t)...)...)
	other := decDict{"train-12k.dict", false}
	_, ce := creftest.DecodeCRef(t, frame, other.crefArgs(t)...)
	if ce == nil || ce.Code != int(dErrDictionaryWrong) {
		t.Fatalf("C: %v, want dictionary_wrong", ce)
	}
	otherDD := mustDDict(t, readTestdata(t, other.file), false)
	rawDD := mustDDict(t, readTestdata(t, "dickens-32k.raw"), true)
	set := newDDictSet()
	set.add(otherDD)
	set.add(rawDD) // ID 0: never chosen for a frame with an ID
	for _, c := range []struct {
		name   string
		dd     *ddict
		set    *ddictSet
		memory uint64
	}{
		{"no dictionary", nil, nil, 0},
		{"other dictionary", otherDD, nil, 0},
		{"raw dictionary", rawDD, nil, 0},
		{"set without it", nil, set, 0},
		{"set without it, with a default", otherDD, set, 0},
		{"memory limit below the content size", otherDD, nil, 100},
	} {
		d := newDCtx()
		d.ddicts = c.set
		if c.memory != 0 {
			d.maxMemory = c.memory
		}
		got, err := d.decompressMultiFrame(nil, frame, c.dd)
		checkSentinel(t, c.name, err, ErrUnknownDictionary)
		if cap(got) != 0 {
			t.Errorf("%s: %d bytes reserved", c.name, cap(got))
		}
	}
}

// dictInputsText returns n bytes of the TestDictCref text.
func dictInputsText(t *testing.T, n int) []byte {
	t.Helper()
	_, m := dictInputs(t)
	b, ok := m["text"+strconv.Itoa(n)]
	if !ok {
		t.Fatalf("no text%d", n)
	}
	return b
}

// manyDicts returns n zstd dictionaries: train-12k.dict with IDs 1000.. and
// 256 bytes of content of its own appended to each, and for each an input
// that only that dictionary decodes (its own bytes, repeated).
func manyDicts(t testing.TB, n int) (dicts, inputs [][]byte) {
	base := readTestdata(t, "train-12k.dict")
	r := rand.New(rand.NewPCG(10, 300))
	for i := range n {
		own := make([]byte, 256)
		for j := range own {
			own[j] = byte(r.Uint32())
		}
		d := slices.Concat(base, own)
		binary.LittleEndian.PutUint32(d[4:], uint32(1000+i))
		dicts = append(dicts, d)
		inputs = append(inputs, slices.Concat(own, own[:100], []byte(fmt.Sprintf("frame %d", i)), own))
	}
	return dicts, inputs
}

// TestDDictSetMany registers 300 dictionaries in one set (C's
// ZSTD_DDictHashSet: 64 slots, doubled whenever count*4/size*3 != 0, so 5
// expansions to 2048) and decodes frames that each name their own; a
// replaced ID decodes with the new dictionary, an unknown ID fails.
func TestDDictSetMany(t *testing.T) {
	const n = 300
	dicts, inputs := manyDicts(t, n)
	set := newDDictSet()
	var all, want []byte
	frames := make([][]byte, n)
	for i, dict := range dicts {
		set.add(mustDDict(t, dict, false))
		e := newTestEncoder(t, WithEncoderLevel(SpeedFastest), WithEncoderDict(dict))
		frames[i] = e.EncodeAll(inputs[i], nil)
		all = append(all, frames[i]...)
		want = append(want, inputs[i]...)
	}
	if len(set.table) != 2048 || set.count != n {
		t.Fatalf("table %d slots, %d dictionaries; want 2048, %d", len(set.table), set.count, n)
	}
	for i := range n {
		if dd := set.get(uint32(1000 + i)); dd == nil || dd.dictID != uint32(1000+i) {
			t.Fatalf("get(%d) = %v", 1000+i, dd)
		}
	}
	d := newDCtx()
	d.ddicts = set
	got, err := d.decompressMultiFrame(nil, all, nil)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("all frames: err %v, equal %v", err, bytes.Equal(got, want))
	}
	// A frame with another dictionary's ID decodes to something else or
	// fails: each frame needs its own.
	if got, err := newDCtx().decompressMultiFrame(nil, frames[7], mustDDict(t, dicts[8], false)); err == nil && bytes.Equal(got, inputs[7]) {
		t.Fatal("frame 7 decodes with dictionary 8")
	}
	// Replace ID 1005: same count, the new content decodes.
	newDicts, newInputs := manyDicts(t, 400)
	repl := slices.Clone(newDicts[399])
	binary.LittleEndian.PutUint32(repl[4:], 1005)
	set.add(mustDDict(t, repl, false))
	if set.count != n {
		t.Fatalf("count %d after replacing", set.count)
	}
	frame := newTestEncoder(t, WithEncoderLevel(SpeedFastest), WithEncoderDict(repl)).EncodeAll(newInputs[399], nil)
	if got, err := d.decompressMultiFrame(nil, frame, nil); err != nil || !bytes.Equal(got, newInputs[399]) {
		t.Fatalf("replaced: err %v", err)
	}
	// Unknown ID.
	unknown := slices.Clone(newDicts[350]) // ID 1350
	frame = newTestEncoder(t, WithEncoderLevel(SpeedFastest), WithEncoderDict(unknown)).EncodeAll(newInputs[350], nil)
	_, err = d.decompressMultiFrame(nil, frame, nil)
	checkSentinel(t, "unknown ID", err, ErrUnknownDictionary)
}

// TestDDictSetProbe pins the hash set's probing: colliding IDs fill the
// following slots, wrapping at the end of the table, and get stops at the
// first empty slot.
func TestDDictSetProbe(t *testing.T) {
	set := newDDictSet()
	mask := len(set.table) - 1
	// IDs hashing to the last slot.
	var ids []uint32
	for id := uint32(1); len(ids) < 3; id++ {
		if set.getIndex(id) == mask {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if e := set.emplace(&ddict{dictID: id}); e != 0 {
			t.Fatal(e)
		}
	}
	for i, slot := range []int{mask, 0, 1} {
		if set.table[slot] == nil || set.table[slot].dictID != ids[i] {
			t.Fatalf("slot %d does not hold ID %d", slot, ids[i])
		}
	}
	for _, id := range ids {
		if dd := set.get(id); dd == nil || dd.dictID != id {
			t.Fatalf("get(%d) = %v", id, dd)
		}
	}
	// A dictionary of ID 0 in the probe path of another ID: get returns it
	// (C's quirk), selectFrameDDict does not.
	set0 := newDDictSet()
	set0.emplace(&ddict{dictID: 0})
	var other uint32
	for id := uint32(1); ; id++ {
		if set0.getIndex(id) == set0.getIndex(0) {
			other = id
			break
		}
	}
	if dd := set0.get(other); dd == nil || dd.dictID != 0 {
		t.Fatalf("get(%d) = %v, want the ID-0 dictionary", other, dd)
	}
	if dd := set0.selectFrameDDict(other); dd != nil {
		t.Fatalf("selectFrameDDict(%d) = ID %d", other, dd.dictID)
	}
	if dd := set0.selectFrameDDict(0); dd == nil || dd.dictID != 0 {
		t.Fatalf("selectFrameDDict(0) = %v", dd)
	}
	var absent uint32
	for id := uint32(1); ; id++ {
		if set.getIndex(id) == mask && !slices.Contains(ids, id) {
			absent = id
			break
		}
	}
	if dd := set.get(absent); dd != nil {
		t.Fatalf("get(%d) = ID %d", absent, dd.dictID)
	}
}

// collidingIDs returns n nonzero IDs whose probe starts where ID 0's does
// in a table of size slots.
func collidingIDs(size, n int) []uint32 {
	s := &ddictSet{table: make([]*ddict, size)}
	var ids []uint32
	for id := uint32(1); len(ids) < n; id++ {
		if s.getIndex(id) == s.getIndex(0) {
			ids = append(ids, id)
		}
	}
	return ids
}

// TestDDictSetShadowID0: a dictionary of ID 0 on another dictionary's
// probe path stops C's lookup (get returns the ID-0 one), but
// selectFrameDDict still finds every registered ID: registered after the
// ID-0 one, after the table has grown (expand re-emplaces in slot order),
// and with a zstd dictionary whose header ID is 0, which frames without a
// dictionary ID then use.
func TestDDictSetShadowID0(t *testing.T) {
	t.Run("after ID 0", func(t *testing.T) {
		set := newDDictSet()
		set.add(&ddict{dictID: 0})
		x := collidingIDs(len(set.table), 1)[0]
		set.add(&ddict{dictID: x})
		if dd := set.get(x); dd == nil || dd.dictID != 0 {
			t.Fatalf("get(%d) = %v, want C's ID-0 stop", x, dd)
		}
		if dd := set.selectFrameDDict(x); dd == nil || dd.dictID != x {
			t.Fatalf("selectFrameDDict(%d) = %v", x, dd)
		}
	})
	t.Run("after expand", func(t *testing.T) {
		set := newDDictSet()
		set.add(&ddict{dictID: 0})
		colliding := collidingIDs(2048, 4)
		var ids []uint32
		for _, id := range colliding {
			set.add(&ddict{dictID: id})
			ids = append(ids, id)
		}
		for id := uint32(100000); len(ids) < 300; id++ {
			set.add(&ddict{dictID: id})
			ids = append(ids, id)
		}
		if len(set.table) != 2048 {
			t.Fatalf("table %d slots", len(set.table))
		}
		shadowed := 0
		for _, id := range colliding {
			if dd := set.get(id); dd != nil && dd.dictID == 0 {
				shadowed++
			}
		}
		if shadowed == 0 {
			t.Fatal("no ID shadowed for C's lookup: the case is not exercised")
		}
		for _, id := range ids {
			if dd := set.selectFrameDDict(id); dd == nil || dd.dictID != id {
				t.Fatalf("selectFrameDDict(%d) = %v", id, dd)
			}
		}
		if dd := set.selectFrameDDict(0); dd == nil || dd.dictID != 0 {
			t.Fatalf("selectFrameDDict(0) = %v", dd)
		}
	})
	t.Run("zstd dictionary with ID 0", func(t *testing.T) {
		zero := slices.Clone(readTestdata(t, "train-12k.dict"))
		binary.LittleEndian.PutUint32(zero[4:], 0)
		dd0 := mustDDict(t, zero, false)
		if dd0.dictID != 0 || !dd0.entropyPresent {
			t.Fatalf("dictID %d, entropy %v", dd0.dictID, dd0.entropyPresent)
		}
		dicts, inputs := manyDicts(t, 1)
		other := mustDDict(t, dicts[0], false)
		set := newDDictSet()
		set.add(dd0)
		set.add(other)
		src := dictInputsText(t, 4096)
		frame0 := newTestEncoder(t, WithEncoderLevel(SpeedFastest), WithEncoderDict(zero)).EncodeAll(src, nil)
		if hs, _ := frameHeaderSizeInternal(frame0); frame0[4]&3 != 0 || hs == 0 {
			t.Fatal("the ID-0 dictionary's frame has a dictionary ID field")
		}
		frame1 := newTestEncoder(t, WithEncoderLevel(SpeedFastest), WithEncoderDict(dicts[0])).EncodeAll(inputs[0], nil)
		d := newDCtx()
		d.ddicts = set
		got, err := d.decompressMultiFrame(nil, slices.Concat(frame0, frame1, frame0), nil)
		if want := slices.Concat(src, inputs[0], src); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("err %v, equal %v", err, bytes.Equal(got, want))
		}
	})
}

// TestInspectDictionary compares InspectDictionary with C
// (ZSTD_getDictID_fromDict, ZSTD_loadDEntropy, through cref dictinfo) on
// the trained dictionaries and variants, and createDDict's acceptance with
// ZSTD_createDDict_advanced (ZSTD_dct_auto): raw content is an error for
// InspectDictionary but a valid (content-only) decoder dictionary.
func TestInspectDictionary(t *testing.T) {
	train := readTestdata(t, "train.dict")
	t12 := readTestdata(t, "train-12k.dict")
	hdr := len(t12) - len(dictContent(t, t12, false)) // the header of both
	reps := func(d []byte, r0, r1, r2 uint32) []byte {
		d = slices.Clone(d)
		at := hdr - 12
		binary.LittleEndian.PutUint32(d[at:], r0)
		binary.LittleEndian.PutUint32(d[at+4:], r1)
		binary.LittleEndian.PutUint32(d[at+8:], r2)
		return d
	}
	cases := map[string][]byte{
		"train":           train,
		"train12k":        t12,
		"raw":             readTestdata(t, "dickens-32k.raw"),
		"empty":           {},
		"short":           t12[:7],
		"magic only":      t12[:8],
		"magic+1":         t12[:9],
		"header cut":      t12[:100],
		"header only":     t12[:133],
		"rep custom":      reps(t12, 3, 1000, 12155),
		"rep 0":           reps(t12, 0, 4, 8),
		"rep beyond":      reps(t12, 1, 4, 12156),
		"content 1 byte":  reps(t12[:134], 1, 1, 1),
		"huf corrupted":   slices.Concat(t12[:8], []byte{255, 255, 255}, t12[11:]),
		"fse corrupted":   slices.Concat(t12[:70], bytes.Repeat([]byte{0xff}, 30), t12[100:]),
		"wrong magic":     slices.Concat([]byte{0x37}, t12[1:]),
		"id 0":            slices.Concat(t12[:4], []byte{0, 0, 0, 0}, t12[8:]),
		"bigrep":          bigRepDict(t, 20000, 15000),
		"zero rep helper": zeroRep(t12),
	}
	{
		// The offset table at log 9 (> OffFSELog), else train-12k.dict.
		var hdr [8 + 1000]byte
		var dt huf.DTable
		dt.Init(hufDTableCapacityLog)
		hSize, err := huf.ReadDTableX2(&dt, t12[8:], 0)
		if err != nil {
			t.Fatal(err)
		}
		huffEnd := 8 + hSize
		maxSV := uint(maxOff)
		_, ofSize, err := fse.ReadNCount(make([]int16, maxOff+1), &maxSV, t12[huffEnd:])
		if err != nil {
			t.Fatal(err)
		}
		norm := []int16{256, 256}
		n, err := fse.WriteNCount(hdr[:], norm, 1, 9)
		if err != nil {
			t.Fatal(err)
		}
		cases["of log 9"] = slices.Concat(t12[:huffEnd], hdr[:n], t12[huffEnd+ofSize:])
		n, err = fse.WriteNCount(hdr[:], []int16{128, 128}, 1, 8)
		if err != nil {
			t.Fatal(err)
		}
		cases["of log 8"] = slices.Concat(t12[:huffEnd], hdr[:n], t12[huffEnd+ofSize:])
	}
	for i := hdr - 13; i <= hdr+1; i++ {
		cases["cut"+strconv.Itoa(i)] = t12[:i]
	}
	for i := 0; i <= 140; i += 7 {
		cases["cut"+strconv.Itoa(i)] = t12[:i]
	}
	for name, dict := range cases {
		t.Run(name, func(t *testing.T) { checkDictInfo(t, dict, true) })
	}
}

// checkDictInfo compares createDDict and InspectDictionary on dict with
// `cref dictinfo`: acceptance and ID (createDDict, against
// ZSTD_createDDict_advanced), and the error or ID, ContentSize, Content
// and Offsets (InspectDictionary, against ZSTD_loadDEntropy). wantLit
// requires a LitEncoder.
func checkDictInfo(t *testing.T, dict []byte, wantLit bool) {
	t.Helper()
	c := creftest.DictInfoCRef(t, dict)
	dd, err := createDDict(dict, false)
	if (err == nil) != (c["ddict"] == "ok") {
		t.Fatalf("createDDict err %v, C ddict=%s", err, c["ddict"])
	}
	if err == nil && strconv.FormatUint(uint64(dd.dictID), 10) != c["ddictID"] {
		t.Fatalf("dictID %d, C %s", dd.dictID, c["ddictID"])
	}
	if err == nil && !bytes.Equal(dd.content, dict) {
		t.Fatal("ddict content is not the whole dictionary")
	}
	info, err := InspectDictionary(dict)
	eSize, ok := c["entropy"]
	if !ok || strings.HasPrefix(eSize, "err:") {
		if err == nil {
			t.Fatalf("InspectDictionary: no error, C entropy=%q", eSize)
		}
		if errors.Is(err, errDictionaryWrong) != !ok {
			t.Fatalf("InspectDictionary: %v (C entropy=%q)", err, eSize)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(eSize)
	if got := strconv.FormatUint(uint64(info.ID()), 10); got != c["dictID"] {
		t.Errorf("ID %s, C %s", got, c["dictID"])
	}
	if got := strconv.Itoa(info.ContentSize()); got != c["contentSize"] {
		t.Errorf("ContentSize %s, C %s", got, c["contentSize"])
	}
	if !bytes.Equal(info.Content(), dict[n:]) {
		t.Error("Content differs")
	}
	o := info.Offsets()
	if got := fmt.Sprintf("%d,%d,%d", o[0], o[1], o[2]); got != c["rep"] {
		t.Errorf("Offsets %s, C %s", got, c["rep"])
	}
	if wantLit && info.LitEncoder() == nil {
		t.Error("LitEncoder is nil")
	}
}

// FuzzInspectDictionary: InspectDictionary and createDDict never panic,
// and agree with C (checkDictInfo; LitEncoder may be nil).
func FuzzInspectDictionary(f *testing.F) {
	t12 := readTestdata(f, "train-12k.dict")
	f.Add(t12)
	f.Add(t12[:140])
	f.Add(t12[:133])
	f.Add(readTestdata(f, "train.dict")[:400])
	f.Add([]byte("raw content"))
	f.Fuzz(func(t *testing.T, dict []byte) { checkDictInfo(t, dict, false) })
}

// TestDDictEntropyState checks the context state a dictionary sets up
// (C's ZSTD_copyDDictParameters): a zstd dictionary's tables are used in
// place (hufPtr and the sequence table pointers point into the ddict,
// which decoding never writes), a raw one's are not; checkContinuity makes
// the whole dictionary the extDict with virtualStart = prefixStart -
// len(extDict); the dictionary is cold until a frame used it (C's
// ddictIsCold). insertDictionary (ZSTD_decompress_insertDictionary) loads
// the same tables and references the content after them.
func TestDDictEntropyState(t *testing.T) {
	train := readTestdata(t, "train.dict")
	scratch := slices.Clone(train)
	dd := mustDDict(t, scratch, false)
	clear(scratch)
	if !bytes.Equal(dd.content, train) {
		t.Fatal("the ddict does not keep its own copy of the dictionary")
	}
	raw := mustDDict(t, readTestdata(t, "dickens-32k.raw"), true)
	if !dd.entropyPresent || raw.entropyPresent || raw.dictID != 0 {
		t.Fatalf("entropyPresent %v/%v, raw dictID %d", dd.entropyPresent, raw.entropyPresent, raw.dictID)
	}
	// A C frame whose first literals reuse the dictionary's Huffman table.
	var src, frame []byte
search:
	for _, n := range []int{4096, 65536, 100} {
		for _, lv := range []string{"1", "3", "7", "11"} {
			src = dictInputsText(t, n)
			frame = creftest.Run(t, src, append([]string{"compress", "-level", lv}, decDict{"train.dict", false}.crefArgs(t)...)...)
			if firstLitType(t, frame) == int(setRepeat) {
				t.Logf("text%d L%s", n, lv)
				break search
			}
			frame = nil
		}
	}
	if frame == nil {
		t.Fatal("no frame's first literals reuse the dictionary's table")
	}
	saved := dd.entropy

	d := newDCtx()
	d.decompressBeginUsingDDict(dd)
	if !d.ddictIsCold {
		t.Error("first use: not cold")
	}
	if d.hufPtr != &dd.entropy.hufTable || &d.llTptr[0] != &dd.entropy.llTable[0] ||
		&d.ofTptr[0] != &dd.entropy.ofTable[0] || &d.mlTptr[0] != &dd.entropy.mlTable[0] {
		t.Error("tables not taken from the ddict")
	}
	if !d.litEntropy || !d.fseEntropy || d.entropy.rep != dd.entropy.rep || d.dictID != dd.dictID {
		t.Error("entropy flags, repcodes or dictID not copied")
	}
	buf := make([]byte, 0, 10)
	d.checkContinuity(buf, len(src))
	if len(d.extDict) != len(train) || &d.extDict[0] != &dd.content[0] {
		t.Fatalf("extDict %d bytes, want the whole dictionary (%d)", len(d.extDict), len(train))
	}
	if d.prefixStart != 0 || d.virtualStart != d.prefixStart-len(d.extDict) {
		t.Fatalf("prefixStart %d virtualStart %d, extDict %d", d.prefixStart, d.virtualStart, len(d.extDict))
	}
	out, _, e := d.decompressFrame(buf, len(src), frame)
	if e != 0 || !bytes.Equal(out, src) {
		t.Fatalf("decode: %d", e)
	}
	if d.virtualStart != d.prefixStart-len(d.extDict) {
		t.Fatalf("after the frame: prefixStart %d virtualStart %d, extDict %d", d.prefixStart, d.virtualStart, len(d.extDict))
	}
	if dd.entropy != saved {
		t.Fatal("decoding wrote into the ddict's tables")
	}
	d.decompressBeginUsingDDict(dd)
	if d.ddictIsCold {
		t.Error("second use: still cold")
	}
	// Streaming C moves the output between blocks: checkContinuity makes
	// the previous output the extDict, so the next frame finds the
	// dictionary cold again.
	d.checkContinuity(make([]byte, 0, 10), len(src))
	d.checkContinuity(make([]byte, 0, 10), 1)
	d.decompressBeginUsingDDict(dd)
	if !d.ddictIsCold {
		t.Error("after other output: not cold")
	}
	// Repcodes other than the defaults.
	t12 := readTestdata(t, "train-12k.dict")
	at := len(t12) - len(dictContent(t, t12, false)) - 12
	custom := slices.Clone(t12)
	binary.LittleEndian.PutUint32(custom[at:], 7)
	binary.LittleEndian.PutUint32(custom[at+4:], 100)
	binary.LittleEndian.PutUint32(custom[at+8:], 9000)
	d.decompressBeginUsingDDict(mustDDict(t, custom, false))
	if d.entropy.rep != [3]uint32{7, 100, 9000} {
		t.Errorf("repcodes %v, want the dictionary's", d.entropy.rep)
	}
	d.decompressBeginUsingDDict(raw)
	if !d.ddictIsCold || d.litEntropy || d.fseEntropy || d.hufPtr != &d.entropy.hufTable || d.entropy.rep != repStartValue {
		t.Error("raw dictionary: cold, no entropy, own tables, default repcodes expected")
	}

	// insertDictionary.
	d.decompressBegin()
	if e := d.insertDictionary(train); e != 0 {
		t.Fatal(e)
	}
	content := dictContent(t, train, false)
	if d.dictID != dd.dictID || d.entropy.rep != dd.entropy.rep || d.entropy.llTable != dd.entropy.llTable ||
		d.entropy.ofTable != dd.entropy.ofTable || d.entropy.mlTable != dd.entropy.mlTable ||
		d.entropy.hufTable != dd.entropy.hufTable || !d.litEntropy || !d.fseEntropy {
		t.Fatal("insertDictionary: state differs from the ddict's")
	}
	d.checkContinuity(make([]byte, 0, 1), len(src))
	if len(d.extDict) != len(content) || &d.extDict[0] != &content[0] {
		t.Fatalf("insertDictionary: extDict %d bytes, want the content (%d)", len(d.extDict), len(content))
	}
	if e := d.insertDictionary(train[:100]); e != dErrDictionaryCorrupted {
		t.Fatalf("insertDictionary(cut): %d", e)
	}
}

// FuzzDecodeDict decodes frame with dict loaded as ZSTD_dct_auto: no
// panic. createDDict accepts exactly the dictionaries C's
// ZSTD_createDDict_advanced does; when both load it, C (cref decompress)
// and Go agree: equal output when both succeed, Go fails when C does, and
// Go succeeds when C does except for the error mapping table's deviations
// (window, block over blockSizeMax, maxMemory). Each input
// runs cref twice: fuzz with a short -fuzzminimizetime (e.g. 2s), or
// minimizing a large input looks like a hang.
func FuzzDecodeDict(f *testing.F) {
	t12 := readTestdata(f, "train-12k.dict")
	raw := readTestdata(f, "dickens-32k.raw")[:2000]
	text := malText(3000)
	for _, s := range []struct {
		dict []byte
		raw  bool
	}{{t12, false}, {raw, true}} {
		for _, lv := range []EncoderLevel{SpeedFastest, SpeedBetterCompression} {
			opt := WithEncoderDict(s.dict)
			if s.raw {
				opt = WithEncoderDictRaw(0, s.dict)
			}
			e := newTestEncoder(f, WithEncoderLevel(lv), opt)
			f.Add(s.dict, e.EncodeAll(text, nil))
			f.Add(s.dict, e.EncodeAll(slices.Concat(s.dict[len(s.dict)-300:], text[:500]), nil))
		}
	}
	f.Add(t12[:200], []byte{})
	dir := f.TempDir()
	d := newDCtx()
	d.maxMemory = 1 << 26
	f.Fuzz(func(t *testing.T, dict, frame []byte) {
		dd, err := createDDict(dict, false)
		c := creftest.DictInfoCRef(t, dict)
		if (err == nil) != (c["ddict"] == "ok") {
			t.Fatalf("createDDict err %v, C ddict=%s", err, c["ddict"])
		}
		if err != nil {
			return
		}
		got, ge := d.decompressFrames(nil, frame, dd)
		if b := decompressBound(frame); b == contentSizeError || b > 1<<26 {
			return // C's buffer would be too large
		}
		p := filepath.Join(dir, "dict")
		if err := os.WriteFile(p, dict, 0o644); err != nil {
			t.Fatal(err)
		}
		want, ce := creftest.DecodeCRef(t, frame, "-dict", p)
		switch {
		case ce == nil && ge == 0:
			if !bytes.Equal(got, want) {
				t.Fatalf("output differs from C (%d bytes, C %d)", len(got), len(want))
			}
		case ce != nil && ge == 0:
			t.Fatalf("C fails (%v), Go decodes %d bytes", ce, len(got))
		case ce == nil:
			// Go fails where C decodes: only the deviations of the error
			// mapping table (docs/superpowers/phase2-api-decisions.md).
			switch ge {
			case dErrFrameParameterWindowTooLarge: // window > 1<<29: one-shot C does not check it
			case dErrDecodedBlockTooLarge: // a block regenerating > blockSizeMax: one-shot C accepts it
			// Not dErrDecoderSizeExceeded: past the bound gate above,
			// every frame's output space is within maxMemory (1<<26).
			default:
				t.Fatalf("C decodes %d bytes, Go fails: %v", len(want), ge.toError("decompress"))
			}
		}
	})
}
