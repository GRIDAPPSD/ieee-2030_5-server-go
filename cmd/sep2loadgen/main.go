// cmd/sep2loadgen is the IEEE 2030.5 stress load generator.
//
// Usage: sep2loadgen [flags]
//
// All parameters are read from environment variables (mirroring the stress.sh
// harness convention) and overridable via flags.
//
// The binary is used by scripts/stress.sh; it is NOT intended to run standalone
// without the harness (no server lifecycle management here).
//
// For the fanout dimension, the binary also:
//   - Starts a plain-HTTP notification receiver and prints its URL to stderr
//     so stress.sh can pass it to the subscribe step.
//   - Drives mutation injection via POST /test/mutations/stress-notify using
//     the mutation token from MUTATION_TOKEN / -mutation-token.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/stress/loadgen"
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
	defer func() { _ = lf.Close() }()
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
	defer func() { _ = latFile.Close() }()

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
		// Bind to loopback only: a 0.0.0.0 bind would let stray off-box
		// POSTs inflate notify_delivered and skew the break verdict.
		addr := fmt.Sprintf("127.0.0.1:%d", receiverPort)
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

	// Fanout: build the per-client subscribe func. Each virtual client
	// subscribes to the shared /dcap resource using its own edev ID from
	// the manifest and the notification receiver URL. This unifies subscriber
	// count with active client count: CLIENTS=N means N devices that each
	// drive GET traffic AND hold an active subscription.
	var clientSubscribeFunc func(idx int, client *http.Client) error
	if dim == "fanout" && notifyURL != "" {
		// Read edev manifest written by the register step.
		manifestPath := filepath.Join(resultsDir, "pki", "edev-manifest.json")
		manifestData, merr := os.ReadFile(manifestPath)
		if merr != nil {
			logBoth("fanout: read edev-manifest.json: %v (subscriber-per-client disabled)", merr)
		} else {
			var manifest struct {
				EdevIDs []string `json:"edev_ids"`
			}
			if jerr := json.Unmarshal(manifestData, &manifest); jerr != nil {
				logBoth("fanout: parse edev-manifest.json: %v (subscriber-per-client disabled)", jerr)
			} else {
				edevIDs := manifest.EdevIDs
				subURL := fmt.Sprintf("https://%s:%d", targetHost, targetPort)
				subResource := mutationHref
				if subResource == "" {
					subResource = "/dcap"
				}
				capturedNotifyURL := notifyURL
				clientSubscribeFunc = func(idx int, client *http.Client) error {
					if idx >= len(edevIDs) || edevIDs[idx] == "" {
						return fmt.Errorf("no edev ID for client %d in manifest", idx)
					}
					sub := sep2.Subscription{
						SubscribedResource: subResource,
						NotificationURI:    capturedNotifyURL,
						Encoding:           sep2.EncodingXML,
					}
					body, err := xml.Marshal(sub)
					if err != nil {
						return fmt.Errorf("marshal subscription: %w", err)
					}
					req, err := http.NewRequest(http.MethodPost,
						subURL+"/edev/"+edevIDs[idx]+"/sub",
						bytes.NewReader(body))
					if err != nil {
						return fmt.Errorf("build request: %w", err)
					}
					req.Header.Set("Content-Type", "application/sep+xml")
					resp, err := client.Do(req)
					if err != nil {
						return fmt.Errorf("POST sub: %w", err)
					}
					defer func() { _ = resp.Body.Close() }()
					_, _ = io.Copy(io.Discard, resp.Body)
					if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
						return fmt.Errorf("POST sub: unexpected status %d", resp.StatusCode)
					}
					return nil
				}
				logBoth("fanout: per-client subscribe enabled (edev manifest: %d IDs, subResource=%s)",
					len(edevIDs), subResource)
			}
		}
	}

	cfg := loadgen.Config{
		TargetHost:      targetHost,
		TargetPort:      targetPort,
		MaxClients:      clients,
		RampRate:        rampRate,
		Duration:        duration,
		Dim:             dim,
		ThinkMs:         thinkMs,
		Seed:            seed,
		RootCA:          caCertPEM,
		ClientCCM:       ccm,
		NoKeepalive:     dim == "tls",
		ClientCert:      clientCertFunc,
		LatencyFile:     latFile,
		LogFile:         lf,
		QueueFullOnset:  dim == "fanout" && metricsURL != "",
		MetricsURL:      metricsURL,
		MutationToken:   mutationToken,
		MutationHref:    mutationHref,
		MutationRateHz:  mutationRateHz,
		NotifyReceiver:  notifyReceiver,
		ClientSubscribe: clientSubscribeFunc,
	}

	logBoth("load phase starting")
	br, err := loadgen.Run(ctx, cfg)
	if err != nil {
		logBoth("load run error: %v", err)
		os.Exit(1)
	}

	// Fanout validity gate.
	// After a fanout run, verify that notification deliveries scaled with the
	// active client count (true N-wide fan-out). If delivered_per_mutation is
	// substantially below client count (threshold: 0.9 x clients), some
	// per-client subscriptions must have failed via the log-and-continue path
	// and the run is NOT a valid N-wide data point.
	//
	// mutation_count = mutationRateHz * elapsed_sec (approximate; integer Hz
	// times elapsed seconds in float gives the expected mutation call count).
	// A zero mutation count (e.g. dim != fanout, or elapsed < 1s) skips
	// the gate so non-fanout dims are unaffected.
	if dim == "fanout" && clientSubscribeFunc != nil && mutationRateHz > 0 && br.TimeElapsedSec >= 1 {
		mutationCount := float64(mutationRateHz) * br.TimeElapsedSec
		dpm := float64(br.NotifyDelivered) / mutationCount
		br.DeliveredPerMutation = dpm
		threshold := 0.9 * float64(br.AtClients)
		logBoth("fanout validity: delivered_per_mutation=%.2f threshold=%.2f (0.9 x %d clients)",
			dpm, threshold, br.AtClients)
		if dpm < threshold {
			br.PartialSubscription = true
			logBoth("FANOUT VALIDITY GATE FAILED: delivered_per_mutation=%.2f < threshold=%.2f; "+
				"some subscriptions failed; this run is NOT a valid N-wide data point", dpm, threshold)
		} else {
			logBoth("fanout validity gate passed")
		}
	}

	// Write breaking-point.json.
	bpPath := filepath.Join(resultsDir, "breaking-point.json")
	bpData, _ := json.MarshalIndent(br, "", "  ")
	if writeErr := os.WriteFile(bpPath, bpData, 0o644); writeErr != nil {
		logBoth("write breaking-point.json: %v", writeErr)
	}
	logBoth("done: criterion=%s clients=%d elapsed=%.1fs notifyDelivered=%d notifyQueueFull=%d deliveredPerMutation=%.2f partialSubscription=%v",
		br.Criterion, br.AtClients, br.TimeElapsedSec, br.NotifyDelivered, br.NotifyQueueFull,
		br.DeliveredPerMutation, br.PartialSubscription)
	if br.PartialSubscription {
		os.Exit(2)
	}
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
