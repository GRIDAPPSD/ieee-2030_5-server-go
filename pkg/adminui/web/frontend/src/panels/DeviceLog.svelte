<script lang="ts">
  // The log of one device from GET /api/device-logs/{lfdi}, read like a tail:
  // oldest at the top, newest at the bottom. It polls every 5 s while mounted,
  // so leaving the tab stops the polling. Lines sit in their own scroll box. A
  // poll never touches what the reader is looking at: at the bottom its lines
  // append and the view stays pinned, anywhere else they wait behind a
  // control. Older lines load when the top of the box is reached.
  import { tick, untrack } from 'svelte'
  import { fetchJSON } from '../lib/api'
  import { formatAge } from '../lib/comms'
  import { deviceKind, deviceLabelIn } from '../lib/devices'
import { followBottom } from '../lib/followBottom'
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

  let { lfdi, devices }: { lfdi: string; devices: { lfdi: string; sfdi?: string }[] } = $props()

  const PAGE = 200
  const POLL_MS = 5000
  const FETCH_TIMEOUT_MS = 20000
  // A poll that returns a full page asks again at once, at most this often
  // per tick, so a long gap is caught up without an unbounded loop.
  const MAX_CATCH_UP = 5

  // Newest first, as the server returns them; the table shows them reversed.
  let entries = $state<LogEntry[]>([])
  let nextBefore = $state<string | null>(null)
  let nextAfter = $state<string | null>(null)
  let dropped = $state(0)
  let loaded = $state(false)
  let error = $state('')
  let olderBusy = $state(false)
  // A failed Older read stops the automatic loading: the top of the box stays
  // in view, so the observer would otherwise ask again at once.
  let olderFailed = $state(false)
  // The message of that failure, kept while Retry is shown: a later poll
  // clears the shared error, and Retry would otherwise stand with no reason.
  let olderError = $state('')
  // The last older page added no line the filter shows. Auto-load pauses, so
  // a filter that hides everything does not page through the whole log; a
  // click on "Load older lines" or a filter change picks it up again.
  let stalled = $state(false)
  // Lines a poll delivered while the reader was away from the bottom, newest first.
  let pending = $state<LogEntry[]>([])
  let scroller = $state<HTMLElement>()
  const follow = followBottom(() => scroller)
  let sentinel = $state<HTMLElement>()
  let kind = $state<KindFilter>('all')
  // The switch is offered for an aggregator only. scope=managed adds each
  // managed device's lines, so the view then names the device of each line.
  let scope = $state<'own' | 'managed'>('own')
  const aggregator = $derived(deviceKind(lfdi) === 'aggregator')
  const managed = $derived(aggregator && scope === 'managed')
  let errorsOnly = $state(false)
  let nowMs = $state(Date.now())

  const rows = $derived(entries.map(logRow))
  const shown = $derived(visible(rows, kind, errorsOnly))
  const ordered = $derived([...shown].reverse())
  const label = $derived(deviceLabelIn(devices, lfdi))

  // The controller of the current device; replaced when the device changes
  // and aborted when the component unmounts, which also drops a reply in flight.
  let lifetime: AbortController | undefined
  // The signal of the run whose poll is out. A poll of a replaced run must
  // not clear it, or the new run's poll would be joined by a second one.
  let pollingFor: AbortSignal | undefined

  function failure(status: number, message: string): string {
    if (status === 404) return 'No log yet for this device.'
    if (status === 503) return `The device log is unavailable: ${message}`
    return `Could not load the log (${status}): ${message}`
  }

  function query(extra = ''): string {
    return `?limit=${PAGE}${managed ? '&scope=managed' : ''}${extra}`
  }

  async function read(path: string, signal: AbortSignal) {
    const res = await fetchJSON<unknown>(logPath(lfdi, path), { signal, timeoutMs: FETCH_TIMEOUT_MS })
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
        const page = await read(query(after ? `&after=${encodeURIComponent(after)}` : ''), signal)
        if (page === null) return
        const initial = !loaded
        if (initial) {
          entries = page.entries
          nextBefore = page.nextBefore
          loaded = true
          follow.pinAfterUpdate()
        } else {
          absorb(page.entries)
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

  // absorb shows a poll's lines now when the reader is at the bottom, and holds
  // them back otherwise so the rows under the reader do not move.
  function absorb(fresh: LogEntry[]) {
    if (follow.following) {
      entries = mergeNewer(entries, fresh)
      follow.pinAfterUpdate()
      return
    }
    const have = new Set(entries.map((e) => e.cursor))
    pending = mergeNewer(pending, fresh.filter((e) => !have.has(e.cursor)))
  }

  function showPending() {
    entries = mergeNewer(entries, pending)
    pending = []
    follow.pinAfterUpdate()
  }

  function onScroll() {
    follow.onscroll()
    if (follow.following && pending.length > 0) showPending()
  }

  function jumpToNew() {
    follow.follow()
    showPending()
  }

  async function older() {
    const before = nextBefore
    const signal = lifetime?.signal
    if (!before || olderBusy || !signal) return
    olderBusy = true
    try {
      const page = await read(query(`&before=${encodeURIComponent(before)}`), signal)
      if (page === null) {
        if (!signal.aborted) {
          olderFailed = true
          olderError = error
        }
        return
      }
      const held = new Set(entries.map((e) => e.cursor))
      const added = visible(page.entries.filter((e) => !held.has(e.cursor)).map(logRow), kind, errorsOnly)
      // The lines land above the reader, so the box is moved by the height
      // they add to keep the line under the reader where it was.
      const height = scroller?.scrollHeight ?? 0
      const top = scroller?.scrollTop ?? 0
      entries = mergeOlder(entries, page.entries)
      nextBefore = page.nextBefore
      stalled = added.length === 0
      await tick()
      if (scroller && !signal.aborted) scroller.scrollTop = top + (scroller.scrollHeight - height)
    } finally {
      olderBusy = false
    }
  }

  // The parent's lfdi prop is read through a derived pick that gets a new
  // object on every dashboard frame; reading it here directly would restart
  // the log each time. A derived string only notifies when the text changes.
  const device = $derived(lfdi)

  $effect(() => {
    void device
    void managed
    const mine = new AbortController()
    lifetime = mine
    entries = []
    nextBefore = null
    nextAfter = null
    loaded = false
    error = ''
    pending = []
    follow.follow()
    olderFailed = false
    olderError = ''
    stalled = false
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

  function retryOlder() {
    olderFailed = false
    olderError = ''
    void older()
  }

  $effect(() => {
    void kind
    void errorsOnly
    stalled = false
  })

  const pendingShown = $derived(visible(pending.map(logRow), kind, errorsOnly).length)

  // One observer at a time, and none while a read is out, so Older pages load
  // one request at a time. Making a new observer once the read lands makes it
  // report the sentinel again, which keeps loading while the top is in view.
  $effect(() => {
    if (!sentinel || !scroller || nextBefore === null || olderBusy || olderFailed || stalled) return
    if (typeof IntersectionObserver === 'undefined') return
    const seen = new IntersectionObserver(
      (hits) => {
        if (hits.some((h) => h.isIntersecting)) void older()
      },
      { root: scroller },
    )
    seen.observe(sentinel)
    return () => seen.disconnect()
  })

  function via(actor: string): string {
    if (managed || actor === '' || actor.toLowerCase() === lfdi.toLowerCase()) return ''
    return deviceLabelIn(devices, actor)
  }
</script>

<div class="logs" data-testid="device-logs">
  <div class="logs-head">
    <strong>Logs</strong>
    <label>
      Kind
      <select class="compact-select" bind:value={kind} data-testid="device-logs-kind">
        <option value="all">All</option>
        <option value="request">Requests</option>
        <option value="logevent">Log events</option>
        <option value="refused">Refused</option>
      </select>
    </label>
    {#if aggregator}
      <span role="radiogroup" aria-label="Whose requests" data-testid="device-logs-scope">
        <label><input type="radio" name="logs-scope" value="own" bind:group={scope} /> Own requests</label>
        <label><input type="radio" name="logs-scope" value="managed" bind:group={scope} /> Own and managed devices</label>
      </span>
    {/if}
    <label><input type="checkbox" bind:checked={errorsOnly} data-testid="device-logs-errors" /> Errors only</label>
    <a
      class="btn btn-small"
      href={logPath(lfdi, '/download')}
      download
      data-testid="device-logs-download"
      aria-label="Download log of device {label}">Download</a
    >
  </div>

  {#if error}
    <div class="result err" role="alert" data-testid="device-logs-error">{error}</div>
  {/if}
  {#if dropped > 0}
    <p class="hint" data-testid="device-logs-dropped">The writer has lost {dropped} lines since it started.</p>
  {/if}

  {#if pendingShown > 0}
    <button class="btn btn-small" data-testid="device-logs-new" onclick={jumpToNew}>
      {pendingShown} new {pendingShown === 1 ? 'line' : 'lines'}
    </button>
  {/if}

  {#if !loaded && !error}
    <p class="hint">Loading log...</p>
  {:else if loaded}
    {#if nextBefore !== null}
      {#if olderBusy}
        <p class="hint">Loading older lines...</p>
      {:else if olderFailed}
        <p class="result err" data-testid="device-logs-older-error">{olderError}</p>
        <button class="btn btn-small" data-testid="device-logs-older-retry" onclick={retryOlder}>Retry older lines</button>
      {:else if stalled}
        <button class="btn btn-small" data-testid="device-logs-older-manual" onclick={older}>Load older lines</button>
      {/if}
    {:else if entries.length > 0}
      <p class="hint" data-testid="device-logs-start">Start of the log.</p>
    {/if}
    <div class="log-scroll" bind:this={scroller} onscroll={onScroll} data-testid="device-logs-scroll">
    {#if nextBefore !== null}
      <div bind:this={sentinel} class="log-end" data-testid="device-logs-more" aria-hidden="true"></div>
    {/if}
    {#if shown.length === 0}
      <p class="hint" data-testid="device-logs-empty">
        {entries.length === 0 ? 'No lines yet.' : 'No loaded line matches the filters.'}
      </p>
    {:else}
      <table>
        <caption class="visually-hidden">Log of device {label}</caption>
        <thead>
          <tr><th>Time</th><th>Kind</th><th>Request</th><th>Status</th><th>ms</th><th>Detail</th>{#if managed}<th>Device</th>{/if}<th>Made by</th></tr>
        </thead>
        <tbody>
          {#each ordered as r (r.cursor)}
            {@const by = via(r.actor)}
            <tr data-testid="device-log-row">
              <td><time datetime={r.t} title={r.t}>{formatAge(r.t === '' ? null : r.t, nowMs)}</time></td>
              <td>{r.kind}</td>
              <td class="mono">{r.method} {r.path}</td>
              <td class={r.status !== null && r.status >= 400 ? 'offline' : ''} data-testid="device-log-status">{r.status ?? ''}</td>
              <td>{r.durMs ?? ''}</td>
              <td>{r.detail}</td>
              {#if managed}
                <td data-testid="device-log-device">{deviceLabelIn(devices, r.actor)}{r.source === 'unregistered' ? ' (unregistered)' : ''}</td>
              {/if}
              <td data-testid="device-log-via">
                {#if by !== ''}via {by}{/if}{#if r.source !== ''}{by !== '' ? ', ' : ''}{r.source}{/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
    </div>
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

  .log-scroll {
    max-height: 70vh;
    overflow-y: auto;
    /* The box is moved by hand when older lines land above the reader. */
    overflow-anchor: none;
  }

  .log-end {
    height: 1px;
  }

  .visually-hidden {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }
</style>
