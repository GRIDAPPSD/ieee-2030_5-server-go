// Package fixture holds logging calls the ratchet checker must flag (lines
// ending in "// want") and must pass (every other call).
package fixture

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
)

type id string

type level int

func (l level) String() string { return "free text" }

type count int

// printfWrapper forwards to log.Printf; its call sites are what count.
func printfWrapper(key, format string, args ...any) {
	log.Printf(format, args...)
}

// prefixed forwards through printfWrapper with a built message.
func prefixed(format string, args ...any) {
	printfWrapper("k", "prefix: "+format, args...)
}

// printlnWrapper forwards values with no format of its own.
func printlnWrapper(args ...any) {
	log.Println(args...)
}

func shapes(r *http.Request, name string, lv level, c count) {
	logf := log.Printf
	logf("function value %s", name) // want
	logf("function value constant")
	printfWrapper("k", "wrapper %s", name) // want
	printfWrapper("k", "wrapper %s", logsafe.Str(name))
	prefixed("nested %s", r.URL.Path) // want
	prefixed("nested constant")
	printlnWrapper(name) // want
	printlnWrapper("constant", 1)
	_ = log.Output(2, "line "+name) // want
	_ = log.Output(2, "constant line")
	log.Printf("numeric with String %v", lv) // want
	log.Printf("plain number %d", c)
}

func flagged(r *http.Request, err error, name string) {
	log.Printf("refused %s", r.URL.Path)                       // want
	log.Printf("refused: "+name+" %d", 1)                      // want
	log.Printf(name)                                           // want
	log.Printf("failed: %v", err)                              // want
	log.Println("path", r.URL.Query().Get("agg"))              // want
	slog.Warn("refused", "path", r.URL.Path)                   // want
	slog.Warn("refused "+name, "n", 1)                         // want
	slog.InfoContext(context.Background(), "x", "k", id(name)) // want
	slog.Info("x", slog.String("path", r.URL.Path))            // want
	slog.Default().Error("x", "err", err)                      // want
	var raw []any
	slog.Warn("x", raw...) // want
}

func passed(r *http.Request, err error, n int, d float64, ok bool) {
	log.Printf("refused %s", logsafe.Str(r.URL.Path))
	log.Printf("failed: %v %d %v %v", logsafe.Err(err), n, d, ok)
	log.Println("constant", 1, true)
	slog.Warn("refused", "path", logsafe.Str(r.URL.Path), "n", n)
	slog.Info("x", slog.String("path", logsafe.Str(r.URL.Path)), slog.Int("n", n))
	slog.InfoContext(context.Background(), "x", "err", logsafe.Err(errors.New("e")))
}
