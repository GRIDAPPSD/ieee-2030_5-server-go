<script lang="ts">
  // The Logs drawer of one device: its log newest first, from
  // GET /api/device-logs/{lfdi}. It polls for newer lines every 5 s while it
  // is mounted, so closing the drawer is what stops the polling.
  import { untrack } from 'svelte'
  import { fetchJSON } from '../lib/api'
  import { formatAge } from '../lib/comms'
  import { deviceLabelIn } from '../lib/devices'
  import {
    logPath,
    logRow,
    mergeNewer,
    mergeOlder,
    parsePage,
    visible,
    type KindFilter,
    type LogEntry,
  } from '../lib/devicelog'

  let {
    deviceId,
    lfdi,
    devices,
    onClose,
  }: { deviceId: string; lfdi: string; devices: { lfdi: string; sfdi?: string }[]; onClose: () => void } = $props()

  const PAGE = 200
  const POLL_MS = 5000
  const FETCH_TIMEOUT_MS = 20000
  // A poll that returns a full page asks again at once, at most this often
  // per tick, so a long gap is caught up without an unbounded loop.
  const MAX_CATCH_UP = 5

  let entries = $state<LogEntry[]>([])
  let nextBefore = $state<string | null>(null)
  let nextAfter = $state<string | null>(null)
  let dropped = $state(0)
  let loaded = $state(false)
  let error = $state('')
  let olderBusy = $state(false)
  let kind = $state<KindFilter>('all')
  let errorsOnly = $state(false)
  let nowMs = $state(Date.now())

  const rows = $derived(entries.map(logRow))
  const shown = $derived(visible(rows, kind, errorsOnly))

  // The controller of the current device; replaced when the device changes
  // and aborted when the drawer closes, which also drops a reply in flight.
  let lifetime: AbortController | undefined
  // The signal of the run whose poll is out. A poll of a replaced run must
  // not clear it, or the new run's poll would be joined by a second one.
  let pollingFor: AbortSignal | undefined

  function failure(status: number, message: string): string {
    if (status === 404) return 'No log yet for this device.'
    if (status === 503) return `The device log is unavailable: ${message}`
    return `Could not load the log (${status}): ${message}`
  }

  async function read(query: string, signal: AbortSignal) {
    const res = await fetchJSON<unknown>(logPath(lfdi, query), { signal, timeoutMs: FETCH_TIMEOUT_MS })
    if (signal.aborted) return null
    if (!res.ok) {
      error = failure(res.status, res.error)
      return null
    }
    const page = parsePage(res.data)
    if (page === null) {
      error = 'The server returned an unexpected response shape.'
      return null
    }
    error = ''
    dropped = page.dropped
    return page
  }

  async function poll(signal: AbortSignal) {
    if (pollingFor === signal) return
    pollingFor = signal
    try {
      for (let i = 0; i < MAX_CATCH_UP; i++) {
        const after = loaded ? nextAfter : null
        const page = await read(`?limit=${PAGE}${after ? `&after=${encodeURIComponent(after)}` : ''}`, signal)
        if (page === null) return
        const initial = !loaded
        if (initial) {
          entries = page.entries
          nextBefore = page.nextBefore
          loaded = true
        } else {
          entries = mergeNewer(entries, page.entries)
          // With no cursor yet (the first page was empty) this page is the
          // newest of everything written since, so the lines before it are
          // reachable through Older instead of being skipped.
          if (after === null) nextBefore = page.nextBefore
        }
        if (page.nextAfter !== null) nextAfter = page.nextAfter
        if (initial || page.entries.length < PAGE) return
      }
    } finally {
      if (pollingFor === signal) pollingFor = undefined
    }
  }

  async function older() {
    const before = nextBefore
    const signal = lifetime?.signal
    if (!before || olderBusy || !signal) return
    olderBusy = true
    try {
      const page = await read(`?limit=${PAGE}&before=${encodeURIComponent(before)}`, signal)
      if (page === null) return
      entries = mergeOlder(entries, page.entries)
      nextBefore = page.nextBefore
    } finally {
      olderBusy = false
    }
  }

  $effect(() => {
    void lfdi
    const mine = new AbortController()
    lifetime = mine
    entries = []
    nextBefore = null
    nextAfter = null
    loaded = false
    error = ''
    pollingFor = undefined
    untrack(() => void poll(mine.signal))
    const timer = setInterval(() => {
      nowMs = Date.now()
      void poll(mine.signal)
    }, POLL_MS)
    return () => {
      clearInterval(timer)
      mine.abort()
    }
  })

  function via(actor: string): string {
    if (actor === '' || actor.toLowerCase() === lfdi.toLowerCase()) return ''
    return deviceLabelIn(devices, actor)
  }
</script>

<div class="logs" id="device-logs-{deviceId}" data-testid="device-logs-{deviceId}">
  <div class="logs-head">
    <strong>Logs</strong>
    <label>
      Kind
      <select bind:value={kind} data-testid="device-logs-kind">
        <option value="all">All</option>
        <option value="request">Requests</option>
        <option value="logevent">Log events</option>
        <option value="refused">Refused</option>
      </select>
    </label>
    <label><input type="checkbox" bind:checked={errorsOnly} data-testid="device-logs-errors" /> Errors only</label>
    <a
      class="btn btn-small"
      href={logPath(lfdi, '/download')}
      download
      data-testid="device-logs-download"
      aria-label="Download log of device {deviceId}">Download</a
    >
    <button class="btn btn-small" aria-label="Close logs" onclick={onClose}>Close</button>
  </div>

  {#if error}
    <div class="result err" role="alert" data-testid="device-logs-error">{error}</div>
  {/if}
  {#if dropped > 0}
    <p class="hint" data-testid="device-logs-dropped">The writer has lost {dropped} lines since it started.</p>
  {/if}

  {#if !loaded && !error}
    <p class="hint">Loading log...</p>
  {:else if loaded}
    {#if shown.length === 0}
      <p class="hint" data-testid="device-logs-empty">
        {entries.length === 0 ? 'No lines yet.' : 'No loaded line matches the filters.'}
      </p>
    {:else}
      <table>
        <caption class="visually-hidden">Log of device {deviceId}</caption>
        <thead>
          <tr><th>Time</th><th>Kind</th><th>Request</th><th>Status</th><th>ms</th><th>Detail</th><th>Made by</th></tr>
        </thead>
        <tbody>
          {#each shown as r (r.cursor)}
            {@const by = via(r.actor)}
            <tr data-testid="device-log-row">
              <td><time datetime={r.t} title={r.t}>{formatAge(r.t === '' ? null : r.t, nowMs)}</time></td>
              <td>{r.kind}</td>
              <td class="mono">{r.method} {r.path}</td>
              <td class={r.status !== null && r.status >= 400 ? 'offline' : ''} data-testid="device-log-status">{r.status ?? ''}</td>
              <td>{r.durMs ?? ''}</td>
              <td>{r.detail}</td>
              <td data-testid="device-log-via">
                {#if by !== ''}via {by}{/if}{#if r.source !== ''}{by !== '' ? ', ' : ''}{r.source}{/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
    {#if nextBefore !== null}
      <button class="btn btn-small" data-testid="device-logs-older" disabled={olderBusy} onclick={older}>Older</button>
    {/if}
  {/if}
</div>

<style>
  .logs {
    margin: 4px 0 8px 0;
  }

  .logs-head {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
  }

  .visually-hidden {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }

  select {
    background: var(--bg);
    border: 1px solid var(--border);
    color: var(--text);
    padding: 2px 4px;
    font-size: 12px;
  }
</style>
