// cmd/sep2loadgen is the IEEE 2030.5 stress load generator for IEEESRV-007.
//
// Usage: sep2loadgen [flags]
//
// All parameters are read from environment variables (mirroring the stress.sh
// harness convention) and overridable via flags.
//
// The binary is used by scripts/stress.sh; it is NOT intended to run standalone
// without the harness (no server lifecycle management here).
//
// For the fanout dimension (IEEESRV-010), the binary also:
//   - Starts a plain-HTTP notification receiver and prints its URL to stderr
//     so stress.sh can pass it to the subscribe step.
//   - Drives mutation injection via POST /test/mutations/stress-notify using
//     the mutation token from MUTATION_TOKEN / -mutation-token.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/test/stress/loadgen"
)

func main() {
	var (
		targetHost     = strEnv("TARGET_HOST", "127.0.0.1")
		targetPort     = intEnv("TARGET_PORT", 8443)
		clients        = intEnv("CLIENTS", 0)
		rampRate       = intEnv("RAMP_RATE", 5)
		durationSec    = intEnv("DURATION", 0)
		dim            = strEnv("DIM", "throughput")
		ccm            = boolEnv("CCM", false)
		seed           = uint64Env("SEED", 42)
		resultsDir     = strEnv("RESULTS_DIR", "results/current")
		caFile         = strEnv("CA_FILE", "")
		metricsURL     = strEnv("METRICS_URL", "")
		mutationToken  = strEnv("MUTATION_TOKEN", "")
		mutationHref   = strEnv("MUTATION_HREF", "")
		mutationRateHz = intEnv("MUTATION_RATE_HZ", 0)
		// RECEIVER_PORT overrides the default port the notification receiver
		// binds on. 0 = pick a free port (default). The harness reads the
		// URL from the loadgen's stderr line "notify-receiver-url: <url>".
		receiverPort = intEnv("RECEIVER_PORT", 0)
	)

	flag.StringVar(&targetHost, "host", targetHost, "server host")
	flag.IntVar(&targetPort, "port", targetPort, "server port")
	flag.IntVar(&clients, "clients", clients, "max virtual clients (0=open-ended)")
	flag.IntVar(&rampRate, "ramp-rate", rampRate, "clients per second")
	flag.IntVar(&durationSec, "duration", durationSec, "run duration in seconds (0=unlimited)")
	flag.StringVar(&dim, "dim", dim, "dimension: throughput|fanout|soak|tls")
	flag.BoolVar(&ccm, "ccm", ccm, "use CCM-8 cipher")
	flag.StringVar(&resultsDir, "results-dir", resultsDir, "results output directory")
	flag.StringVar(&caFile, "ca", caFile, "CA cert PEM file for server trust")
	flag.StringVar(&metricsURL, "metrics-url", metricsURL, "server Prometheus metrics URL for queue_full criterion (fanout dim)")
	flag.StringVar(&mutationToken, "mutation-token", mutationToken, "X-CSIP-Test-Token value for stress-notify injection (fanout dim)")
	flag.StringVar(&mutationHref, "mutation-href", mutationHref, "href for stress-notify mutation (fanout dim; default /edev/stress-fanout/fsa)")
	flag.IntVar(&mutationRateHz, "mutation-rate-hz", mutationRateHz, "stress-notify calls per second (fanout dim; default 20)")
	flag.IntVar(&receiverPort, "receiver-port", receiverPort, "notification receiver port (fanout dim; 0=auto)")
	flag.Parse()

	// Set up logging to loadgen.log in the results dir.
	logPath := filepath.Join(resultsDir, "loadgen.log")
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Fatalf("open loadgen log: %v", err)
	}
	defer lf.Close()
	log.SetOutput(lf)
	log.SetFlags(log.LstdFlags)
	// Also write to stderr so the harness can capture progress.
	logBoth := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		_, _ = fmt.Fprintln(os.Stderr, "[loadgen] "+msg)
		log.Println(msg)
	}

	logBoth("starting dim=%s clients=%d ramp=%d/s duration=%ds seed=%d ccm=%v",
		dim, clients, rampRate, durationSec, seed, ccm)

	// Read pre-generated PKI (CA cert + per-client certs) from results dir.
	if caFile == "" {
		caFile = filepath.Join(resultsDir, "pki", "ca.crt")
	}
	caCertPEM, err := os.ReadFile(caFile)
	if err != nil {
		logBoth("read CA cert: %v", err)
		os.Exit(1)
	}

	// Build per-client cert func: reads device-<idx>.crt / .key from pki/.
	pkiDir := filepath.Join(resultsDir, "pki")
	clientCertFunc := func(idx int) ([]byte, []byte, error) {
		certPath := filepath.Join(pkiDir, fmt.Sprintf("device-%05d.crt", idx))
		keyPath := filepath.Join(pkiDir, fmt.Sprintf("device-%05d.key", idx))
		certPEM, err := os.ReadFile(certPath)
		if err != nil {
			return nil, nil, fmt.Errorf("read cert %s: %w", certPath, err)
		}
		keyPEM, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, nil, fmt.Errorf("read key %s: %w", keyPath, err)
		}
		return certPEM, keyPEM, nil
	}

	// Open latency file.
	latPath := filepath.Join(resultsDir, "client-latency.jsonl")
	latFile, err := os.OpenFile(latPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logBoth("open latency file: %v", err)
		os.Exit(1)
	}
	defer latFile.Close()

	// Signal handling.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var duration time.Duration
	if durationSec > 0 {
		duration = time.Duration(durationSec) * time.Second
	}

	thinkMs := 0
	if dim == "soak" {
		thinkMs = 1000
	}

	// Fanout: start notification receiver so the server has somewhere to
	// deliver notifications. The receiver URL is printed to stderr as
	// "notify-receiver-url: <url>" so stress.sh can read it before the
	// subscribe step (stress.sh runs the setup binary AFTER the load phase
	// in a sub-shell, so the URL is conveyed via a temp file).
	var notifyReceiver *loadgen.NotifyReceiver
	notifyURL := ""
	if dim == "fanout" {
		addr := fmt.Sprintf(":%d", receiverPort)
		rcv, url, err := loadgen.StartNotifyReceiver(addr)
		if err != nil {
			logBoth("start notify receiver: %v", err)
			os.Exit(1)
		}
		defer rcv.Close()
		notifyReceiver = rcv
		notifyURL = url
		// Write URL to a file so stress.sh can read it before the subscribe
		// step. File name is stable per run dir; stress.sh reads it after
		// the binary prints the "notify-receiver-url" line below.
		urlFile := filepath.Join(resultsDir, "notify-receiver-url.txt")
		if werr := os.WriteFile(urlFile, []byte(notifyURL+"\n"), 0o644); werr != nil {
			logBoth("write notify-receiver-url.txt: %v", werr)
		}
		// Print to stderr in a parseable form so the harness can grep it.
		_, _ = fmt.Fprintln(os.Stderr, "notify-receiver-url: "+notifyURL)
		logBoth("notify receiver started at %s", notifyURL)
	}

	cfg := loadgen.Config{
		TargetHost:     targetHost,
		TargetPort:     targetPort,
		MaxClients:     clients,
		RampRate:       rampRate,
		Duration:       duration,
		Dim:            dim,
		ThinkMs:        thinkMs,
		Seed:           seed,
		RootCA:         caCertPEM,
		ClientCCM:      ccm,
		NoKeepalive:    dim == "tls",
		ClientCert:     clientCertFunc,
		LatencyFile:    latFile,
		LogFile:        lf,
		QueueFullOnset: dim == "fanout" && metricsURL != "",
		MetricsURL:     metricsURL,
		MutationToken:  mutationToken,
		MutationHref:   mutationHref,
		MutationRateHz: mutationRateHz,
		NotifyReceiver: notifyReceiver,
	}

	// For the fanout dimension, the notification receiver is started above but
	// subscriptions are registered by stress.sh AFTER the server starts, BEFORE
	// the load generator is launched. The subscribe step reads notify-receiver-url.txt
	// from the run dir. Stress.sh runs the setup binary in subscribe mode first,
	// then launches this binary, so by the time mutation injection begins, subs
	// are already in place. (The receiver is started early so the URL is known;
	// it accepts inbound POSTs from the server once the load phase begins.)
	_ = notifyURL // used indirectly via the file write above

	logBoth("load phase starting")
	br, err := loadgen.Run(ctx, cfg)
	if err != nil {
		logBoth("load run error: %v", err)
		os.Exit(1)
	}

	// Write breaking-point.json.
	bpPath := filepath.Join(resultsDir, "breaking-point.json")
	bpData, _ := json.MarshalIndent(br, "", "  ")
	if writeErr := os.WriteFile(bpPath, bpData, 0o644); writeErr != nil {
		logBoth("write breaking-point.json: %v", writeErr)
	}
	logBoth("done: criterion=%s clients=%d elapsed=%.1fs notifyDelivered=%d notifyQueueFull=%d",
		br.Criterion, br.AtClients, br.TimeElapsedSec, br.NotifyDelivered, br.NotifyQueueFull)
}

func strEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func intEnv(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscan(v, &n); err != nil {
		return def
	}
	return n
}

func boolEnv(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v == "true" || v == "1" || v == "yes"
}

func uint64Env(key string, def uint64) uint64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var n uint64
	if _, err := fmt.Sscan(v, &n); err != nil {
		return def
	}
	return n
}
