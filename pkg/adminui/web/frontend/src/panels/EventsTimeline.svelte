<script lang="ts">
  // The scheduled-events timeline under the Overview energy chart. The effect
  // owns the poll timer and the in-flight read; its teardown runs when the
  // window changes or the card is destroyed, so leaving the tab stops polling.
  // The time range comes from the card, so the bars line up with the chart
  // above them.
  import { fetchJSON } from '../lib/api'
  import { PANEL_REQUEST_TIMEOUT_MS } from '../lib/descriptor'
  import {
    barsFor,
    controlEventsPath,
    eventLabelParts,
    eventTitle,
    lanesFor,
    nowPercent,
    parseControlEvents,
    type ControlEventsAnswer,
  } from '../lib/controlEvents'
  import { deviceLabel } from '../lib/devices'
  import { powerBucketMs, type PowerAnswer } from '../lib/power'
  import { startPoll } from '../lib/poll'

  // ahead is how far past now the shown events reach, in seconds; the card
  // reads it to extend the range of every chart. nowMs is the card's "now".
  let {
    answer,
    windowSeconds,
    range,
    nowMs,
    ahead = $bindable(0),
  }: { answer: PowerAnswer; windowSeconds: number; range: [number, number]; nowMs: number; ahead?: number } = $props()

  let events = $state<ControlEventsAnswer | null>(null)
  // True once an older server answered 404: the route is absent, so the
  // timeline draws nothing and stops asking.
  let absent = $state(false)
  let error = $state('')

  $effect(() => {
    const win = windowSeconds
    events = null
    ahead = 0
    absent = false
    error = ''
    return startPoll(async (signal) => {
      const res = await fetchJSON<unknown>(controlEventsPath(win), { signal, timeoutMs: PANEL_REQUEST_TIMEOUT_MS })
      if (signal.aborted) return undefined
      if (!res.ok && res.status === 404) {
        absent = true
        ahead = 0
        return undefined
      }
      const parsed = res.ok ? parseControlEvents(res.data) : null
      if (parsed !== null) {
        events = parsed
        ahead = parsed.aheadSeconds ?? 0
        error = ''
      } else {
        error = res.ok ? 'The server sent a scheduled events reply that could not be read.' : `Could not load scheduled events: ${res.error}`
      }
      return powerBucketMs(win)
    })
  })

  const lanes = $derived(lanesFor(answer, events?.events ?? [], deviceLabel))
  const laneBars = $derived(
    lanes.map((lane) => ({
      lane,
      bars: barsFor(
        (events?.events ?? []).filter((e) => e.group === lane.key),
        range[0],
        range[1],
      ),
    })),
  )
  const now = $derived(nowPercent(nowMs, range[0], range[1]))
  const empty = $derived(events !== null && events.events.length === 0)
</script>

{#if !absent}
  <section class="events" data-testid="events-timeline" aria-label="Scheduled events">
    {#each laneBars as { lane, bars } (lane.key)}
      <div class="ev-lane" data-testid="events-lane">
        <span class="ev-lane-label" data-testid="events-lane-label">{lane.label}</span>
        <ul class="ev-track" style:height="calc({Math.max(1, ...bars.map((b) => b.row + 1))} * var(--ev-bar-h))">
          {#each bars as bar (bar.event.mrid + ':' + bar.event.group + ':' + (bar.event.fsaID ?? '') + ':' + bar.event.start)}
            <li
              class="ev-bar ev-{bar.event.status} ev-dir-{bar.event.direction ?? 'none'}"
              data-testid="events-bar"
              data-status={bar.event.status}
              data-origin={bar.event.origin}
              style:left="{bar.left}%"
              style:width="{bar.width}%"
              style:top="calc({bar.row} * var(--ev-bar-h))"
              title={eventTitle(bar.event)}
              aria-label={eventTitle(bar.event)}
            >
              {#if bar.event.origin === 'derms'}<span class="ev-derms" data-testid="events-derms">DERMS</span>{/if}
              <span class="ev-text">{eventLabelParts(bar.event)[0]}<span class="ev-detail">{eventLabelParts(bar.event)[1]}</span></span>
            </li>
          {/each}
          {#if now !== null}
            <li class="ev-future" aria-hidden="true" style:left="{now}%"></li>
            <li class="ev-now" aria-hidden="true" style:left="{now}%"></li>
          {/if}
        </ul>
      </div>
    {/each}
    <div class="ev-axis">
      {#if now !== null}<span class="ev-now-label" class:ev-now-end={now > 85} style:left="{now}%">now</span>{/if}
    </div>
    {#if empty}
      <p class="hint ev-empty" data-testid="events-empty">No scheduled events in this window.</p>
    {/if}
    {#if error}
      <p class="hint" role="status" data-testid="events-error">{error}</p>
    {/if}
  </section>
{/if}
