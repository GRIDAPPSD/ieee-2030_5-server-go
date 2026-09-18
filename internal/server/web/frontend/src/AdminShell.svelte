<script lang="ts">
  // The admin UI's shell: mounted once by App.svelte for every admin
  // path, and never swapped, so the tab switch below cannot remount it
  // and close the stream or drop history. It owns the three pieces of
  // shared state the panels read (the SSE payload, the FSA catalog, the
  // topology tree) and the one refresh path that reloads the last two,
  // so a create/attach/assign in any panel updates every panel that
  // shows the result.
  import { onDestroy, onMount } from 'svelte'
  import { fetchJSON } from './lib/api'
  import {
    appendHistory,
    connectDashboard,
    type DashboardData,
    type HistoryPoint,
  } from './lib/dashboard'
  import type { AdminFSA, AdminFSAList, TopologyNode } from './lib/fsa'
  import { currentPath, navigate, replace } from './lib/router'
  import NavBar from './panels/NavBar.svelte'
  import Overview from './panels/Overview.svelte'
  import ServerInfo from './panels/ServerInfo.svelte'
  import CertPanel from './panels/CertPanel.svelte'
  import DerControl from './panels/DerControl.svelte'
  import AddDevice from './panels/AddDevice.svelte'
  import LookupDevice from './panels/LookupDevice.svelte'
  import CreateFsa from './panels/CreateFsa.svelte'
  import FsaCatalog from './panels/FsaCatalog.svelte'
  import TopologyTree from './panels/TopologyTree.svelte'
  import DeviceTable from './panels/DeviceTable.svelte'
  import ActivityChart from './panels/ActivityChart.svelte'
  import LoginPanel from './panels/LoginPanel.svelte'

  // The five tabs and the card each owns (issue 561's Context section).
  // "/" and "/ui/" are not tab paths of their own: they alias overview.
  type TabSlug = 'overview' | 'devices' | 'fsas' | 'control' | 'certificates'
  const TABS: { slug: TabSlug; label: string }[] = [
    { slug: 'overview', label: 'Overview' },
    { slug: 'devices', label: 'Devices' },
    { slug: 'fsas', label: 'FSAs' },
    { slug: 'control', label: 'Control' },
    { slug: 'certificates', label: 'Certificates' },
  ]

  // 'not-found' is issue 561 criterion 6: any /ui/ path that names no tab
  // and no trailing-slash form of one. A path outside /ui/ and outside "/"
  // is not this issue's concern (the server's SPA fallback only ever sends
  // an admin UI path here), so it keeps the old default of overview.
  type ActiveTab = TabSlug | 'not-found'

  function tabForPath(path: string): ActiveTab {
    if (path === '/' || path === '/ui/') return 'overview'
    if (!path.startsWith('/ui/')) return 'overview'
    const exact = TABS.find((tab) => path === `/ui/${tab.slug}`)
    if (exact) return exact.slug
    const trailingSlug = path.endsWith('/') ? path.slice('/ui/'.length, -1) : null
    const trailing = trailingSlug ? TABS.find((tab) => tab.slug === trailingSlug) : undefined
    return trailing ? trailing.slug : 'not-found'
  }

  let activeTab = $derived(tabForPath($currentPath))

  // Renders the correct tab immediately from tabForPath above; this effect
  // only fixes the address bar afterward, as a replace so a trailing-slash
  // entry never lands in history for back to stop on. "/ui/" itself is the
  // overview alias (criterion 2), not a trailing-slash tab, so it is left
  // alone.
  $effect(() => {
    const path = $currentPath
    if (path === '/ui/' || !path.startsWith('/ui/') || !path.endsWith('/')) return
    const slug = path.slice('/ui/'.length, -1)
    if (TABS.some((tab) => tab.slug === slug)) replace(`/ui/${slug}`)
  })

  function onTabClick(event: MouseEvent, slug: TabSlug) {
    // A modifier key or a non-primary button asks the browser for its own
    // handling (new tab, new window); only a plain click becomes a
    // client-side navigation.
    if (event.defaultPrevented || event.button !== 0) return
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    event.preventDefault()
    navigate(`/ui/${slug}`)
  }

  let data = $state<DashboardData | null>(null)
  let history = $state<HistoryPoint[]>([])
  let fsas = $state<AdminFSA[]>([])
  let topology = $state<TopologyNode | null>(null)
  let topologyError = $state('')
  let unauthorized = $state('')
  let disconnect: (() => void) | null = null
  let destroyed = false

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
    // A component destroyed while this await was pending must not open a
    // stream nobody can close: onDestroy already ran, so a disconnect
    // assigned after it would never be called.
    if (destroyed) return
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
    destroyed = true
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
  <nav class="tab-bar">
    {#each TABS as tab (tab.slug)}
      <a
        href="/ui/{tab.slug}"
        data-testid="tab-{tab.slug}"
        aria-current={activeTab === tab.slug ? 'page' : undefined}
        onclick={(event) => onTabClick(event, tab.slug)}
      >{tab.label}</a>
    {/each}
  </nav>
  <div class="grid">
    {#if activeTab === 'overview'}
      <Overview {data} />
      <ServerInfo {data} />
      <ActivityChart {history} />
    {:else if activeTab === 'devices'}
      <DeviceTable devices={data?.devices ?? []} {fsas} onChanged={refresh} />
      <AddDevice onAdded={refresh} />
      <LookupDevice />
    {:else if activeTab === 'fsas'}
      <CreateFsa onCreated={refresh} />
      <FsaCatalog {fsas} onChanged={refresh} />
      <TopologyTree tree={topology} error={topologyError} onRefresh={refresh} />
    {:else if activeTab === 'control'}
      <DerControl />
    {:else if activeTab === 'certificates'}
      <CertPanel />
    {:else if activeTab === 'not-found'}
      <div class="card" data-testid="not-found">
        <h2>Not Found</h2>
        <p>No tab matches <span class="mono">{$currentPath}</span>.</p>
        <a
          href="/ui/overview"
          data-testid="not-found-overview-link"
          onclick={(event) => onTabClick(event, 'overview')}
        >Return to Overview</a>
      </div>
    {/if}
  </div>
{/if}
