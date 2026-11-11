// Package reqlimit holds the request-body cap shared by the server's handlers.
// It imports nothing, so any handler package may use it without an import cycle.
package reqlimit

// MaxBody is how much of a request body a handler reads into memory. A longer
// body is cut short and fails to parse, so no path accepts a larger document
// than its neighbour.
const MaxBody = 1 << 20
