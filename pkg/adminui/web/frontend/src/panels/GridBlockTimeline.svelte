<script lang="ts">
  // The next 24 hours of grid blocks: discharge above the axis, charge below,
  // a "now" line through both. It draws the entries it is given and owns no
  // data; the pane decides which blocks are shown, so this view and the list
  // beside it cannot disagree. Bar layout, labels and colors are the Overview
  // timeline's (controlEvents.ts, EventBar, the ev-* styles).
  import { barsFor, nowPercent, type Bar } from '../lib/controlEvents'
  import { formatClock } from '../lib/power'
  import { blockEvent, TIMELINE_AHEAD_SECONDS, TIMELINE_BEHIND_SECONDS, type BlockEntry } from '../lib/gridblocks'
  import { gridDirectionWord } from '../lib/gridrows'
  import { formatQuantity } from '../lib/flowreservation'
  import EventBar from './EventBar.svelte'

  let { entries, nowMs }: { entries: BlockEntry[]; nowMs: number } = $props()

  // A bar is never drawn narrower than the room its direction word and amount
  // need, so a short block still reads. Blocks are stacked as if they were
  // that wide, so two short ones close together do not draw over each other.
  const MIN_BAR_PX = 170
  let trackWidth = $state(0)
  let root = $state<HTMLElement | undefined>()

  // The measured width of a track, kept current on resize. Without a
  // ResizeObserver (a test DOM) it stays at its first reading.
  $effect(() => {
    const el = root
    if (el === undefined) return
    const measure = () => (trackWidth = el.querySelector('.ev-track')?.clientWidth ?? 0)
    measure()
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(measure)
    observer.observe(el)
    return () => observer.disconnect()
  })

  const range = $derived<[number, number]>([nowMs - TIMELINE_BEHIND_SECONDS * 1000, nowMs + TIMELINE_AHEAD_SECONDS * 1000])
  const now = $derived(nowPercent(nowMs, range[0], range[1]))

  interface DrawnBar {
    bar: Bar
    entry: BlockEntry
  }

  function lane(direction: 'discharge' | 'charge', r: [number, number]): { bars: DrawnBar[]; height: number } {
    const mine = entries.filter((e) => e.block.direction === direction)
    const byId = new Map(mine.map((e) => [`${e.batch}:${e.block.row}`, e]))
    const events = mine.map(blockEvent)
    const minSeconds = trackWidth > 0 ? (MIN_BAR_PX / trackWidth) * ((r[1] - r[0]) / 1000) : 0
    const rowOf = new Map(
      barsFor(
        events.map((ev) => ({ ...ev, end: Math.max(ev.end, ev.start + minSeconds) })),
        r[0],
        r[1],
      ).map((b) => [b.event.mrid, b.row]),
    )
    const bars = barsFor(events, r[0], r[1]).map((bar) => ({
      bar: { ...bar, row: rowOf.get(bar.event.mrid) ?? bar.row },
      entry: byId.get(bar.event.mrid) as BlockEntry,
    }))
    return { bars, height: Math.max(1, ...bars.map((b) => b.bar.row + 1)) }
  }

  const above = $derived(lane('discharge', range))
  const below = $derived(lane('charge', range))
  const beyond = $derived(entries.filter((e) => e.block.start * 1000 >= range[1]).length)

  // A tick every six hours from now, labelled in the viewer's clock. The
  // six and eighteen hour ticks are dropped on a narrow screen.
  const ticks = $derived(
    [0, 6, 12, 18, 24].map((h) => {
      const ms = nowMs + h * 3600_000
      return { ms, left: ((ms - range[0]) / (range[1] - range[0])) * 100, minor: h === 6 || h === 18 }
    }),
  )
  const clock = (ms: number): string => new Date(ms).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })

  const titleOf = (e: BlockEntry): string =>
    `${gridDirectionWord(e.block.direction)} ${formatQuantity(Math.abs(e.block.encodedW ?? e.block.requestedW ?? NaN), 'W')}, batch ${e.batch} block ${e.block.row}, ${e.block.status}, ${formatClock(e.block.start * 1000)} to ${formatClock(e.block.end * 1000)}`
</script>

{#snippet track(drawn: { bars: DrawnBar[]; height: number }, label: string, testid: string)}
  <div class="ev-lane" data-testid={testid}>
    <span class="ev-lane-label">{label}</span>
    <ul class="ev-track" style:height="calc({drawn.height} * var(--ev-bar-h))">
      {#each drawn.bars as { bar, entry } (bar.event.mrid)}
        <EventBar {bar} title={titleOf(entry)} block={bar.event.mrid} />
      {/each}
      {#if now !== null}<li class="ev-now" aria-hidden="true" style:left="{now}%"></li>{/if}
    </ul>
  </div>
{/snippet}

<section class="events grid-tl" bind:this={root} data-testid="grid-timeline" aria-label="Grid blocks, next 24 hours">
  {@render track(above, 'To the grid (discharge), above the axis', 'grid-timeline-discharge')}
  <div class="ev-axis grid-axis">
    {#each ticks as t (t.ms)}
      <span class="ev-now-label" class:ev-now-end={t.ms === ticks[4].ms} class:minor={t.minor} style:left="{t.left}%">{t.ms === nowMs ? 'now' : clock(t.ms)}</span>
    {/each}
  </div>
  {@render track(below, 'From the grid (charge), below the axis', 'grid-timeline-charge')}
  {#if entries.length === 0}
    <p class="hint ev-empty" data-testid="grid-timeline-empty">No blocks to draw.</p>
  {/if}
  {#if beyond > 0}
    <p class="hint ev-empty" data-testid="grid-timeline-beyond">
      {beyond} block{beyond === 1 ? ' starts' : 's start'} after the next 24 hours and {beyond === 1 ? 'is' : 'are'} listed below only.
    </p>
  {/if}
</section>

<style>
  .grid-tl :global(.ev-bar) {
    min-width: 170px;
  }
  /* Every bar here is DERMS, so the pill only takes room from the label. */
  .grid-tl :global(.ev-derms) {
    display: none;
  }
  .grid-tl :global(.ev-now-label) {
    white-space: nowrap;
  }
  @media (max-width: 800px) {
    .minor {
      display: none;
    }
  }
  .grid-axis {
    height: var(--sp-6);
    border-top: 1px solid var(--chart-axis);
    border-bottom: 1px solid var(--chart-axis);
  }
</style>
