package main

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func profile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "coverage.out")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	return p
}

// TestParseCountsStatementsNotLines is the reason this program exists rather
// than a shell one-liner. A profile has one line per function block, so the
// total has to be a ratio of statements, not a count of lines. Counting lines
// lets a long untested function hide behind a short tested one.
func TestParseCountsStatementsNotLines(t *testing.T) {
	p := profile(t, `mode: set
a.go:1.1,2.2 10 10
b.go:3.1,4.2 30 0
`)
	pct, covered, total, err := Parse(p)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if total != 40 {
		t.Errorf("total = %d, want 40", total)
	}
	if covered != 10 {
		t.Errorf("covered = %d, want 10", covered)
	}
	if pct != 25 {
		t.Errorf("pct = %v, want 25", pct)
	}
}

// TestParseTreatsAnyPositiveCountAsCovered checks the partial case. A block with
// some statements hit is counted whole, which matches how the profile is
// defined rather than something this tool should try to second guess.
func TestParseTreatsAnyPositiveCountAsCovered(t *testing.T) {
	pct, _, _, err := Parse(profile(t, "mode: set\na.go:1.1,2.2 4 1\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if pct != 100 {
		t.Errorf("pct = %v, want 100", pct)
	}
}

// TestParseRejectsAnEmptyProfile stops an empty profile from reading as
// perfect coverage. That would let the gate pass on a run that tested nothing.
func TestParseRejectsAnEmptyProfile(t *testing.T) {
	if _, _, _, err := Parse(profile(t, "mode: set\n")); err == nil {
		t.Error("Parse accepted a profile with no statements")
	}
	if _, _, _, err := Parse(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("Parse accepted a missing profile")
	}
}

// TestParseRejectsMalformedRows is the corrected behaviour, and the reason for
// it is worth stating. Skipping a row it cannot read drops those statements from
// the denominator, so a profile that is mostly unreadable reports a high
// percentage and passes. A gate that passes what it cannot read is worse than no
// gate at all, because it looks like it is working.
func TestParseRejectsMalformedRows(t *testing.T) {
	for name, body := range map[string]string{
		"not three fields":  "mode: set\ngarbage\n",
		"non numeric stmts": "mode: set\na.go:1.1,2.2 notanumber 1\n",
		"non numeric count": "mode: set\na.go:1.1,2.2 5 wat\n",
		"negative counts":   "mode: set\na.go:1.1,2.2 -5 0\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := Parse(profile(t, body)); err == nil {
				t.Error("Parse accepted a malformed profile")
			}
		})
	}
}

// TestParseRejectsAMalformedRowEvenWhenOthersAreCovered is the exact attack. One
// covered row plus one unreadable row must not read as full coverage.
func TestParseRejectsAMalformedRowEvenWhenOthersAreCovered(t *testing.T) {
	p := profile(t, "mode: set\na.go:1.1,2.2 10 10\ngarbage\n")
	pct, _, _, err := Parse(p)
	if err == nil {
		t.Fatalf("Parse returned %.1f%% from a profile containing a malformed row", pct)
	}
}

// TestCheckRejectsNonFiniteThresholds covers the case where every comparison
// silently succeeds. NaN <= 0 is false and pct < NaN is false, so a NaN
// threshold would pass any coverage at all while looking properly configured.
func TestCheckRejectsNonFiniteThresholds(t *testing.T) {
	p := profile(t, "mode: set\na.go:1.1,2.2 10 0\n")
	for _, th := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		var out, errOut bytes.Buffer
		if code, err := Check(p, th, &out, &errOut); code != 2 || err == nil {
			t.Errorf("Check(threshold=%v) = %d, %v; want 2, error", th, code, err)
		}
	}
}

// TestParseHandlesLongLines covers a generated file whose block spans more
// columns than the default scanner buffer assumes. A deep path is enough to
// overflow it, and generated packages really do produce those.
func TestParseHandlesLongLines(t *testing.T) {
	deep := strings.Repeat("nested/", 1500) + "generated.go"
	if len(deep) < 9000 {
		t.Fatalf("test path is only %d bytes, too short to exercise the buffer", len(deep))
	}
	pct, _, total, err := Parse(profile(t, "mode: set\n"+deep+":1.1,2.2 2 2\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2", total)
	}
	if pct != 100 {
		t.Errorf("pct = %v, want 100", pct)
	}
}

// TestCheckPassesAboveThreshold is the happy path of the gate. A gate that
// cannot pass is worse than no gate, because it gets disabled.
func TestCheckPassesAboveThreshold(t *testing.T) {
	p := profile(t, "mode: set\na.go:1.1,2.2 10 10\n")
	var out, errOut bytes.Buffer
	code, err := Check(p, 80, &out, &errOut)
	if err != nil || code != 0 {
		t.Fatalf("Check = %d, %v; want 0, nil", code, err)
	}
	if !strings.Contains(out.String(), "100.0%") {
		t.Errorf("output %q does not report the percentage", out.String())
	}
}

// TestCheckFailsBelowThreshold is the whole reason the tool is here.
func TestCheckFailsBelowThreshold(t *testing.T) {
	p := profile(t, "mode: set\na.go:1.1,2.2 10 1\nb.go:1.1,2.2 10 0\n")
	var out, errOut bytes.Buffer
	code, err := Check(p, 80, &out, &errOut)
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if err == nil {
		t.Error("Check returned no error below the threshold")
	}
	if !strings.Contains(errOut.String(), "below") {
		t.Errorf("stderr %q does not explain the failure", errOut.String())
	}
}

// TestCheckRejectsAnUnusableThreshold guards against a zero threshold silently
// passing everything, which is how a coverage gate quietly stops working.
func TestCheckRejectsAnUnusableThreshold(t *testing.T) {
	var out, errOut bytes.Buffer
	for _, th := range []float64{0, -1} {
		if code, err := Check("coverage.out", th, &out, &errOut); code != 2 || err == nil {
			t.Errorf("Check(threshold=%v) = %d, %v; want 2, error", th, code, err)
		}
	}
}

// TestCheckReportsAnUnreadableProfile separates a broken run from a low score,
// so a failing CI job says which of the two happened.
func TestCheckReportsAnUnreadableProfile(t *testing.T) {
	var out, errOut bytes.Buffer
	if code, err := Check(filepath.Join(t.TempDir(), "absent"), 80, &out, &errOut); code != 2 || err == nil {
		t.Errorf("Check = %d, %v; want 2, error", code, err)
	}
}

// TestCheckBoundaryIsInclusive pins the exact edge. A gate that rejects exactly
// 80% would surprise everyone.
func TestCheckBoundaryIsInclusive(t *testing.T) {
	p := profile(t, "mode: set\na.go:1.1,2.2 40 1\nb.go:1.1,2.2 10 0\n")
	var out, errOut bytes.Buffer
	if code, err := Check(p, 80, &out, &errOut); code != 0 || err != nil {
		t.Errorf("Check at exactly the threshold = %d, %v; want 0, nil", code, err)
	}
}

// TestResolvePositionalRules pins every accepted invocation, because this logic
// was wrong twice: the documented order is profile then threshold and it was
// read the other way round, then a flag was compared against its default string
// so "-profile coverage.out 80" took the 80 as the path. Neither mistake throws
// an error, it just quietly does the wrong thing.
func TestResolvePositionalRules(t *testing.T) {
	for _, tc := range []struct {
		name         string
		profile      string
		threshold    float64
		profileGiven bool
		args         []string
		wantProfile  string
		wantThresh   float64
		wantErr      bool
	}{
		{"flags only", "coverage.out", 80, true, nil, "coverage.out", 80, false},
		{"profile then threshold", "coverage.out", 0, false, []string{"other.out", "80"}, "other.out", 80, false},
		{"threshold only", "coverage.out", 0, false, []string{"80"}, "coverage.out", 80, false},
		{"profile only, threshold from flag", "coverage.out", 90, false, []string{"other.out"}, "other.out", 90, false},
		{"profile by flag, threshold by argument", "a.out", 0, true, []string{"80"}, "a.out", 80, false},
		{"nothing at all", "coverage.out", 0, false, nil, "coverage.out", 0, false},

		{"profile given twice", "a.out", 0, true, []string{"b.out", "80"}, "", 0, true},
		{"non numeric threshold", "coverage.out", 0, false, []string{"a.out", "wat"}, "", 0, true},
		{"non numeric after profile flag", "a.out", 0, true, []string{"wat"}, "", 0, true},
		{"too many arguments", "coverage.out", 0, false, []string{"a", "b", "c"}, "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prof, thr, err := resolve(tc.profile, tc.threshold, tc.profileGiven, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolve(%v) = %q, %v; want an error", tc.args, prof, thr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve(%v): %v", tc.args, err)
			}
			if prof != tc.wantProfile || thr != tc.wantThresh {
				t.Errorf("resolve(%v) = %q, %v; want %q, %v", tc.args, prof, thr, tc.wantProfile, tc.wantThresh)
			}
		})
	}
}
