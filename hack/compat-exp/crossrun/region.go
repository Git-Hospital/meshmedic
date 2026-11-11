package main

import "fmt"

// Metrics are the five numbers of section 1.
type Metrics struct {
	N  int `json:"nNew"`
	P  int `json:"pNew"`
	D  int `json:"d"`
	PD int `json:"pD"`
	S  int `json:"s"`
}

// RegionRow is one row of the section 1 decision table.
type RegionRow struct {
	Region    string `json:"region"`
	Condition string `json:"condition"`
	Outcome   string `json:"outcome"`
}

// DecisionTable is section 1's table in order; the first matching row decides.
var DecisionTable = []RegionRow{
	{"A", "N_new >= 3, P_new >= 3, D >= 2, P_D >= 2", "Continue in this repository"},
	{"B", "N_new >= 2, P_new >= 2, S >= 2", "Leave: an istioctl analyze proposal stream"},
	{"C", "N_new >= 3, P_new >= 3", "Leave: an atlas in its own repository"},
	{"E", "anything else", "Drop"},
}

func rowMatches(region string, m Metrics) bool {
	switch region {
	case "A":
		return m.N >= 3 && m.P >= 3 && m.D >= 2 && m.PD >= 2
	case "B":
		return m.N >= 2 && m.P >= 2 && m.S >= 2
	case "C":
		return m.N >= 3 && m.P >= 3
	}
	return true
}

// Region applies the table: the first matching row.
func Region(m Metrics) RegionRow {
	for _, row := range DecisionTable {
		if rowMatches(row.Region, m) {
			return row
		}
	}
	return DecisionTable[len(DecisionTable)-1]
}

// RowTrace says, for each row in order, whether it matched and which clauses
// failed, so a reader sees why a row was skipped.
type RowTrace struct {
	Region  string   `json:"region"`
	Matched bool     `json:"matched"`
	Failed  []string `json:"failed,omitempty"`
}

// Trace evaluates every row; the first with Matched true decides.
func Trace(m Metrics) []RowTrace {
	clauses := map[string][]struct {
		name string
		ok   bool
	}{
		"A": {{"N_new >= 3", m.N >= 3}, {"P_new >= 3", m.P >= 3}, {"D >= 2", m.D >= 2}, {"P_D >= 2", m.PD >= 2}},
		"B": {{"N_new >= 2", m.N >= 2}, {"P_new >= 2", m.P >= 2}, {"S >= 2", m.S >= 2}},
		"C": {{"N_new >= 3", m.N >= 3}, {"P_new >= 3", m.P >= 3}},
	}
	var out []RowTrace
	for _, row := range DecisionTable {
		t := RowTrace{Region: row.Region, Matched: true}
		for _, c := range clauses[row.Region] {
			if !c.ok {
				t.Matched = false
				t.Failed = append(t.Failed, c.name)
			}
		}
		out = append(out, t)
	}
	return out
}

// Bounds are the upper bounds of section 7's futility test, with the current
// values as lower bounds.
type Bounds struct {
	Cur           Metrics
	NMax, PMax    int
	DMax, PDMax   int
	SMax          int
	SLowerIsZero  bool
	PendingCells  int
	PendingPubs   int
	PendingNewPub int
}

func clamp3(x int) int {
	if x > 3 {
		return 3
	}
	return x
}

// Reachable lists the regions some final metrics in the bounds fall into.
// Every threshold is at most 3, so values are clamped to 3, which keeps the
// enumeration exact and small. Constraints: D <= N, P_D <= P, S <= N, every
// metric at least its current value except S, which can fall when a later
// finding of a key is not flagged by its check.
func Reachable(b Bounds) []string {
	got := map[string]bool{}
	for n := clamp3(b.Cur.N); n <= clamp3(b.NMax); n++ {
		for p := clamp3(b.Cur.P); p <= clamp3(b.PMax); p++ {
			for d := clamp3(b.Cur.D); d <= clamp3(minInt(b.DMax, b.NMax)); d++ {
				if d > n {
					continue
				}
				for pd := clamp3(b.Cur.PD); pd <= clamp3(minInt(b.PDMax, b.PMax)); pd++ {
					if pd > p {
						continue
					}
					for s := 0; s <= clamp3(minInt(b.SMax, b.NMax)); s++ {
						if s > n {
							continue
						}
						got[Region(Metrics{n, p, d, pd, s}).Region] = true
					}
				}
			}
		}
	}
	var out []string
	for _, r := range []string{"A", "B", "C", "E"} {
		if got[r] {
			out = append(out, r)
		}
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (m Metrics) String() string {
	return fmt.Sprintf("N_new=%d P_new=%d D=%d P_D=%d S=%d", m.N, m.P, m.D, m.PD, m.S)
}
