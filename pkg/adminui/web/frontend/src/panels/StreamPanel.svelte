<script lang="ts">
  // A panel that serves a live feed. The effect owns the EventSource: its
  // teardown closes the stream when Stop, a new submit, a panel or tab
  // change, or unmount ends it, so a swap never leaves a stream open
  // holding one of the server's few stream slots.
  import type { PanelStreamInfo } from '../lib/descriptor'
  import {
    appendLine,
    parseStreamEvent,
    streamURL,
    validateStreamParam,
    type StreamLine,
  } from '../lib/stream'

  let { id, label, stream }: { id: string; label: string; stream: PanelStreamInfo } = $props()

  let param = $state('')
  let validation = $state('')
  let run = $state<{ param: string; n: number } | null>(null)
  let lines = $state.raw<StreamLine[]>([])
  let connection = $state<'idle' | 'open' | 'reconnecting' | 'closed'>('idle')
  let nextKey = 0

  function submit(event: SubmitEvent) {
    event.preventDefault()
    const problem = validateStreamParam(param, stream)
    validation = problem ?? ''
    if (problem !== null) return
    lines = []
    run = { param, n: (run?.n ?? 0) + 1 }
  }

  function stop() {
    run = null
  }

  function add(kind: StreamLine['kind'], text: string, time: string) {
    lines = appendLine(lines, { key: nextKey++, kind, text, time })
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
      if (parsed === null) add('status', 'The server sent an event that could not be read.', '')
      else add(parsed.kind, parsed.text, parsed.time)
    }
    // The browser retries a dropped stream by itself and resumes from the
    // last event id; a CLOSED source is a refusal it will not retry.
    source.onerror = () => {
      if (source.readyState === EventSource.CLOSED) {
        connection = 'closed'
        add('status', 'The server refused this stream or closed it. Check the value and submit again.', '')
      } else {
        connection = 'reconnecting'
      }
    }
    return () => {
      source.close()
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
  <div class="result" data-testid="stream-connection">{connection}</div>
  <ol class="stream-log" data-testid="stream-log" aria-label="Stream output">
    {#each lines as line (line.key)}
      <li class="stream-line" class:stream-status={line.kind === 'status'} data-kind={line.kind}>
        {#if line.time}<time class="mono" datetime={line.time}>{line.time}</time>{/if}
        <bdi>{line.text}</bdi>
      </li>
    {/each}
  </ol>
</div>
