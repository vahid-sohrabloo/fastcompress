package guardmem

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"testing"
	"unsafe"
)

const crashEnv = "GUARDMEM_CRASH"

// TestGuardMemCrash runs only as TestGuardMem's child process. It touches
// the last (After) or first (Before) byte of a guarded slice, prints a
// marker, then reads one byte beyond it, which must fault.
func TestGuardMemCrash(t *testing.T) {
	mode := os.Getenv(crashEnv)
	if mode == "" {
		t.Skip("child process of TestGuardMem only")
	}
	for _, n := range []int{1, 4095, 4096, 4097, 100000} {
		a, b := After(t, n), Before(t, n)
		if len(a) != n || cap(a) != n || len(b) != n || cap(b) != n {
			t.Fatalf("n=%d: len/cap After %d/%d, Before %d/%d", n, len(a), cap(a), len(b), cap(b))
		}
		a[0], a[n-1], b[0], b[n-1] = 1, 2, 3, 4
	}
	const n = 100
	var p unsafe.Pointer
	switch mode {
	case "after":
		b := After(t, n)
		b[n-1] = 1
		p = unsafe.Add(unsafe.Pointer(unsafe.SliceData(b)), n)
	case "before":
		b := Before(t, n)
		b[0] = 1
		p = unsafe.Add(unsafe.Pointer(unsafe.SliceData(b)), -1)
	default:
		t.Fatalf("bad %s=%q", crashEnv, mode)
	}
	os.Stdout.WriteString("in-bounds-ok\n")
	v := *(*byte)(p)
	t.Fatalf("read past the guard page returned %d without faulting", v)
}

func TestGuardMem(t *testing.T) {
	if !Real() {
		t.Skip("no guard pages on this platform")
	}
	for _, mode := range []string{"after", "before"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestGuardMemCrash$", "-test.v")
			// GOTRACEBACK=single: an inherited "none" would hide the signal report.
			cmd.Env = append(os.Environ(), crashEnv+"="+mode, "GOTRACEBACK=single")
			out, err := cmd.CombinedOutput()
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("child did not fail (err %v):\n%s", err, out)
			}
			if !bytes.Contains(out, []byte("in-bounds-ok")) {
				t.Fatalf("child failed before the out-of-bounds read:\n%s", out)
			}
			// The runtime's fatal-signal report names the signal; darwin
			// reports a PROT_NONE access as SIGBUS.
			if !bytes.Contains(out, []byte("signal SIGSEGV")) && !bytes.Contains(out, []byte("signal SIGBUS")) {
				t.Fatalf("child did not die from a memory fault (%v):\n%s", err, out)
			}
		})
	}
}
