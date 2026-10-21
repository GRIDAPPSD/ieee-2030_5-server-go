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
  }: {
    devices: DashboardDevice[]
    manager?: string
    selected?: string[]
    held?: string[]
    loaded?: boolean
  } = $props()

  let pairs = $state<ManagementPair[]>([])
  let loadError = $state('')
  let loadSeq = 0
  let seeded = false

  const lfdis = $derived(devices.map((d) => d.lfdi))
  const loadKey = $derived(pairsKey(lfdis))
  const options = $derived(pickableDevices(devices, pairs, manager))

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

<fieldset class="picker" data-testid="device-picker">
  <legend>Managed devices</legend>
  {#if loadError}
    <div class="result err" role="alert" data-testid="device-picker-error">{loadError}</div>
  {:else if !loaded}
    <span class="stat-label">Loading...</span>
  {:else if options.length === 0}
    <span class="stat-label" data-testid="device-picker-empty">No unmanaged devices to pick</span>
  {/if}
  {#each options as d (d.lfdi)}
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
