<script lang="ts">
  // The operator dashboard. This route owns the three pieces of shared
  // state the panels read (the SSE payload, the FSA catalog, the topology
  // tree) and the one refresh path that reloads the last two, so a
  // create/attach/assign in any panel updates every panel that shows the
  // result.
  import { onDestroy, onMount } from 'svelte'
  import { fetchJSON } from '../lib/api'
  import {
    appendHistory,
    connectDashboard,
    type DashboardData,
    type HistoryPoint,
  } from '../lib/dashboard'
  import type { AdminFSA, AdminFSAList, TopologyNode } from '../lib/fsa'
  import NavBar from '../panels/NavBar.svelte'
  import Overview from '../panels/Overview.svelte'
  import ServerInfo from '../panels/ServerInfo.svelte'
  import CertPanel from '../panels/CertPanel.svelte'
  import DerControl from '../panels/DerControl.svelte'
  import AddDevice from '../panels/AddDevice.svelte'
  import LookupDevice from '../panels/LookupDevice.svelte'
  import CreateFsa from '../panels/CreateFsa.svelte'
  import FsaCatalog from '../panels/FsaCatalog.svelte'
  import TopologyTree from '../panels/TopologyTree.svelte'
  import DeviceTable from '../panels/DeviceTable.svelte'
  import ActivityChart from '../panels/ActivityChart.svelte'
  import LoginPanel from '../panels/LoginPanel.svelte'

  let data = $state<DashboardData | null>(null)
  let history = $state<HistoryPoint[]>([])
  let fsas = $state<AdminFSA[]>([])
  let topology = $state<TopologyNode | null>(null)
  let topologyError = $state('')
  let unauthorized = $state('')
  let disconnect: (() => void) | null = null

  async function refresh() {
    const list = await fetchJSON<AdminFSAList>('/api/fsas')
    fsas = list.ok ? (list.data.fsas ?? []) : []

    const tree = await fetchJSON<TopologyNode>('/api/topology')
    if (tree.ok) {
      topology = tree.data
      topologyError = ''
    } else {
      topology = null
      topologyError = tree.error
    }
  }

  onMount(async () => {
    // One authenticated read before opening the stream: EventSource
    // reports an auth failure as an indistinguishable onerror, so a
    // 401 discovered here is the only way to tell "not logged in" from
    // "stream dropped" and show the login form instead of an empty page.
    const probe = await fetchJSON<DashboardData>('/dashboard/data')
    if (!probe.ok && probe.status === 401) {
      unauthorized = 'Admin session required.'
      return
    }
    if (probe.ok) {
      data = probe.data
      history = appendHistory(history, probe.data)
    }

    disconnect = connectDashboard((next) => {
      data = next
      history = appendHistory(history, next)
    })
    await refresh()
  })

  onDestroy(() => {
    disconnect?.()
    disconnect = null
  })
</script>

{#if unauthorized}
  <NavBar data={null} />
  <div class="grid">
    <LoginPanel reason={unauthorized} />
  </div>
{:else}
  <NavBar {data} />
  <div class="grid">
    <Overview {data} />
    <ServerInfo {data} />
    <CertPanel />
    <DerControl />
    <AddDevice onAdded={refresh} />
    <LookupDevice />
    <CreateFsa onCreated={refresh} />
    <FsaCatalog {fsas} onChanged={refresh} />
    <TopologyTree tree={topology} error={topologyError} onRefresh={refresh} />
    <DeviceTable devices={data?.devices ?? []} {fsas} onChanged={refresh} />
    <ActivityChart {history} />
  </div>
{/if}
