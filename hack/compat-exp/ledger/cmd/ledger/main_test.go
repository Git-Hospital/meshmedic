package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRun(t *testing.T) {
	causes := filepath.Join("..", "..", "..", "causes.json")
	dir := t.TempDir()
	write := func(name, doc string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good.json", `{"schema":1,"rows":[],"newKeys":[]}`)
	bad := write("bad.json", `{"schema":1,"rows":[{"chart":"a","world":"W1","key":"k","status":"counted"}],"newKeys":[]}`)
	unknown := write("unknown.json", `{"schema":1,"rows":[],"newKeys":[],"x":1}`)
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"valid", []string{"check", "-ledger", good, "-causes", causes}, 0},
		{"invalid row", []string{"check", "-ledger", bad, "-causes", causes}, 1},
		{"unknown field", []string{"check", "-ledger", unknown, "-causes", causes}, 1},
		{"missing causes", []string{"check", "-ledger", good, "-causes", filepath.Join(dir, "none.json")}, 2},
		{"no subcommand", nil, 2},
		{"extra arg", []string{"check", "-ledger", good, "-causes", causes, "x"}, 2},
	}
	for _, tc := range cases {
		if got := run(tc.args); got != tc.want {
			t.Errorf("%s: exit %d, want %d", tc.name, got, tc.want)
		}
	}
}
