package main

import (
	"strings"
	"testing"

	"github.com/Git-Hospital/meshmedic/hack/compat-exp/ledger"
)

// F1: a key admitted after the freeze is not in causes.json, so it has no
// static check. One counted finding of it must not stop the analysis.
func TestNewKeyHasNoStaticCheckAndNoError(t *testing.T) {
	r := []ChartReport{chart("hit", wr("W3", bad(wk))), chart("hit2", wr("W3", bad(wk)))}
	for _, n := range names(10, "s") {
		r = append(r, chart(n, wr("W3", ok(wk))))
	}
	f := newFixture(r, r, nil)
	f.in.Ledger.NewKeys = []ledger.NewKey{{Key: "tls-origination"}}
	f.withCerts(row("hit", "W3", wk, "protocol", "gap"), row("hit2", "W3", wk, "tls-origination", "gap"))
	// static results exist for the frozen key only, as static.sh writes them
	st := []StaticResult{stat("hit", "W3", "protocol", true)}
	for _, n := range names(10, "s") {
		st = append(st, stat(n, "W3", "protocol", false))
	}
	f.in.Static, f.in.StaticGiven = st, true
	res := f.analyze(t)
	if res.Metrics.N != 2 || res.Metrics.P != 2 || res.Metrics.S != 1 {
		t.Fatalf("%v", res.Metrics)
	}
	var found bool
	for _, sk := range res.Static {
		if sk.Key == "tls-origination" {
			found = sk.NoCheck && !sk.Qualifies && sk.NoCheckReason == NoCheckAfterFreeze
		}
	}
	if !found {
		t.Fatalf("%+v", res.Static)
	}
	if md := string(RenderMarkdown(res)); !strings.Contains(md, "no static check: admitted after the freeze") {
		t.Fatal(md)
	}
}
