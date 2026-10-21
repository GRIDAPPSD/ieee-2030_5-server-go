<script lang="ts">
  // Management pairs: which aggregator manages which device. A device
  // answers its protocol reads only for its own manager's pairs, so this is
  // the operator's way to hand devices to an aggregator. Both LFDIs are
  // chosen from the registered device list, never typed, so a pair cannot
  // name a device the server does not hold.
  import { onDestroy, untrack } from 'svelte'
  import type { DashboardDevice } from '../lib/dashboard'
  import { deviceLabel } from '../lib/devices'
  import { createPair, listPairs, pairsKey, removePair, type ManagementPair } from '../lib/management'

  // ready is false until the shell has received its first device list, so an
  // empty list before then is not mistaken for a server with no devices.
  let { devices, ready }: { devices: DashboardDevice[]; ready: boolean } = $props()

  let pairs = $state<ManagementPair[]>([])
  let loaded = $state(false)
  let listError = $state('')
  let manager = $state('')
  let managed = $state('')
  let busy = $state(false)
  let result = $state('')
  let ok = $state(false)

  const lfdis = $derived(devices.map((d) => d.lfdi))
  // null until the device list is ready; a string equal across reorders and
  // case changes, so the effect below reruns only when the LFDI set does.
  const loadKey = $derived(ready ? pairsKey(lfdis) : null)

  // A newer load, or the panel going away, makes an older answer stale.
  let loadSeq = 0

  async function load(ids: string[]) {
    const seq = ++loadSeq
    const res = await listPairs(ids)
    if (seq !== loadSeq) return
    loaded = true
    if (res.ok) {
      pairs = res.data
      listError = ''
    } else {
      listError = res.error
    }
  }

  $effect(() => {
    if (loadKey === null) return
    untrack(() => void load(lfdis))
  })

  // A chosen device that leaves the list must not be posted from a select
  // that no longer shows it.
  $effect(() => {
    const present = new Set(lfdis)
    untrack(() => {
      if (manager && !present.has(manager)) manager = ''
      if (managed && !present.has(managed)) managed = ''
    })
  })

  onDestroy(() => {
    loadSeq++
  })

  async function create() {
    if (busy) return
    if (!manager || !managed) {
      ok = false
      result = 'Choose a manager and a managed device.'
      return
    }
    if (manager === managed) {
      ok = false
      result = 'A device cannot manage itself.'
      return
    }
    busy = true
    try {
      const res = await createPair(manager, managed)
      if (!res.ok) {
        ok = false
        result = `Error (${res.status}): ${res.error}`
        return
      }
      ok = true
      result = `Paired ${res.data.managedLFDI} under ${res.data.managerLFDI}.`
      managed = ''
      await load(lfdis)
    } finally {
      busy = false
    }
  }

  async function remove(pair: ManagementPair) {
    if (busy) return
    busy = true
    try {
      const res = await removePair(pair.managedLFDI)
      if (!res.ok) {
        ok = false
        result = `Error (${res.status}): ${res.error}`
        return
      }
      ok = true
      result = `Removed the pair for ${pair.managedLFDI}.`
      await load(lfdis)
    } finally {
      busy = false
    }
  }

  // pairLabel names a pair's device from the registered list, so its SFDI is
  // used when it has no name; a LFDI not in the list falls back to its prefix.
  function pairLabel(lfdi: string): string {
    const d = devices.find((x) => x.lfdi.toUpperCase() === lfdi.toUpperCase())
    return deviceLabel(lfdi, d?.sfdi)
  }

  function option(d: DashboardDevice): string {
    return `${deviceLabel(d.lfdi, d.sfdi)} (${d.lfdi.substring(0, 8)}...)`
  }
</script>

<div class="card full-width">
  <h2>Management Pairs</h2>
  <p class="hint">
    An aggregator reads a device only when a pair names it as that device's manager. Each device has one manager.
  </p>
  <table>
    <thead>
      <tr><th>Manager</th><th>Managed device</th><th>Manager LFDI</th><th>Managed LFDI</th><th></th></tr>
    </thead>
    <tbody>
      {#each pairs as pair (pair.managedLFDI)}
        <tr data-testid="pair-row">
          <td data-testid="pair-manager-label">{pairLabel(pair.managerLFDI)}</td>
          <td data-testid="pair-managed-label">{pairLabel(pair.managedLFDI)}</td>
          <td class="mono" data-testid="pair-manager">{pair.managerLFDI}</td>
          <td class="mono" data-testid="pair-managed">{pair.managedLFDI}</td>
          <td><button
              class="btn btn-small"
              disabled={busy}
              aria-label="Remove pair for {pair.managedLFDI}"
              onclick={() => remove(pair)}>Remove</button></td>
        </tr>
      {/each}
      {#if !ready || !loaded}
        <tr><td colspan="5" class="stat-label" data-testid="pairs-loading">Loading...</td></tr>
      {:else if !listError && pairs.length === 0}
        <tr><td colspan="5" class="stat-label" data-testid="pairs-empty">No management pairs</td></tr>
      {/if}
    </tbody>
  </table>
  {#if listError}
    <div class="result err" role="alert" data-testid="pairs-list-error">{listError}</div>
  {/if}
  <div class="form-row">
    <label for="pairManager">Manager</label>
    <select id="pairManager" bind:value={manager}>
      <option value="">(pick manager)</option>
      {#each devices as d (d.lfdi)}
        <option value={d.lfdi}>{option(d)}</option>
      {/each}
    </select>
    <label for="pairManaged">Managed device</label>
    <select id="pairManaged" bind:value={managed}>
      <option value="">(pick device)</option>
      {#each devices as d (d.lfdi)}
        <option value={d.lfdi}>{option(d)}</option>
      {/each}
    </select>
    <button class="btn btn-green" disabled={busy} onclick={create}>Create pair</button>
  </div>
  {#if result}
    <div class="result" class:ok class:err={!ok} role={ok ? 'status' : 'alert'} data-testid="pairs-result">{result}</div>
  {/if}
</div>
