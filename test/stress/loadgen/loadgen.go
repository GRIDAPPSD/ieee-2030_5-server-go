// Package loadgen is the lean IEEE 2030.5 stress load generator for IEEESRV-007.
//
// Design: one *http.Client per virtual client (pre-built from a unique device
// cert), driven from a semaphore-gated goroutine pool. No inverter state
// machine. Deterministic from SEED via per-client math/rand instances.
// Self-metrics (send rate, goroutine count) are emitted so the
// driver-vs-server disambiguation is computable from the result artifacts.
package loadgen

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Config is the full parameterisation for a load run. All fields are required
// unless noted.
type Config struct {
	// Addressing
	TargetHost string // server host, e.g. "127.0.0.1"
	TargetPort int    // server protocol port, e.g. 8443

	// Scale
	MaxClients int           // 0 = open-ended (ramp until criterion fires or ctx cancelled)
	RampRate   int           // virtual clients added per second (default 5)
	Duration   time.Duration // 0 = unlimited (run until criterion or ctx cancelled)

	// Behaviour
	Dim      string  // "throughput" | "fanout" | "soak" | "tls"
	ThinkMs  int     // per-request think time in ms (0 = no think, 1000 for soak)
	Seed     uint64  // RNG seed for deterministic per-client behaviour

	// TLS
	RootCA     []byte // PEM CA cert trusted by clients
	ClientCCM  bool   // true = CCM-8 mode on client side (GCM by default)
	NoKeepalive bool  // disable keepalive (forces fresh handshake per request, TLS dim)

	// Per-client cert accessor. Called once per virtual client index at setup.
	// Returns (certPEM, keyPEM, err).
	ClientCert func(idx int) (certPEM, keyPEM []byte, err error)

	// Output
	LatencyFile *os.File // client-latency.jsonl destination
	LogFile     *os.File // loadgen log destination

	// Breaking-point criteria (populated from dimension defaults by Run)
	P99ThresholdMs   int     // p99 latency threshold in ms
	ErrRateThreshold float64 // error rate threshold [0,1]
	QueueFullOnset   bool    // stop when queue_full counter becomes non-zero
	HandshakeErrRate float64 // handshake error rate threshold (TLS dim)
}

// BreakResult carries the breaking-point verdict.
type BreakResult struct {
	Dimension       string  `json:"dimension"`
	Criterion       string  `json:"criterion"`
	ValueAtBreak    float64 `json:"value_at_break"`
	TimeElapsedSec  float64 `json:"time_elapsed_sec"`
	AtClients       int     `json:"at_clients"`
	HostLimited     bool    `json:"host_limited"`
	HostLimitReason string  `json:"host_limit_reason,omitempty"`
}

// latencyRecord is one line in client-latency.jsonl.
type latencyRecord struct {
	TSendUnix  float64 `json:"t_send"`
	TRecvMs    float64 `json:"t_recv_ms"`
	StatusCode int     `json:"status"`
	Endpoint   string  `json:"endpoint"`
	ClientID   int     `json:"client_id"`
}

// selfMetrics holds driver-side counters for disambiguation.
type selfMetrics struct {
	sent     atomic.Int64
	errors   atomic.Int64
	gorCount atomic.Int64
}

// endpoints is the round-robin GET path set for each dimension.
var endpointsByDim = map[string][]string{
	"throughput": {"/dcap", "/tm", "/edev"},
	"fanout":     {"/dcap", "/tm", "/edev"},
	"soak":       {"/dcap", "/tm", "/edev"},
	"tls":        {"/dcap"},
}

// Run executes the load phase and returns a BreakResult (which may have
// Criterion=="none" if ctx is cancelled before any criterion fires).
func Run(ctx context.Context, cfg Config) (*BreakResult, error) {
	logger := log.New(cfg.LogFile, "[loadgen] ", log.LstdFlags)

	baseURL := fmt.Sprintf("https://%s:%d", cfg.TargetHost, cfg.TargetPort)

	endpoints, ok := endpointsByDim[cfg.Dim]
	if !ok {
		return nil, fmt.Errorf("unknown dim %q", cfg.Dim)
	}

	rootPool := x509.NewCertPool()
	if !rootPool.AppendCertsFromPEM(cfg.RootCA) {
		return nil, fmt.Errorf("loadgen: no root CA certs parsed from RootCA PEM")
	}

	if cfg.RampRate <= 0 {
		cfg.RampRate = 5
	}

	// Breaking-point criteria defaults per dimension.
	if cfg.P99ThresholdMs == 0 {
		switch cfg.Dim {
		case "tls":
			cfg.P99ThresholdMs = 1000
		default:
			cfg.P99ThresholdMs = 500
		}
	}
	if cfg.ErrRateThreshold == 0 {
		cfg.ErrRateThreshold = 0.01
	}
	if cfg.HandshakeErrRate == 0 {
		cfg.HandshakeErrRate = 0.001
	}

	// Latency ring buffer: last 2000 samples for rolling p99.
	const ringSize = 2000
	var (
		ring     [ringSize]int64 // latency in ms
		ringHead int
		ringMu   sync.Mutex
	)

	sm := &selfMetrics{}

	var (
		latMu sync.Mutex // guards latency file writes
	)

	writeLatency := func(rec latencyRecord) {
		if cfg.LatencyFile == nil {
			return
		}
		b, err := json.Marshal(rec)
		if err != nil {
			return
		}
		latMu.Lock()
		defer latMu.Unlock()
		_, _ = cfg.LatencyFile.Write(b)
		_, _ = cfg.LatencyFile.Write([]byte("\n"))
	}

	// breakResult carries the first criterion that fires.
	// stopCh is closed (not sent on) to stop all virtual clients simultaneously.
	// This avoids the drain-on-first-read problem with a capacity-1 channel.
	var breakResult atomic.Pointer[BreakResult]
	stopCh := make(chan struct{})
	var stopOnce sync.Once
	fireStop := func(br *BreakResult) {
		stopOnce.Do(func() {
			breakResult.Store(br)
			close(stopCh)
		})
	}

	// clientCount tracks active virtual clients.
	var clientCount atomic.Int32

	start := time.Now()

	// checkCriteria runs the rolling-window check every 2 seconds.
	// It calls fireStop at most once.
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		var sent0 int64
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-ticker.C:
				totalSent := sm.sent.Load()
				totalErrs := sm.errors.Load()
				elapsed := time.Since(start).Seconds()

				// Rolling p99 from ring buffer.
				ringMu.Lock()
				samples := make([]int64, ringSize)
				copy(samples, ring[:])
				ringMu.Unlock()
				p99 := rollingP99(samples)

				// Error rate over total requests.
				var errRate float64
				if totalSent > 0 {
					errRate = float64(totalErrs) / float64(totalSent)
				}

				// Send rate (delta over 2s window).
				sendRate := float64(totalSent-sent0) / 2.0
				sent0 = totalSent

				// Write self-metric log line.
				sm.gorCount.Store(int64(runtime.NumGoroutine()))
				logger.Printf("t=%.0fs clients=%d p99=%dms errRate=%.4f sendRate=%.1frps goroutines=%d",
					elapsed, clientCount.Load(), p99, errRate, sendRate, sm.gorCount.Load())

				atClients := int(clientCount.Load())

				// Check criteria.
				if p99 > int64(cfg.P99ThresholdMs) && totalSent > 100 {
					fireStop(&BreakResult{
						Dimension: cfg.Dim, Criterion: "p99_latency_ms",
						ValueAtBreak: float64(p99), TimeElapsedSec: elapsed, AtClients: atClients,
					})
					return
				}
				if errRate > cfg.ErrRateThreshold && totalSent > 100 {
					fireStop(&BreakResult{
						Dimension: cfg.Dim, Criterion: "error_rate",
						ValueAtBreak: errRate, TimeElapsedSec: elapsed, AtClients: atClients,
					})
					return
				}
			}
		}
	}()

	// rampTicker fires once per second; each tick launches RampRate clients.
	rampTicker := time.NewTicker(time.Second)
	defer rampTicker.Stop()

	var wg sync.WaitGroup
	clientIdx := 0

	// duration timeout.
	var doneTimer <-chan time.Time
	if cfg.Duration > 0 {
		t := time.NewTimer(cfg.Duration)
		defer t.Stop()
		doneTimer = t.C
	}

	// Build and launch one virtual client goroutine.
	launchClient := func(idx int) {
		certPEM, keyPEM, err := cfg.ClientCert(idx)
		if err != nil {
			logger.Printf("client %d: cert error: %v", idx, err)
			return
		}
		tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			logger.Printf("client %d: key pair error: %v", idx, err)
			return
		}
		tlsCfg := &tls.Config{
			Certificates: []tls.Certificate{tlsCert},
			RootCAs:      rootPool,
			ServerName:   cfg.TargetHost,
			MinVersion:   tls.VersionTLS12,
			MaxVersion:   tls.VersionTLS12,
		}
		transport := &http.Transport{
			TLSClientConfig:     tlsCfg,
			DisableKeepAlives:   cfg.NoKeepalive,
			MaxIdleConnsPerHost: 2,
		}
		client := &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
		}

		// Per-client seeded RNG for deterministic endpoint selection.
		rng := rand.New(rand.NewPCG(cfg.Seed, uint64(idx)))

		clientCount.Add(1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer clientCount.Add(-1)
			epIdx := 0
			for {
				select {
				case <-ctx.Done():
					return
				case <-stopCh:
					return
				default:
				}

				// Endpoint selection: round-robin with RNG jitter.
				ep := endpoints[epIdx%len(endpoints)]
				_ = rng.IntN(100) // consume RNG for future jitter use
				epIdx++

				url := baseURL + ep
				tSend := time.Now()
				resp, doErr := client.Get(url)
				tRecv := time.Now()
				latMs := tRecv.Sub(tSend).Milliseconds()

				sm.sent.Add(1)

				var statusCode int
				if doErr != nil {
					sm.errors.Add(1)
					statusCode = 0
				} else {
					statusCode = resp.StatusCode
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					if resp.StatusCode >= 400 {
						sm.errors.Add(1)
					}
				}

				// Update ring buffer.
				ringMu.Lock()
				ring[ringHead%ringSize] = latMs
				ringHead++
				ringMu.Unlock()

				writeLatency(latencyRecord{
					TSendUnix:  float64(tSend.UnixNano()) / 1e9,
					TRecvMs:    float64(latMs),
					StatusCode: statusCode,
					Endpoint:   ep,
					ClientID:   idx,
				})

				if cfg.ThinkMs > 0 {
					select {
					case <-ctx.Done():
						return
					case <-time.After(time.Duration(cfg.ThinkMs) * time.Millisecond):
					}
				}
			}
		}()
	}

	for {
		select {
		case <-ctx.Done():
			fireStop(&BreakResult{
				Dimension: cfg.Dim, Criterion: "none",
				TimeElapsedSec: time.Since(start).Seconds(), AtClients: int(clientCount.Load()),
			})
			wg.Wait()
			return breakResult.Load(), nil

		case <-doneTimer:
			fireStop(&BreakResult{
				Dimension: cfg.Dim, Criterion: "duration_elapsed",
				TimeElapsedSec: time.Since(start).Seconds(), AtClients: clientIdx,
			})
			wg.Wait()
			return breakResult.Load(), nil

		case <-stopCh:
			// Criterion fired in the checker goroutine.
			wg.Wait()
			br := breakResult.Load()
			if br != nil {
				br.TimeElapsedSec = time.Since(start).Seconds()
			}
			return br, nil

		case <-rampTicker.C:
			// Check whether we have already hit CLIENTS cap.
			if cfg.MaxClients > 0 && clientIdx >= cfg.MaxClients {
				// All clients launched; wait for duration or criterion.
				continue
			}
			// Launch up to RampRate new clients this tick.
			end := clientIdx + cfg.RampRate
			if cfg.MaxClients > 0 && end > cfg.MaxClients {
				end = cfg.MaxClients
			}
			for ; clientIdx < end; clientIdx++ {
				launchClient(clientIdx)
			}
		}
	}
}

// RegisterClients sends POST /edev using each client's cert to register
// the device before the load phase. Returns on first error.
func RegisterClients(ctx context.Context, baseURL string, count int, rootCA []byte, clientCert func(int) ([]byte, []byte, error)) error {
	rootPool := x509.NewCertPool()
	if !rootPool.AppendCertsFromPEM(rootCA) {
		return fmt.Errorf("register: no CA certs")
	}

	for i := 0; i < count; i++ {
		certPEM, keyPEM, err := clientCert(i)
		if err != nil {
			return fmt.Errorf("register client %d cert: %w", i, err)
		}
		tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return fmt.Errorf("register client %d key pair: %w", i, err)
		}
		tlsCfg := &tls.Config{
			Certificates: []tls.Certificate{tlsCert},
			RootCAs:      rootPool,
			ServerName:   "127.0.0.1",
			MinVersion:   tls.VersionTLS12,
			MaxVersion:   tls.VersionTLS12,
		}
		client := &http.Client{
			Timeout:   10 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/edev", bytes.NewReader(nil))
		if err != nil {
			return fmt.Errorf("register client %d req: %w", i, err)
		}
		req.Header.Set("Content-Type", "application/sep+xml")
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("register client %d: %w", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
			return fmt.Errorf("register client %d: unexpected status %d", i, resp.StatusCode)
		}
	}
	return nil
}

// rollingP99 returns the 99th-percentile of the non-zero samples in buf.
func rollingP99(buf []int64) int64 {
	var vals []int64
	for _, v := range buf {
		if v > 0 {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return 0
	}
	// Simple insertion-sort on small slice.
	for i := 1; i < len(vals); i++ {
		for j := i; j > 0 && vals[j] < vals[j-1]; j-- {
			vals[j], vals[j-1] = vals[j-1], vals[j]
		}
	}
	idx := int(float64(len(vals)) * 0.99)
	if idx >= len(vals) {
		idx = len(vals) - 1
	}
	return vals[idx]
}

