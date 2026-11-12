package chartcheck

import (
	"reflect"
	"strings"
	"testing"
)

func TestArgvExactVector(t *testing.T) {
	a := RenderArgs{
		Chart: "./chart", Namespace: "ns1", ReleaseName: "r1", KubeVersion: "v1.33.0",
		ValuesFiles:          []string{"a.yaml", "-b.yaml"},
		Set:                  []string{"x=1", "--post-renderer=evil"},
		SetString:            []string{"s=t"},
		SetJSON:              []string{`j={"a":1}`},
		SkipSchemaValidation: true,
	}
	want := []string{
		"template", "--namespace=ns1", "--kube-version=v1.33.0",
		"--values=a.yaml", "--values=-b.yaml",
		"--set=x=1", "--set=--post-renderer=evil", "--set-string=s=t", `--set-json=j={"a":1}`,
		"--skip-schema-validation", "--include-crds", "--", "r1", "./chart",
	}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := a.Argv(); !reflect.DeepEqual(got, want) {
		t.Errorf("argv:\n%q\nwant\n%q", got, want)
	}
}

func TestRenderArgsRefusals(t *testing.T) {
	ok := RenderArgs{Chart: "./chart", Namespace: "default", ReleaseName: "release"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*RenderArgs)
		msg  string
	}{
		{"chart is a flag", func(a *RenderArgs) { a.Chart = "-h" }, "start with"},
		{"chart is a long flag", func(a *RenderArgs) { a.Chart = "--debug" }, "start with"},
		{"chart empty", func(a *RenderArgs) { a.Chart = "" }, "chart"},
		{"release is a flag", func(a *RenderArgs) { a.ReleaseName = "--debug" }, "release name"},
		{"release is a flag with value", func(a *RenderArgs) { a.ReleaseName = "--post-renderer=X" }, "release name"},
		{"release upper case", func(a *RenderArgs) { a.ReleaseName = "Release" }, "release name"},
		{"release too long", func(a *RenderArgs) { a.ReleaseName = strings.Repeat("a", 54) }, "release name"},
		{"release empty", func(a *RenderArgs) { a.ReleaseName = "" }, "release name"},
		{"namespace is a flag", func(a *RenderArgs) { a.Namespace = "-n" }, "namespace"},
		{"namespace with a dot", func(a *RenderArgs) { a.Namespace = "a.b" }, "namespace"},
		{"namespace too long", func(a *RenderArgs) { a.Namespace = strings.Repeat("a", 64) }, "namespace"},
		{"kube version a flag", func(a *RenderArgs) { a.KubeVersion = "--debug" }, "kube-version"},
		{"kube version junk", func(a *RenderArgs) { a.KubeVersion = "latest" }, "kube-version"},
	} {
		a := ok
		tc.edit(&a)
		err := a.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: err = %v, want one containing %q", tc.name, err, tc.msg)
		}
	}
	for _, good := range []string{"v1.33.0", "1.33", "1.30.2-gke.100", "v1.31.0+k3s1"} {
		a := ok
		a.KubeVersion = good
		if err := a.Validate(); err != nil {
			t.Errorf("kube version %q refused: %v", good, err)
		}
	}
	// A chart path that starts with a dash is written with a directory.
	a := ok
	a.Chart = "./-name"
	if err := a.Validate(); err != nil {
		t.Errorf("./-name refused: %v", err)
	}
}
