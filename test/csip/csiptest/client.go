// Package csiptest provides reusable helpers for the CSIP conformance
// harness under test/csip/. It lifts the inline HTTP plumbing patterns
// established by test/csip/handshake_test.go (IEEE-021) so downstream
// Phase 3 tests can express the canonical "boot a server, GET /dcap,
// walk advertised links, parse the body" flow without re-implementing
// each step.
//
// The package is co-located under test/csip/ on purpose: it is harness
// scaffolding, not production code, and should not appear in the
// `github.com/GRIDAPPSD/ieee-2030_5-go/...` public import surface used
// by consumers of the server.
//
// Scope today:
//   - Client.GetDeviceCapability — GET <baseURL>/dcap and parse (IEEE-056).
//   - Client.WalkLink            — GET <baseURL>+link.Href and parse (IEEE-056).
//   - BootServer                 — boot an in-process spec server on a
//     random port with t.Cleanup teardown (IEEE-058). See server.go.
//
// Out of scope (separate tickets):
//   - Fixture loader             — IEEE-057.
//
// The Client deliberately accepts a pre-built *http.Client + base URL.
// BootServer constructs one wired to its ephemeral CA and exposes it
// via BootedServer.Client(); callers that drive the server with their
// own transport (e.g. SunSpec V1.2 device cert) can still pass it in
// via the BootOption WithClientCert. This keeps the helpers composable
// across CCM-mode and GCM-mode tests without baking CSIP-specific TLS
// assumptions into the Client itself.
package csiptest

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"

	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
)

// ErrEmptyLink is returned by WalkLink when the advertised link has no
// Href set. Tests can distinguish "the server didn't advertise this
// resource" from "the transport failed" with errors.Is.
var ErrEmptyLink = errors.New("csiptest: empty link href")

// UnexpectedStatusError is returned when a chained GET succeeds at the
// transport layer but the server returned a non-200 status. The status
// code is preserved so callers can branch on it (errors.As).
type UnexpectedStatusError struct {
	URL    string
	Status int
}

// Error implements error.
func (e *UnexpectedStatusError) Error() string {
	return fmt.Sprintf("csiptest: GET %s: unexpected status %d", e.URL, e.Status)
}

// Client issues HTTP requests against an IEEE 2030.5 server and parses
// the response bodies as the appropriate sep2 XML types. The zero value
// is not usable; construct via NewClient.
type Client struct {
	http    *http.Client
	baseURL string
}

// NewClient returns a Client that issues requests via httpClient against
// baseURL. baseURL must NOT end with a trailing slash; advertised links
// already carry their absolute path (e.g. "/dcap", "/edev"). httpClient
// must be non-nil; callers compose their own mTLS / transport config
// and hand it in.
func NewClient(httpClient *http.Client, baseURL string) *Client {
	return &Client{http: httpClient, baseURL: baseURL}
}

// HTTPClient returns the underlying *http.Client. Tests that need to
// issue verbs the typed Client does not expose (POST a Subscription,
// DELETE a sub, POST malformed XML to assert 400) can use this to
// reuse the same TLS / device-cert wiring BootServer set up, without
// rebuilding the transport stack.
//
// Mutating the returned client (e.g. swapping Transport) leaks back to
// every Client method on the same instance — don't do that. Read-only
// use (NewRequestWithContext + Do) is the intended shape.
func (c *Client) HTTPClient() *http.Client {
	return c.http
}

// GetDeviceCapability issues GET <baseURL>/dcap and parses the response
// as a sep2.DeviceCapability. It wraps errors with %w at every boundary
// (transport, status, body read, XML unmarshal) so callers can
// errors.Is / errors.As against sentinels above.
func (c *Client) GetDeviceCapability(ctx context.Context) (sep2.DeviceCapability, error) {
	var dcap sep2.DeviceCapability
	if err := c.WalkLink(ctx, sep2.Link{Href: "/dcap"}, &dcap); err != nil {
		return sep2.DeviceCapability{}, fmt.Errorf("csiptest: get device capability: %w", err)
	}
	return dcap, nil
}

// WalkLink issues GET against link.Href (relative to the client's
// baseURL) and unmarshals the response body into dest as XML.
//
// dest must be a non-nil pointer to a sep2 resource type. WalkLink does
// not validate dest's shape — encoding/xml does that.
//
// Behavior:
//   - link.Href == ""           → returns ErrEmptyLink, no dial.
//   - transport failure         → wrapped error from http.Client.Do.
//   - non-200 status            → *UnexpectedStatusError (errors.As friendly).
//   - body read failure         → wrapped error from io.ReadAll.
//   - xml.Unmarshal failure     → wrapped error from xml.Unmarshal.
//
// For sep2.ListLink callers, convert via sep2.Link{Href: ll.Href} at the
// call site — the two types share the Href field and Go's method-sets
// don't allow a single method to accept either pointer.
func (c *Client) WalkLink(ctx context.Context, link sep2.Link, dest any) error {
	if link.Href == "" {
		return ErrEmptyLink
	}
	url := c.baseURL + link.Href

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("csiptest: build request for %s: %w", url, err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("csiptest: GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return &UnexpectedStatusError{URL: url, Status: resp.StatusCode}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("csiptest: read body for %s: %w", url, err)
	}

	if err := xml.Unmarshal(body, dest); err != nil {
		return fmt.Errorf("csiptest: unmarshal body for %s: %w", url, err)
	}
	return nil
}
