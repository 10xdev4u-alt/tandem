// Command coverage-gate fails a build when total coverage is below a threshold.
//
// The threshold is an argument rather than a constant, and the reason to have a
// program at all rather than a line in a workflow file is that a number in a
// YAML string cannot be argued with. When the gate blocks a pull request, the
// conversation should be about the threshold, not about where it was written.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

// Parse reads a Go coverage profile and returns the total percentage of
// statements covered.
//
// A coverage profile has one line per function block, so the total is the ratio
// of covered statements to all statements rather than a count of lines that
// happen to be hit. Counting lines would let a long untested function hide
// behind a short tested one.
func Parse(path string) (float64, int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("opening coverage profile: %w", err)
	}
	defer f.Close()

	var total, covered, lineNo int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		// name:file.go,startLine.startCol,endLine.endCol numStmts count
		//
		// A row that cannot be read is an error rather than a skipped row.
		// Skipping it would quietly drop statements from the denominator, so a
		// profile that is mostly unreadable would report a high percentage and
		// pass. A gate that passes what it cannot read is worse than no gate.
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return 0, 0, 0, fmt.Errorf("line %d: expected 3 fields, got %d: %q", lineNo, len(fields), line)
		}
		stmts, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0, 0, 0, fmt.Errorf("line %d: %q is not a statement count: %w", lineNo, fields[1], err)
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			return 0, 0, 0, fmt.Errorf("line %d: %q is not an execution count: %w", lineNo, fields[2], err)
		}
		if stmts < 0 || count < 0 {
			return 0, 0, 0, fmt.Errorf("line %d: negative counts in %q", lineNo, line)
		}
		total += stmts
		if count > 0 {
			covered += stmts
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, 0, fmt.Errorf("reading coverage profile: %w", err)
	}
	if total == 0 {
		return 0, 0, 0, fmt.Errorf("no statements found in %s", path)
	}
	return float64(covered) / float64(total) * 100, covered, total, nil
}

// Check applies the gate and reports whether the build should pass.
//
// The verdict is returned rather than acted on so the decision is testable.
// This program exists to block pull requests, and the only part of it worth
// trusting is the part that decides.
func Check(profile string, threshold float64, out, errOut io.Writer) (int, error) {
	// NaN fails every comparison it is put through: NaN <= 0 is false and
	// pct < NaN is false, so a NaN threshold would pass everything while looking
	// configured. Inf is the mirror image, rejecting everything. Both are worse
	// than a missing threshold because they are silent.
	if math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold <= 0 {
		fmt.Fprintln(errOut, "error: a finite threshold above zero is required")
		return 2, fmt.Errorf("threshold %v is not usable", threshold)
	}

	pct, covered, total, err := Parse(profile)
	if err != nil {
		fmt.Fprintf(errOut, "error: %v\n", err)
		return 2, err
	}

	fmt.Fprintf(out, "coverage %.1f%% (%d/%d statements), threshold %.1f%%\n",
		pct, covered, total, threshold)
	if pct < threshold {
		fmt.Fprintf(errOut,
			"coverage %.1f%% is below the %.1f%% threshold; add tests rather than lowering the gate\n",
			pct, threshold)
		return 1, fmt.Errorf("coverage %.1f%% below threshold %.1f%%", pct, threshold)
	}
	return 0, nil
}

// resolve applies the positional rules to the flag values.
//
// It is a separate function because these rules are the part of this command
// most likely to be wrong, and the part that was wrong twice already: the
// documented order is profile then threshold, reading it the other way round
// made the documented invocation fail, and comparing a flag against its default
// string made "-profile coverage.out 80" read the 80 as the path. Neither is
// reachable from a test while it lives inside main.
func resolve(profile string, threshold float64, profileGiven bool, args []string) (string, float64, error) {
	if len(args) > 2 {
		return "", 0, errors.New("expected at most a profile and a threshold")
	}

	// A lone numeric argument is a threshold, because a bare number is not a
	// plausible profile path and "gate 80" is what someone means by it.
	if len(args) == 1 && !profileGiven {
		if v, err := strconv.ParseFloat(args[0], 64); err == nil {
			return profile, v, nil
		}
		return args[0], threshold, nil
	}

	if len(args) == 1 {
		v, err := strconv.ParseFloat(args[0], 64)
		if err != nil {
			return "", 0, fmt.Errorf("%q is not a number", args[0])
		}
		return profile, v, nil
	}

	if len(args) == 2 {
		if profileGiven {
			return "", 0, errors.New("the profile was given twice, as a flag and as an argument")
		}
		v, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			return "", 0, fmt.Errorf("%q is not a number", args[1])
		}
		return args[0], v, nil
	}

	return profile, threshold, nil
}

func main() {
	var (
		profile   = flag.String("profile", "coverage.out", "path to the coverage profile")
		threshold = flag.Float64("threshold", 0, "minimum total coverage percentage")
	)
	flag.Parse()

	// A flag that was actually given cannot be told from its default by value,
	// so record it properly. Comparing against the default string made
	// "-profile coverage.out 80" read the 80 as the profile path.
	profileGiven := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "profile" {
			profileGiven = true
		}
	})

	prof, thr, err := resolve(*profile, *threshold, profileGiven, flag.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: "+err.Error())
		os.Exit(2)
	}

	code, _ := Check(prof, thr, os.Stdout, os.Stderr)
	os.Exit(code)
}
