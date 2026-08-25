// Package cpu stubs internal/cpu for the crypto/tls fork.
// All hardware feature detection returns false — pure Go fallback.
package cpu

var X86 struct {
	HasAES       bool
	HasPCLMULQDQ bool
}

var ARM64 struct {
	HasAES  bool
	HasPMULL bool
}

var S390X struct {
	HasAES    bool
	HasAESCBC bool
	HasAESCTR bool
	HasGHASH  bool
	HasAESGCM bool
}
