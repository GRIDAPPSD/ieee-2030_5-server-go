package sep2capture

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"testing"
)

// benchmarkListener serves the same handler over the same TLS listener
// setup with and without Attach, so the two benchmarks below isolate what
// Attach itself costs on the request path: one pass-through copy per Read
// and Write, one map lookup per request, one channel send per exchange.
func benchmarkListener(b *testing.B, attach bool) {
	m := newMaterial(b)
	tcpLn := listenTCP(b)
	tlsLn := tls.NewListener(tcpLn, gcmServerConfig(b, m))
	ln := NewListener(tlsLn, nil)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "2")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	srv := &http.Server{Handler: handler}

	var serveLn net.Listener = ln
	if attach {
		serveLn = NewRecorder(NewMemorySink(), nil).Attach(srv, ln)
	}
	go func() { _ = srv.Serve(serveLn) }()
	b.Cleanup(func() { _ = srv.Close() })

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: gcmClientConfig(b, m)}}
	url := "https://" + tcpLn.Addr().String() + "/"

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resp, err := client.Get(url)
		if err != nil {
			b.Fatalf("GET: %v", err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			b.Fatalf("read body: %v", err)
		}
		_ = resp.Body.Close()
	}
}

func BenchmarkBareListener(b *testing.B)     { benchmarkListener(b, false) }
func BenchmarkAttachedListener(b *testing.B) { benchmarkListener(b, true) }
