package main

// Measure reproduces ldbench.c / benchfn.c timing: passes are run in windows
// of `loops` passes, loops is re-sized after each window so the next lasts
// about 1 s (x10 after a window under 20 ms), a window's time is the mean per
// pass, windows under 0.5 s are discarded, and the best window is reported.
// It stops once at least secs seconds of windows were timed and at least
// minWindows windows were counted. run(n) executes n passes; clock returns
// seconds. The result is MB/s (1e6 bytes) of bytesPerPass, 0 if no window
// counted.
func Measure(run func(loops int), bytesPerPass float64, secs float64, minWindows int, clock func() float64) float64 {
	best, total, loops, counted := 0.0, 0.0, 1, 0
	for total < secs || counted < minWindows {
		t0 := clock()
		run(loops)
		dt := clock() - t0
		total += dt
		if dt >= 0.5 {
			counted++
			if mbs := bytesPerPass * float64(loops) / dt / 1e6; mbs > best {
				best = mbs
			}
		}
		if dt < 0.02 {
			loops *= 10
		} else {
			loops = int(float64(loops)/dt) + 1
		}
	}
	return best
}
