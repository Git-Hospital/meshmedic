package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUnknownScenario(t *testing.T) {
	if os.Getenv("MESHMEDIC_SCENARIO_TEST") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append(os.Args[:1], os.Args[i+1:]...)
				main()
				return
			}
		}
	}

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[0,"1"]}]}}`)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "watch.yaml")
	data := fmt.Sprintf("prometheus: %s\ntargets:\n  - params:\n      service: payments\n      namespace: demo\n    scenarios: [misspelled-scenario]\n", server.URL)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"check", "watch"} {
		t.Run(command, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestUnknownScenario$", "--", command, "--config", path)
			cmd.Env = append(os.Environ(), "MESHMEDIC_SCENARIO_TEST=1")
			output, err := cmd.CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
				t.Fatalf("want exit 1, got %v; output: %s", err, output)
			}
			for _, want := range []string{"config invalid:", "payments", "misspelled-scenario"} {
				if !strings.Contains(string(output), want) {
					t.Errorf("output missing %q: %s", want, output)
				}
			}
			if got := requests.Load(); got != 0 {
				t.Errorf("made %d Prometheus requests before rejecting config", got)
			}
		})
	}
}
