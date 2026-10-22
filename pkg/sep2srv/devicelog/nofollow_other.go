//go:build !unix

package devicelog

// noFollow is unavailable here; the directory check in PrepareDir is the
// only guard on these platforms.
const noFollow = 0
