<script lang="ts">
  // The device-activity chart. ECharts is imported here and therefore
  // bundled into the embedded asset: the previous markup pulled it from a
  // public CDN, which failed silently on an air-gapped or firewalled
  // deployment and left the operator with a blank panel and no
  // server-side signal.
  //
  // Only the pieces this chart uses are registered, so the tree-shaken
  // build carries a line chart and a canvas renderer rather than all of
  // ECharts. The dark look comes from explicit colours in the option
  // below rather than the named "dark" theme, which is a separate
  // registration step in the modular build.
  import { onDestroy, onMount } from 'svelte'
  import * as echarts from 'echarts/core'
  import { LineChart } from 'echarts/charts'
  import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
  import { CanvasRenderer } from 'echarts/renderers'
  import type { HistoryPoint } from '../lib/dashboard'

  echarts.use([LineChart, GridComponent, LegendComponent, TooltipComponent, CanvasRenderer])

  let { history }: { history: HistoryPoint[] } = $props()

  let container: HTMLDivElement | null = $state(null)
  let chart: echarts.ECharts | null = null
  let initError = $state('')

  const baseOption = {
    backgroundColor: 'transparent',
    tooltip: { trigger: 'axis' as const },
    xAxis: { type: 'category' as const, data: [] as string[], axisLabel: { color: '#94a3b8' } },
    yAxis: [
      {
        type: 'value' as const,
        name: 'Devices',
        axisLabel: { color: '#94a3b8' },
        splitLine: { lineStyle: { color: '#334155' } },
      },
      {
        type: 'value' as const,
        name: 'MUPs',
        axisLabel: { color: '#94a3b8' },
        splitLine: { show: false },
      },
    ],
    series: [
      {
        name: 'Devices',
        type: 'line' as const,
        smooth: true,
        symbol: 'none' as const,
        lineStyle: { width: 2, color: '#3b82f6' },
        areaStyle: { color: 'rgba(59,130,246,0.1)' },
        data: [] as number[],
      },
      {
        name: 'MUPs',
        type: 'line' as const,
        smooth: true,
        symbol: 'none' as const,
        yAxisIndex: 1,
        lineStyle: { width: 2, color: '#22c55e' },
        data: [] as number[],
      },
    ],
    legend: { textStyle: { color: '#94a3b8' }, top: 0 },
    grid: { left: 50, right: 50, top: 40, bottom: 30 },
    animation: false,
  }

  function resize() {
    chart?.resize()
  }

  onMount(() => {
    if (container === null) return
    try {
      chart = echarts.init(container)
      chart.setOption(baseOption)
      window.addEventListener('resize', resize)
    } catch (err) {
      // Surfaced rather than swallowed: a chart that cannot initialize
      // must say so on the page, which is exactly what the CDN load
      // failure never did.
      initError = err instanceof Error ? err.message : String(err)
      chart = null
    }
  })

  onDestroy(() => {
    window.removeEventListener('resize', resize)
    chart?.dispose()
    chart = null
  })

  $effect(() => {
    const points = history
    if (chart === null) return
    chart.setOption({
      xAxis: { data: points.map((p) => p.time) },
      series: [{ data: points.map((p) => p.devices) }, { data: points.map((p) => p.mups) }],
    })
  })
</script>

<div class="card full-width">
  <h2>Device Activity</h2>
  <div class="chart-container" id="activityChart" bind:this={container}></div>
  {#if initError}
    <div class="result err" data-testid="chart-error">Chart unavailable: {initError}</div>
  {/if}
</div>
