package sep2capture

import (
	"crypto/tls"
	"net"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls/gotls"
)

// Conn wraps an already-handshaken TLS connection, *tls.Conn (GCM) or
// core's *gotls.Conn (CCM-8), and re-exposes ConnectionState as the
// signature net/http checks for on any connection that is not itself a
// *tls.Conn (net/http/server.go's unexported connectionStater interface).
// Embedding net.Conn alone would hide it: net.Conn's own method set has no
// ConnectionState, so a plain embed promotes nothing for net/http to find,
// no matter what the wrapped value's concrete type is. Conn forwards
// every other net.Conn method by embedding.
//
// Against a bare *tls.Conn on the GCM path, this costs three things:
// CloseWrite is hidden, so no close_notify is sent on a half-close; the
// automatic 400 reply net/http sends a plaintext HTTP request on a TLS
// port is lost, because that path type-asserts the connection to
// *tls.Conn directly; and the server's error log names this package
// instead of "http", since the concrete type is no longer *tls.Conn.
// ConnectionState also drops NegotiatedProtocol: no config here sets ALPN
// today, so this is latent, but an h2 client would be parsed as HTTP/1.1
// if one ever connected.
type Conn struct {
	net.Conn
}

// ConnectionState returns the TLS state net/http copies into
// http.Request.TLS. Listener.Accept never returns a Conn whose handshake is
// incomplete, so this always reflects a finished handshake.
func (c *Conn) ConnectionState() tls.ConnectionState {
	switch inner := c.Conn.(type) {
	case *tls.Conn:
		return inner.ConnectionState()
	case *gotls.Conn:
		return convertGotlsState(inner.ConnectionState())
	default:
		// Unreachable given Listener's own contract (handshake only wraps a
		// *tls.Conn or *gotls.Conn): fails closed with a zero-value state
		// rather than panicking if that contract is ever broken.
		return tls.ConnectionState{}
	}
}

// convertGotlsState copies the same five fields core's own
// CCMIdentityMiddleware copies (vendor/.../sep2tls/ccmserver.go), so a
// request reaching a handler through this listener carries the identity
// CCMIdentityMiddleware would have derived without capture in front of it.
// CCMIdentityMiddleware itself then sees r.TLS already set and passes
// through (ccmserver.go's "Already populated" branch): one mechanism feeds
// both.
func convertGotlsState(state gotls.ConnectionState) tls.ConnectionState {
	return tls.ConnectionState{
		Version:           state.Version,
		HandshakeComplete: state.HandshakeComplete,
		CipherSuite:       state.CipherSuite,
		ServerName:        state.ServerName,
		PeerCertificates:  state.PeerCertificates,
	}
}

var _ net.Conn = (*Conn)(nil)
var _ handshaker = (*gotls.Conn)(nil)
