<script lang="ts">
  // Checkbox list of the devices an aggregator may manage. It loads the
  // current pairs itself, because who is already managed decides who is
  // offered (pickableDevices). `held` is what the manager holds when the
  // pairs loaded, and `selected` starts equal to it, so a parent can diff
  // the two on save.
  import { onDestroy, untrack } from 'svelte'
  import type { DashboardDevice } from '../lib/dashboard'
  import { canon, deviceLabel } from '../lib/devices'
  import { listPairs, pairsKey, pickableDevices, type ManagementPair } from '../lib/management'

  let {
    devices,
    manager = '',
    selected = $bindable([]),
    held = $bindable([]),
    loaded = $bindable(false),
    asAggregator = true,
  }: {
    devices: DashboardDevice[]
    manager?: string
    selected?: string[]
    held?: string[]
    loaded?: boolean
    // false offers only the devices already held, so they can be unticked
    // while the kind is being changed away from aggregator.
    asAggregator?: boolean
  } = $props()

  let pairs = $state<ManagementPair[]>([])
  let loadError = $state('')
  let loadSeq = 0
  let seeded = false

  const lfdis = $derived(devices.map((d) => d.lfdi))
  const loadKey = $derived(pairsKey(lfdis))
  const pickable = $derived(pickableDevices(devices, pairs, manager))
  const options = $derived(pickable.filter((d) => asAggregator || held.includes(canon(d.lfdi))))
  const visible = $derived(asAggregator || held.length > 0)

  // A pick that is no longer offered (its device got a manager, or left the
  // list) must not be posted from a form that no longer shows it.
  $effect(() => {
    if (!loaded) return
    const offered = new Set(pickable.map((d) => canon(d.lfdi)))
    untrack(() => {
      if (selected.some((s) => !offered.has(s))) selected = selected.filter((s) => offered.has(s))
    })
  })

  async function load(ids: string[]) {
    const seq = ++loadSeq
    const res = await listPairs(ids)
    if (seq !== loadSeq) return
    if (!res.ok) {
      loadError = res.error
      return
    }
    loadError = ''
    pairs = res.data
    const self = manager ? canon(manager) : ''
    held = self ? res.data.filter((p) => p.managerLFDI === self).map((p) => p.managedLFDI) : []
    if (!seeded) {
      selected = [...held]
      seeded = true
    }
    loaded = true
  }

  $effect(() => {
    void loadKey
    untrack(() => void load(lfdis))
  })

  onDestroy(() => {
    loadSeq++
  })

  function toggle(lfdi: string, on: boolean) {
    const key = canon(lfdi)
    selected = on ? [...selected.filter((s) => s !== key), key] : selected.filter((s) => s !== key)
  }
</script>

{#if visible}
<fieldset class="picker" data-testid="device-picker">
  <legend>Managed devices</legend>
  {#if loadError}
    <div class="result err" role="alert" data-testid="device-picker-error">{loadError}</div>
  {:else if !loaded}
    <span class="stat-label">Loading...</span>
  {:else if options.length === 0}
    <span class="stat-label" data-testid="device-picker-empty">No unmanaged devices to pick</span>
  {/if}
  {#each loaded ? options : [] as d (d.lfdi)}
    <label class="picker-option">
      <input
        type="checkbox"
        data-testid="device-picker-option"
        checked={selected.includes(canon(d.lfdi))}
        onchange={(e) => toggle(d.lfdi, e.currentTarget.checked)}
      />
      {deviceLabel(d.lfdi, d.sfdi)}
    </label>
  {/each}
</fieldset>
{/if}

<style>
  .picker {
    border: 1px solid var(--border);
    margin: 4px 0;
    padding: 4px 8px;
  }
  .picker-option {
    display: block;
    font-size: 12px;
  }
</style>
