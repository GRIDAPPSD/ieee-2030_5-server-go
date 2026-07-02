// cmd/sep2loadgen is the IEEE 2030.5 stress load generator for IEEESRV-007.
//
// Usage: sep2loadgen [flags]
//
// All parameters are read from environment variables (mirroring the stress.sh
// harness convention) and overridable via flags.
//
// The binary is used by scripts/stress.sh; it is NOT intended to run standalone
// without the harness (no server lifecycle management here).
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
		targetHost  = strEnv("TARGET_HOST", "127.0.0.1")
		targetPort  = intEnv("TARGET_PORT", 8443)
		clients     = intEnv("CLIENTS", 0)
		rampRate    = intEnv("RAMP_RATE", 5)
		durationSec = intEnv("DURATION", 0)
		dim         = strEnv("DIM", "throughput")
		ccm         = boolEnv("CCM", false)
		seed        = uint64Env("SEED", 42)
		resultsDir  = strEnv("RESULTS_DIR", "results/current")
		caFile      = strEnv("CA_FILE", "")
		metricsURL  = strEnv("METRICS_URL", "")
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
	}

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
	logBoth("done: criterion=%s clients=%d elapsed=%.1fs", br.Criterion, br.AtClients, br.TimeElapsedSec)
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
