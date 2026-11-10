package identity

import "net/http"

// Require reports whether a request that carries lfdi and ok has an identity
// to act on. When it has none, Require answers 403 and returns false, so every
// handler refuses a missing or empty identity the same way: an empty value
// would otherwise key every such caller onto one record.
func Require(w http.ResponseWriter, lfdi string, ok bool) bool {
	if ok && lfdi != "" {
		return true
	}
	http.Error(w, "identity required", http.StatusForbidden)
	return false
}
