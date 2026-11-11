package main

import "testing"

func TestRegionTable(t *testing.T) {
	cases := []struct {
		name string
		m    Metrics
		want string
	}{
		{"A at its edge", Metrics{3, 3, 2, 2, 0}, "A"},
		{"A needs D", Metrics{3, 3, 1, 2, 0}, "C"},
		{"A needs P_D", Metrics{3, 3, 2, 1, 0}, "C"},
		{"A needs N", Metrics{2, 3, 2, 2, 2}, "B"},
		{"A needs N, no S", Metrics{2, 3, 2, 2, 1}, "E"},
		{"A needs P", Metrics{3, 2, 2, 2, 0}, "E"},
		{"B at its edge", Metrics{2, 2, 0, 0, 2}, "B"},
		{"B needs S", Metrics{2, 2, 2, 2, 1}, "E"},
		{"B needs N", Metrics{1, 2, 1, 1, 2}, "E"},
		{"B needs P", Metrics{2, 1, 1, 1, 2}, "E"},
		{"B beats C", Metrics{3, 3, 1, 1, 2}, "B"},
		{"A beats B", Metrics{3, 3, 2, 2, 3}, "A"},
		{"C at its edge", Metrics{3, 3, 0, 0, 0}, "C"},
		{"C needs N", Metrics{2, 3, 0, 0, 1}, "E"},
		{"C needs P", Metrics{3, 2, 0, 0, 1}, "E"},
		{"E empty", Metrics{}, "E"},
		{"E N=1", Metrics{1, 5, 1, 1, 1}, "E"},
		{"E P=1", Metrics{5, 1, 5, 1, 5}, "E"},
		{"E N=2 with S<=1", Metrics{2, 2, 2, 2, 1}, "E"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Region(tc.m).Region; got != tc.want {
				t.Fatalf("%v: region %s, want %s", tc.m, got, tc.want)
			}
			// the trace agrees: the first matched row is the region
			for _, tr := range Trace(tc.m) {
				if tr.Matched {
					if tr.Region != tc.want {
						t.Fatalf("trace first match %s, want %s", tr.Region, tc.want)
					}
					break
				}
			}
		})
	}
}

func TestTraceNamesFailedClauses(t *testing.T) {
	tr := Trace(Metrics{3, 3, 1, 2, 0})
	if tr[0].Matched || len(tr[0].Failed) != 1 || tr[0].Failed[0] != "D >= 2" {
		t.Fatalf("row A trace %+v", tr[0])
	}
	if tr[1].Matched || tr[2].Matched != true {
		t.Fatalf("B/C trace %+v %+v", tr[1], tr[2])
	}
}

// Adding a counted finding never lowers N, P, D or P_D (S can fall when the
// new finding is not flagged). A never moves to a weaker region; E is the only
// region that is left when the first four stay put and S is ignored.
func TestMonotone(t *testing.T) {
	rank := map[string]int{"E": 0, "C": 1, "B": 1, "A": 2}
	var grid []Metrics
	for n := 0; n <= 4; n++ {
		for p := 0; p <= 4; p++ {
			for d := 0; d <= n; d++ {
				for pd := 0; pd <= p; pd++ {
					for s := 0; s <= n; s++ {
						grid = append(grid, Metrics{n, p, d, pd, s})
					}
				}
			}
		}
	}
	for _, m := range grid {
		base := Region(m).Region
		// one more key, from a new publisher, defect, flagged
		up := []Metrics{
			{m.N + 1, m.P, m.D, m.PD, m.S},
			{m.N + 1, m.P + 1, m.D, m.PD, m.S},
			{m.N + 1, m.P + 1, m.D + 1, m.PD + 1, m.S},
			{m.N + 1, m.P, m.D + 1, m.PD, m.S + 1},
			{m.N, m.P + 1, m.D, m.PD, m.S},
			{m.N + 1, m.P + 1, m.D + 1, m.PD + 1, m.S + 1},
		}
		for _, u := range up {
			after := Region(u).Region
			if base == "A" && after != "A" {
				t.Fatalf("%v is A, %v is %s", m, u, after)
			}
			if rank[after] < rank[base] {
				t.Fatalf("%v is %s, %v is %s (weaker)", m, base, u, after)
			}
		}
		// S dropping to a lower value (an unflagged finding) never leaves A
		if base == "A" && Region(Metrics{m.N + 1, m.P, m.D, m.PD, 0}).Region != "A" {
			t.Fatalf("%v: an unflagged extra finding left A", m)
		}
	}
}

func TestReachable(t *testing.T) {
	cases := []struct {
		name string
		b    Bounds
		want string
	}{
		{"nothing pending, nothing counted", Bounds{}, "E"},
		{"N_max 1 is only E", Bounds{NMax: 1, PMax: 5, DMax: 1, PDMax: 5, SMax: 1}, "E"},
		{"P_max 1 is only E", Bounds{NMax: 5, PMax: 1, DMax: 5, PDMax: 1, SMax: 5}, "E"},
		{"N_max 2 P_max 2 reaches B and E", Bounds{NMax: 2, PMax: 2, DMax: 2, PDMax: 2, SMax: 2}, "B,E"},
		{"3 and 3 reaches all", Bounds{NMax: 3, PMax: 3, DMax: 3, PDMax: 3, SMax: 3}, "A,B,C,E"},
		{"3 and 3 but no defects pending reaches B C E", Bounds{NMax: 3, PMax: 3, DMax: 0, PDMax: 0, SMax: 3}, "B,C,E"},
		{"A reached already so C and B cannot be", Bounds{Cur: Metrics{3, 3, 2, 2, 0}, NMax: 3, PMax: 3, DMax: 2, PDMax: 2, SMax: 3}, "A"},
		{"S zero and no more cells: C only", Bounds{Cur: Metrics{3, 3, 0, 0, 0}, NMax: 3, PMax: 3, DMax: 0, PDMax: 0, SMax: 0}, "C"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ""
			for i, r := range Reachable(tc.b) {
				if i > 0 {
					got += ","
				}
				got += r
			}
			if got != tc.want {
				t.Fatalf("reachable %s, want %s", got, tc.want)
			}
		})
	}
	// section 7: N_max <= 1 or P_max <= 1 means only region E, for any current values
	for n := 0; n <= 1; n++ {
		for p := 0; p <= 6; p++ {
			for _, b := range []Bounds{
				{Cur: Metrics{N: n}, NMax: n, PMax: p, DMax: 9, PDMax: 9, SMax: 9},
				{Cur: Metrics{P: n}, NMax: p, PMax: n, DMax: 9, PDMax: 9, SMax: 9},
			} {
				if r := Reachable(b); len(r) != 1 || r[0] != "E" {
					t.Fatalf("%+v reaches %v", b, r)
				}
			}
		}
	}
}
