// MintedCert is what a successful POST /api/certs/device mint hands back
// (internal/handler/admin_certs.go's createDeviceCertResponse), carried
// from CertPanel to AddDevice through AdminShell's hoisted state: the two
// panels sit on different tabs, and a tab switch unmounts and remounts
// them ({#if} in AdminShell.svelte; js-ts.md, "{#if} unmounts its block
// when the guard turns false"), so neither panel's own local state
// survives the crossing.
export interface MintedCert {
  certPEM: string
  keyPEM: string
  sfdi: string
  lfdi: string
  serial: string
}
