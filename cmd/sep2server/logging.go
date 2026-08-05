package main

import (
	"io"
	"log/slog"
)

// setupLogging routes the stdlib log package through a slog JSON handler.
//
// #253: the observability stack ingests container stdout via Promtail,
// which keeps the line opaque and relies on read-time LogQL. JSON on stdout
// is therefore the right wire shape: Promtail forwards the raw line to Loki,
// and Grafana/LogQL parse the fields at query time.
//
// The bridge is deliberately a single default-handler swap rather than a
// per-call-site rewrite. The repo has ~94 stdlib log.Printf/log.Println
// sites; slog.SetDefault redirects the stdlib log package's output through
// the slog handler, so every existing call emits structured JSON without
// touching the call sites. SetLogLoggerLevel pins those bridged lines at
// INFO so none of the current log.* output is dropped or downgraded.
//
// Order matters: SetLogLoggerLevel must precede SetDefault for the bridged
// level to take effect on the installed handler.
//
// NOTE: fatal/usage errors (bad args, serve/certs sub-command failures) are
// written to stderr as plain fmt.Fprintf text and exit via os.Exit — they
// intentionally bypass this JSON handler. Those lines will not appear in
// Loki; operators must check the raw container stderr for exit-reason
// diagnostics. Routing fatals through slog is a separate follow-up.
func setupLogging(w io.Writer) {
	slog.SetLogLoggerLevel(slog.LevelInfo)
	slog.SetDefault(slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
}
