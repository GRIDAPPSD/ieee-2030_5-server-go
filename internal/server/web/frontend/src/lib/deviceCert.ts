// The IEEE 2030.5 certificate device types CertPanel offers, mirroring the
// DeviceType const block in internal/certs/oids.go by hand: the frontend
// build has no live source for a Go const. deviceCert.test.ts reads
// oids.go's const block at test time and fails the build if the two
// disagree, so a value added or renumbered there without a matching
// change here breaks CI instead of drifting silently (issue 594's first
// acceptance criterion).

export interface DeviceTypeOption {
  value: number
  label: string
}

export const DEVICE_TYPES: DeviceTypeOption[] = [
  { value: 1, label: 'Generic' },
  { value: 2, label: 'Mobile' },
  { value: 3, label: 'Post-Manufacture' },
]

export const DEFAULT_DEVICE_TYPE = DEVICE_TYPES[0].value

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
