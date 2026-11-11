package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Git-Hospital/meshmedic/hack/compat-exp/ledger"
)

// TestRealCausesFile reads the committed causes.json, the contract of
// guard.sh, ledger and crossrun, and checks what crossrun takes from it.
func TestRealCausesFile(t *testing.T) {
	c, err := ledger.LoadCauses(filepath.Join("..", "causes.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := &analysis{in: &Inputs{Causes: c}}
	cases := []struct {
		key, check string
		has        bool
	}{
		{"protocol", "protocol-declared", true},
		{"startup-order", "classic-sidecar-startup", true},
		{"bind-address", "", false},
		{"brand-new-key", "", false},
	}
	for _, tc := range cases {
		if id, ok, _ := a.checkID(tc.key); id != tc.check || ok != tc.has {
			t.Errorf("%s: %q %v", tc.key, id, ok)
		}
	}
	// protocol applies in W1 only: a static row that says W3 applies is overridden.
	yes := true
	a.in.Static = []StaticResult{{Chart: "c", World: "W3", Check: "protocol-declared", Applicable: &yes, Flagged: true}}
	if f, app, found := a.staticRow("c", "W3", "protocol", "protocol-declared"); f || app || !found {
		t.Errorf("W3 for protocol: flagged %v applicable %v found %v", f, app, found)
	}
}

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFrameIsChartsExp(t *testing.T) {
	in := &Inputs{}
	good := `{"schema":1,"charts":[{"id":"a","publisher":"p","qualified":true,"chart":"a","version":"1"}],"reserves":[{"id":"r","publisher":"q","qualified":true}]}`
	if err := loadFrame(writeTemp(t, good), in); err != nil {
		t.Fatal(err)
	}
	if in.Publisher["r"] != "q" || !in.Qualified["a"] || len(in.FrameOrder) != 2 {
		t.Fatalf("%+v", in)
	}
	for name, body := range map[string]string{
		"a bare array":       `[{"id":"a","publisher":"p","qualified":true}]`,
		"no publisher":       `{"charts":[{"id":"a","qualified":true}]}`,
		"no qualified flag":  `{"charts":[{"id":"a","publisher":"p"}]}`,
		"a repeated id":      `{"charts":[{"id":"a","publisher":"p","qualified":true}],"reserves":[{"id":"a","publisher":"p","qualified":true}]}`,
		"no charts, reserve": `{"reserves":[{"id":"a","publisher":"p","qualified":true}]}`,
	} {
		if err := loadFrame(writeTemp(t, body), &Inputs{}); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestStaticFileIsStrict(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// reuse the pilot inputs and swap the static file in
	man, _ := os.ReadFile(filepath.Join("testdata", "pilot", "inputs.json"))
	abs, _ := filepath.Abs(filepath.Join("testdata", "pilot"))
	causes, _ := filepath.Abs(filepath.Join("..", "causes.json"))
	m := strings.NewReplacer(`"report": "`, `"report": "`+abs+`/`, `"records": "`, `"records": "`+abs+`/`,
		`"ledger": "`, `"ledger": "`+abs+`/`, `"frame": "`, `"frame": "`+abs+`/`, `"../../../causes.json"`, `"`+causes+`"`).Replace(string(man))
	m = strings.Replace(m, `"frame": "`, `"static": "static.json", "frame": "`, 1)
	write("inputs.json", m)
	cases := map[string]string{
		"wildcard world has no applicable": `[{"chart":"a","world":"*","check":"x","flagged":true}]`,
		"unknown field":                    `[{"chart":"a","world":"W1","check":"x","applicable":true,"flagged":true,"extra":1}]`,
		"wrapper object":                   `{"results":[]}`,
	}
	for name, body := range cases {
		write("static.json", body)
		if _, err := LoadInputs(filepath.Join(dir, "inputs.json")); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	write("static.json", `[{"chart":"a","world":"W1","check":"x","applicable":false,"flagged":false}]`)
	in, err := LoadInputs(filepath.Join(dir, "inputs.json"))
	if err != nil || !in.StaticGiven || *in.Static[0].Applicable {
		t.Fatalf("%v %+v", err, in)
	}
}
