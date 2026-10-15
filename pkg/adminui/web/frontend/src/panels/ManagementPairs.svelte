<script lang="ts">
  // Management pairs: which aggregator manages which device. A device
  // answers its protocol reads only for its own manager's pairs, so this is
  // the operator's way to hand devices to an aggregator. Both LFDIs are
  // chosen from the registered device list, never typed, so a pair cannot
  // name a device the server does not hold.
  import { onDestroy, untrack } from 'svelte'
  import type { DashboardDevice } from '../lib/dashboard'
  import { createPair, listPairs, pairsKey, removePair, type ManagementPair } from '../lib/management'

  let { devices }: { devices: DashboardDevice[] } = $props()

  let pairs = $state<ManagementPair[]>([])
  let loaded = $state(false)
  let listError = $state('')
  let manager = $state('')
  let managed = $state('')
  let result = $state('')
  let ok = $state(false)

  const lfdis = $derived(devices.map((d) => d.lfdi))
  const devicesKey = $derived(pairsKey(lfdis))

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
    void devicesKey
    untrack(() => void load(lfdis))
  })

  onDestroy(() => {
    loadSeq++
  })

  async function create() {
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
  }

  async function remove(pair: ManagementPair) {
    const res = await removePair(pair.managedLFDI)
    if (!res.ok) {
      ok = false
      result = `Error (${res.status}): ${res.error}`
      return
    }
    ok = true
    result = `Removed the pair for ${pair.managedLFDI}.`
    await load(lfdis)
  }

  function option(d: DashboardDevice): string {
    return `${d.sfdi} (${d.lfdi.substring(0, 8)}...)`
  }
</script>

<div class="card full-width">
  <h2>Management Pairs</h2>
  <p class="hint">
    An aggregator reads a device only when a pair names it as that device's manager. Each device has one manager.
  </p>
  <table>
    <thead>
      <tr><th>Manager LFDI</th><th>Managed LFDI</th><th></th></tr>
    </thead>
    <tbody>
      {#each pairs as pair (pair.managedLFDI)}
        <tr data-testid="pair-row">
          <td class="mono" data-testid="pair-manager">{pair.managerLFDI}</td>
          <td class="mono" data-testid="pair-managed">{pair.managedLFDI}</td>
          <td><button class="btn btn-small" onclick={() => remove(pair)}>Remove</button></td>
        </tr>
      {/each}
      {#if loaded && !listError && pairs.length === 0}
        <tr><td colspan="3" class="stat-label" data-testid="pairs-empty">No management pairs</td></tr>
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
    <button class="btn btn-green" onclick={create}>Create pair</button>
  </div>
  {#if result}
    <div class="result" class:ok class:err={!ok} role={ok ? 'status' : 'alert'} data-testid="pairs-result">{result}</div>
  {/if}
</div>
