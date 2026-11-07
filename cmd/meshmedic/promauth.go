package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"

	"github.com/Git-Hospital/meshmedic/pkg/detect"
	"github.com/Git-Hospital/meshmedic/pkg/prom"
)

// promAuthFlags let the forms without a config file reach a Prometheus
// behind a bearer token or a private CA. Basic auth needs the config file's
// prometheusAuth section.
type promAuthFlags struct{ tokenEnv, caFile *string }

func addPromAuthFlags(fs *flag.FlagSet) promAuthFlags {
	return promAuthFlags{
		tokenEnv: fs.String("prometheus-token-env", "", "name of the environment variable that holds a bearer token for Prometheus (flag form; a config file uses prometheusAuth)"),
		caFile:   fs.String("prometheus-ca-file", "", "PEM file of an extra CA to trust for Prometheus (flag form; a config file uses prometheusAuth)"),
	}
}

func (f promAuthFlags) auth() *detect.PrometheusAuth {
	if *f.tokenEnv == "" && *f.caFile == "" {
		return nil
	}
	return &detect.PrometheusAuth{BearerTokenEnv: *f.tokenEnv, CAFile: *f.caFile}
}

// apply puts the flags into a config built from flags, and refuses them next
// to a config file, which has its own prometheusAuth section.
func (f promAuthFlags) apply(cfg *detect.Config, flagForm bool) error {
	a := f.auth()
	switch {
	case a == nil:
		return nil
	case !flagForm:
		return errors.New("--prometheus-token-env and --prometheus-ca-file belong to the --prometheus form; use prometheusAuth in the config file instead")
	}
	cfg.PrometheusAuth = a
	return a.Check()
}

// newPromClient builds the client for a Prometheus URL, reading the
// credentials auth names from the environment. It exits on a bad setting,
// before any request is sent.
func newPromClient(promURL string, auth *detect.PrometheusAuth) *prom.Client {
	a, err := auth.Resolve(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config invalid:", err)
		os.Exit(1)
	}
	c, err := prom.NewClientWithAuth(promURL, a)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config invalid:", err)
		os.Exit(1)
	}
	if u, err := url.Parse(promURL); err == nil && u.Scheme == "http" && (a.BearerToken != "" || a.Password != "") {
		fmt.Fprintln(os.Stderr, "warning: sending Prometheus credentials over plain http; anyone on the path can read them")
	}
	return c
}
