package huf

import (
	"errors"
	"testing"
)

// TestHUFDCtxWkspHeaderErrors drives the DCtx_wksp wrappers (table read +
// decode) through their two early exits: a table description that does not
// parse, and one that fills the whole input so no stream follows it (C:
// HUF_decompress{1,4}X{1,2}_DCtx_wksp return srcSize_wrong there).
func TestHUFDCtxWkspHeaderErrors(t *testing.T) {
	desc := []byte{128 + 11, 0xA9, 0x87, 0x65, 0x43, 0x21, 0x10}
	wrappers := []struct {
		name string
		fn   func(dt *DTable, dst, src []byte, flags int) error
	}{
		{"1X1", Decompress1X1DCtxWksp},
		{"4X1", decompress4X1DCtxWksp},
		{"1X2", decompress1X2DCtxWksp},
		{"4X2", decompress4X2DCtxWksp},
	}
	for _, w := range wrappers {
		t.Run(w.name, func(t *testing.T) {
			var dt DTable
			dt.Init(TableLogMax)
			dst := make([]byte, 64)
			if err := w.fn(&dt, dst, nil, 0); err == nil {
				t.Fatal("empty input: want a table error, got nil")
			}
			dt.Init(TableLogMax)
			if err := w.fn(&dt, dst, desc, 0); !errors.Is(err, ErrSrcSizeWrong) {
				t.Fatalf("description only: got %v, want ErrSrcSizeWrong", err)
			}
		})
	}
}
