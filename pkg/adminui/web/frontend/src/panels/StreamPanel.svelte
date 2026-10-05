<script lang="ts">
  // A panel that serves a live feed. The effect owns the EventSource: its
  // teardown closes the stream when Stop, a new submit, a panel or tab
  // change, or unmount ends it, so a swap never leaves a stream open
  // holding one of the server's few stream slots.
  import { tick } from 'svelte'
  import type { PanelStreamInfo } from '../lib/descriptor'
  import {
    appendLines,
    parseStreamEvent,
    streamURL,
    validateStreamParam,
    type StreamLine,
  } from '../lib/stream'

  let { id, label, stream }: { id: string; label: string; stream: PanelStreamInfo } = $props()

  type Connection = 'idle' | 'open' | 'reconnecting' | 'ended' | 'error'

  let param = $state('')
  let validation = $state('')
  let run = $state<{ param: string; n: number } | null>(null)
  let lines = $state.raw<StreamLine[]>([])
  let connection = $state<Connection>('idle')
  let endedReason = $state('')
  let logEl = $state<HTMLOListElement>()
  let nextKey = 0
  let pending: StreamLine[] = []
  let flushScheduled = false
  // Follow the newest line until the operator scrolls up from the bottom.
  let follow = true

  const connectionText = $derived(
    connection === 'ended'
      ? `ended: ${endedReason}`
      : connection === 'error'
        ? 'The stream was refused or closed by the server.'
        : connection,
  )

  function submit(event: SubmitEvent) {
    event.preventDefault()
    const problem = validateStreamParam(param, stream)
    validation = problem ?? ''
    if (problem !== null) return
    pending = []
    lines = []
    follow = true
    run = { param, n: (run?.n ?? 0) + 1 }
  }

  function stop() {
    run = null
  }

  function onscroll() {
    if (logEl) follow = logEl.scrollHeight - logEl.scrollTop - logEl.clientHeight < 8
  }

  function flush() {
    flushScheduled = false
    if (pending.length === 0) return
    lines = appendLines(lines, pending)
    pending = []
    if (follow) {
      void tick().then(() => {
        if (follow && logEl) logEl.scrollTop = logEl.scrollHeight
      })
    }
  }

  function add(kind: StreamLine['kind'], text: string, time: string) {
    pending.push({ key: nextKey++, kind, text, time })
    if (!flushScheduled) {
      flushScheduled = true
      queueMicrotask(flush)
    }
  }

  $effect(() => {
    if (run === null) {
      connection = 'idle'
      return
    }
    const source = new EventSource(streamURL(id, run.param))
    connection = 'reconnecting'
    source.onopen = () => {
      connection = 'open'
    }
    source.onmessage = (ev: MessageEvent) => {
      const parsed = parseStreamEvent(ev.data)
      if (parsed === null) {
        add('status', 'The server sent an event that could not be read.', '')
        return
      }
      add(parsed.kind, parsed.text, parsed.time)
      // A bare close makes the browser reconnect, so the plane's last
      // status is the only thing that ends this stream for good.
      if (parsed.final) {
        source.close()
        endedReason = parsed.text
        connection = 'ended'
      }
    }
    // The browser retries a dropped stream by itself and resumes from the
    // last event id. EventSource hides the status code, so a CLOSED source
    // (a refusal it will not retry) cannot be told apart by cause.
    source.onerror = () => {
      if (source.readyState === EventSource.CLOSED) {
        connection = 'error'
        add('status', 'The stream was refused or closed by the server.', '')
      } else {
        connection = 'reconnecting'
      }
    }
    return () => {
      source.close()
      pending = []
    }
  })
</script>

<div class="card full-width" data-testid="stream-panel">
  <h2>{label}</h2>
  <form class="form-row" onsubmit={submit}>
    <label for="stream-param">Parameter</label>
    <input
      type="text"
      id="stream-param"
      maxlength={stream.maxLen}
      autocomplete="off"
      spellcheck="false"
      bind:value={param}
    />
    <button class="btn" type="submit">Start</button>
    <button class="btn" type="button" onclick={stop} disabled={run === null}>Stop</button>
  </form>
  <p class="hint">Up to {stream.maxLen} characters from: <span class="mono">{stream.charset}</span></p>
  {#if validation}
    <div class="result err" role="alert" data-testid="stream-validation">{validation}</div>
  {/if}
  <div class="result" data-testid="stream-connection" data-state={connection}>{connectionText}</div>
  <ol class="stream-log" data-testid="stream-log" aria-label="Stream output" aria-live="polite" bind:this={logEl} {onscroll}>
    {#each lines as line (line.key)}
      <li class="stream-line" class:stream-status={line.kind === 'status'} data-kind={line.kind}>
        {#if line.time}<time class="mono" datetime={line.time}>{line.time}</time>{/if}
        <bdi>{line.text}</bdi>
      </li>
    {/each}
  </ol>
</div>
