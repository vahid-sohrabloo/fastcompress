package main

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Gate thresholds from the plan (section 4): per level and direction.
const (
	gateGeomean = 100.0
	gateRowMin  = 95.0
	gateFloor   = 90.0
)

// Median of xs (the lower middle for even counts, like compare.sh).
func Median(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[(len(s)+1)/2-1]
}

func Geomean(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	var s float64
	for _, x := range xs {
		s += math.Log(x)
	}
	return math.Exp(s / float64(len(xs)))
}

// Row statuses. A "mismatch" row (ours and libdeflate output sizes differ) or
// a "missing" row (no usable measurement) never counts as a pass.
const (
	StatusOK       = "ok"
	StatusMismatch = "mismatch"
	StatusMissing  = "missing"
)

// Row is one "% of C" sample of a summary: mode "c" or "d", the level, a label
// ("file/block"), the percentage and a status.
type Row struct {
	Mode   string
	Level  string
	Label  string
	Pct    float64
	Status string
}

func (r Row) valid() bool {
	return r.Status == StatusOK && r.Pct > 0 && !math.IsNaN(r.Pct) && !math.IsInf(r.Pct, 0)
}

// GateResult is the verdict for one (mode, level) group.
type GateResult struct {
	Mode, Level string
	N           int
	Geomean     float64 // over valid rows only; NaN if none
	MinLabel    string
	Min         float64
	Below95     []Row // valid rows under gateRowMin
	Below90     []Row // valid rows under the hard floor
	Mismatch    []Row
	Missing     []Row
	Pass        bool
}

// Gate groups rows by mode then level (first-seen order) and applies the
// gate: geomean >= 100, every row >= 95; any row < 90 is the hard floor.
// Mismatching, missing or non-positive/NaN rows are excluded from the geomean
// and fail the level.
func Gate(rows []Row) []GateResult {
	type key struct{ mode, level string }
	var order []key
	groups := map[key][]Row{}
	for _, r := range rows {
		k := key{r.Mode, r.Level}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	var out []GateResult
	for _, k := range order {
		g := groups[k]
		res := GateResult{Mode: k.mode, Level: k.level, N: len(g), Min: math.Inf(1)}
		var pcts []float64
		for _, r := range g {
			switch {
			case r.Status == StatusMismatch:
				res.Mismatch = append(res.Mismatch, r)
				continue
			case !r.valid():
				res.Missing = append(res.Missing, r)
				continue
			}
			pcts = append(pcts, r.Pct)
			if r.Pct < res.Min {
				res.Min, res.MinLabel = r.Pct, r.Label
			}
			if r.Pct < gateRowMin {
				res.Below95 = append(res.Below95, r)
			}
			if r.Pct < gateFloor {
				res.Below90 = append(res.Below90, r)
			}
		}
		res.Geomean = Geomean(pcts)
		// 1e-9 slack: exp(log(100)) is not exactly 100.
		res.Pass = len(pcts) > 0 && res.Geomean >= gateGeomean-1e-9 &&
			len(res.Below95) == 0 && len(res.Mismatch) == 0 && len(res.Missing) == 0
		out = append(out, res)
	}
	return out
}

// withExpectedLevels returns the results ordered like levels, with an empty
// (N == 0, NaN geomean, failing) result for each expected level that has no
// rows; levels seen but not expected follow in their original order.
func withExpectedLevels(gs []GateResult, mode string, levels []string) []GateResult {
	if len(levels) == 0 {
		return gs
	}
	by := map[string]GateResult{}
	for _, g := range gs {
		by[g.Level] = g
	}
	var out []GateResult
	seen := map[string]bool{}
	for _, l := range levels {
		if seen[l] {
			continue
		}
		seen[l] = true
		if g, ok := by[l]; ok {
			out = append(out, g)
		} else {
			out = append(out, GateResult{Mode: mode, Level: l, Geomean: math.NaN(), Min: math.NaN()})
		}
	}
	for _, g := range gs {
		if !seen[g.Level] {
			out = append(out, g)
		}
	}
	return out
}

// ParseRows reads tab-separated "mode level label pct" lines.
func ParseRows(r io.Reader) ([]Row, error) {
	var rows []Row
	sc := bufio.NewScanner(r)
	for ln := 1; sc.Scan(); ln++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 4 && len(f) != 5 {
			return nil, fmt.Errorf("line %d: want 4 or 5 tab-separated fields, got %d", ln, len(f))
		}
		status := StatusOK
		if len(f) == 5 {
			status = f[4]
		}
		if status != StatusOK && status != StatusMismatch && status != StatusMissing {
			return nil, fmt.Errorf("line %d: bad status %q", ln, status)
		}
		if f[0] != "c" && f[0] != "d" {
			return nil, fmt.Errorf("line %d: bad mode %q", ln, f[0])
		}
		p, err := strconv.ParseFloat(strings.TrimSuffix(f[3], "%"), 64)
		if err != nil && f[3] != "-" {
			return nil, fmt.Errorf("line %d: bad percentage %q", ln, f[3])
		}
		if f[3] == "-" {
			p, status = 0, StatusMissing
		}
		// Zero/negative/NaN values are kept: Gate fails them as missing rows.
		rows = append(rows, Row{f[0], f[1], f[2], p, status})
	}
	return rows, sc.Err()
}

// SummaryOpts configures WriteSummary.
type SummaryOpts struct {
	Dry    bool              // dry run: placeholder numbers, structure only, no verdicts
	Info   bool              // noasm runs: numbers reported only, not a gate
	Levels []string          // expected levels (compare.sh -l); missing ones are n/a rows
	Modes  []string          // requested modes ("c", "d"); empty = the modes that have rows
	Why    map[string]string // mode -> reason it produced no rows (NOT EVALUATED wording)
}

var modeNames = []struct{ mode, name, noun string }{{"c", "Compress", "compression"}, {"d", "Decompress", "decompression"}}

// WriteSummary renders the gate summary as markdown, compress then decompress.
// A banner states which gates were evaluated; a requested mode without rows
// gets an explicit NOT EVALUATED block (levels listed as n/a) and the file ends
// with an OVERALL line that never reads as a pass when a gate was not evaluated.
func WriteSummary(w io.Writer, rows []Row, o SummaryOpts) {
	has := map[string]bool{}
	for _, r := range rows {
		has[r.Mode] = true
	}
	req := map[string]bool{}
	for _, m := range o.Modes {
		req[m] = true
	}
	if len(o.Modes) == 0 {
		req = has
	}
	var evaluated, missing, missingNames []string
	for _, m := range modeNames {
		switch {
		case req[m.mode] && has[m.mode]:
			evaluated = append(evaluated, m.name)
		case req[m.mode]:
			missing = append(missing, m.noun)
			missingNames = append(missingNames, strings.ToLower(m.name))
		}
	}
	if o.Dry {
		fmt.Fprint(w, "\n**DRY RUN: placeholder numbers, no verdicts. Structure of the real summary:**\n")
	}
	switch {
	case len(missing) == 0 && len(evaluated) > 0:
		fmt.Fprintf(w, "\n**Gates evaluated: %s.**\n", strings.Join(evaluated, ", "))
	case len(evaluated) == 0 && len(missing) > 0:
		fmt.Fprintf(w, "\n**NO gate was evaluated; there is NO %s result in this file.**\n", strings.Join(missing, " or "))
	case len(missing) > 0:
		fmt.Fprintf(w, "\n**ONLY the %s gate was evaluated; there is NO %s result in this file.**\n", strings.Join(evaluated, " and "), strings.Join(missing, " or "))
	}
	failed := false
	for _, m := range modeNames {
		if !req[m.mode] {
			continue
		}
		var sel []Row
		for _, r := range rows {
			if r.Mode == m.mode {
				sel = append(sel, r)
			}
		}
		if len(sel) == 0 {
			why := o.Why[m.mode]
			if why == "" {
				why = "no ours-vs-C rows"
			}
			if o.Info {
				fmt.Fprintf(w, "\n### %s: NOT EVALUATED (%s)\n\n", m.name, why)
			} else {
				fmt.Fprintf(w, "\n### %s gate: NOT EVALUATED (%s)\n\n", m.name, why)
			}
			fmt.Fprintln(w, "| level | rows | geomean % of C | min row | gate |")
			fmt.Fprintln(w, "|---|---|---|---|---|")
			for _, l := range o.Levels {
				fmt.Fprintf(w, "| %s | 0 | n/a | n/a | NOT EVALUATED |\n", l)
			}
			continue
		}
		if o.Dry {
			fmt.Fprintf(w, "\n### %s gate: dry run, placeholder numbers, no verdict\n", m.name)
			continue
		}
		informational, levels := o.Info, o.Levels
		if informational {
			fmt.Fprintf(w, "\n### %s: REPORTED ONLY (noasm), not a gate\n\n", m.name)
		} else {
			fmt.Fprintf(w, "\n### %s gate (geomean >= %.0f%%, every row >= %.0f%%, hard floor %.0f%%; MISMATCH/missing rows fail)\n\n",
				m.name, gateGeomean, gateRowMin, gateFloor)
		}
		fmt.Fprintln(w, "| level | rows | geomean % of C | min row | gate |")
		fmt.Fprintln(w, "|---|---|---|---|---|")
		var notes []string
		for _, g := range withExpectedLevels(Gate(sel), m.mode, levels) {
			verdict := "PASS"
			switch {
			case informational:
				verdict = "info"
			case g.N == 0:
				verdict = "FAIL (missing rows)"
				notes = append(notes, fmt.Sprintf("level %s: no rows (ours produced no measurement)", g.Level))
			case len(g.Mismatch) > 0:
				verdict = "FAIL (MISMATCH)"
			case len(g.Missing) > 0:
				verdict = "FAIL (missing rows)"
			case len(g.Below90) > 0:
				verdict = "FAIL (below hard floor)"
			case !g.Pass:
				verdict = "FAIL"
			}
			geo, min := "n/a", "n/a"
			if !math.IsNaN(g.Geomean) {
				geo = fmt.Sprintf("%.1f%%", g.Geomean)
				min = fmt.Sprintf("%.1f%% (%s)", g.Min, g.MinLabel)
			}
			failed = failed || strings.HasPrefix(verdict, "FAIL")
			fmt.Fprintf(w, "| %s | %d | %s | %s | %s |\n", g.Level, g.N, geo, min, verdict)
			if !math.IsNaN(g.Geomean) && g.Geomean < gateGeomean-1e-9 {
				notes = append(notes, fmt.Sprintf("level %s: geomean %.1f%% < %.0f%%", g.Level, g.Geomean, gateGeomean))
			}
			for _, r := range g.Mismatch {
				notes = append(notes, fmt.Sprintf("level %s: %s MISMATCH (output size differs from libdeflate; row excluded from the geomean)", g.Level, r.Label))
			}
			for _, r := range g.Missing {
				notes = append(notes, fmt.Sprintf("level %s: %s missing/invalid measurement", g.Level, r.Label))
			}
			for _, r := range g.Below95 {
				tag := "< 95%"
				if r.Pct < gateFloor {
					tag = "< 90% HARD FLOOR"
				}
				notes = append(notes, fmt.Sprintf("level %s: %s %.1f%% %s", g.Level, r.Label, r.Pct, tag))
			}
		}
		if len(notes) > 0 {
			fmt.Fprintln(w, "\nViolations:")
			for _, n := range notes {
				fmt.Fprintf(w, "- %s\n", n)
			}
		}
	}
	var inc string
	if len(missing) > 0 {
		inc = " INCOMPLETE (" + strings.Join(missingNames, ", ") + " not evaluated)"
	}
	switch {
	case o.Dry:
		fmt.Fprintf(w, "\nOVERALL: DRY RUN, no verdict;%s\n", inc)
	case o.Info:
		fmt.Fprintf(w, "\nOVERALL: REPORTED ONLY (noasm), not a gate;%s\n", inc)
	case failed && inc != "":
		fmt.Fprintf(w, "\nOVERALL: FAIL;%s\n", inc)
	case failed:
		fmt.Fprintln(w, "\nOVERALL: FAIL")
	case inc != "":
		fmt.Fprintf(w, "\nOVERALL:%s\n", inc)
	default:
		fmt.Fprintln(w, "\nOVERALL: PASS")
	}
}
