<script lang="ts">
  // One bar of an events timeline, shared by the Overview timeline and the
  // DERMS block timeline so both draw an event the same way. The caller owns
  // the lane, the placement (the Bar) and any title that differs from the
  // event's own.
  import { eventLabelParts, eventTitle, type Bar } from '../lib/controlEvents'

  let { bar, title = undefined, block = undefined }: { bar: Bar; title?: string; block?: string } = $props()

  const label = $derived(eventLabelParts(bar.event))
  const text = $derived(title ?? eventTitle(bar.event))
</script>

<li
  class="ev-bar ev-{bar.event.status} ev-dir-{bar.event.direction ?? 'none'}"
  data-testid="events-bar"
  data-status={bar.event.status}
  data-origin={bar.event.origin}
  data-block={block}
  style:left="{bar.left}%"
  style:width="{bar.width}%"
  style:top="calc({bar.row} * var(--ev-bar-h))"
  title={text}
  aria-label={text}
>
  {#if bar.event.origin === 'derms'}<span class="ev-derms" data-testid="events-derms">DERMS</span>{/if}
  <span class="ev-text"><span class="ev-lead" class:ev-dirword={label.word}>{label.lead}</span><span class="ev-detail" data-testid="events-detail">{label.detail}</span></span>
</li>
