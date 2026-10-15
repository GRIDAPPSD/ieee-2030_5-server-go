// A one-shot request to open a device's Programs drawer on the Devices tab.
// A link on another tab leaves the request and navigates; the Devices table
// takes it when it mounts, so a later visit to the tab does not reopen it.
let pending = ''

export function requestPrograms(deviceId: string): void {
  pending = deviceId
}

export function takeProgramsRequest(): string {
  const id = pending
  pending = ''
  return id
}
