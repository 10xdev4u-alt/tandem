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

	var total, covered int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		// name:file.go,startLine.startCol,endLine.endCol numStmts count
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		stmts, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
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
	if threshold <= 0 {
		fmt.Fprintln(errOut, "error: a threshold above zero is required")
		return 2, errors.New("threshold must be above zero")
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

func main() {
	var (
		profile   = flag.String("profile", "coverage.out", "path to the coverage profile")
		threshold = flag.Float64("threshold", 0, "minimum total coverage percentage")
	)
	flag.Parse()

	// The threshold is also accepted positionally, because the natural way to
	// type this by hand is "coverage.out 80" and having that silently mean
	// "no threshold" is how a gate ends up disabled by accident.
	if *threshold <= 0 && flag.NArg() > 0 {
		v, err := strconv.ParseFloat(flag.Arg(0), 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %q is not a number\n", flag.Arg(0))
			os.Exit(2)
		}
		*threshold = v
	}
	if flag.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "error: expected at most a profile and a threshold")
		os.Exit(2)
	}

	code, _ := Check(*profile, *threshold, os.Stdout, os.Stderr)
	os.Exit(code)
}
