<script lang="ts">
  // The device-activity chart. ECharts is imported here and therefore
  // bundled into the embedded asset: the previous markup pulled it from a
  // public CDN, which failed silently on an air-gapped or firewalled
  // deployment and left the operator with a blank panel and no
  // server-side signal.
  //
  // Only the pieces this chart uses are registered, so the tree-shaken
  // build carries a line chart and a canvas renderer rather than all of
  // ECharts. Colours come from the design tokens through chartTheme(),
  // read when the option is built, because a canvas cannot read a CSS variable.
  import { onDestroy, onMount } from 'svelte'
  import * as echarts from 'echarts/core'
  import { LineChart } from 'echarts/charts'
  import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
  import { CanvasRenderer } from 'echarts/renderers'
  import { chartTheme, tooltipStyle } from '../lib/chartTheme'
  import type { HistoryPoint } from '../lib/dashboard'

  echarts.use([LineChart, GridComponent, LegendComponent, TooltipComponent, CanvasRenderer])

  let { history }: { history: HistoryPoint[] } = $props()

  let container: HTMLDivElement | null = $state(null)
  let chart: echarts.ECharts | null = null
  let initError = $state('')

  function buildOption() {
    const theme = chartTheme()
    const [registered, comms, mups] = theme.palette
    return {
      backgroundColor: 'transparent',
      tooltip: { trigger: 'axis' as const, ...tooltipStyle(theme) },
      xAxis: { type: 'category' as const, data: [] as string[], axisLabel: { color: theme.axis } },
      yAxis: [
        {
          type: 'value' as const,
          name: 'Devices',
          axisLabel: { color: theme.axis },
          splitLine: { lineStyle: { color: theme.grid } },
        },
        {
          type: 'value' as const,
          name: 'MUPs',
          axisLabel: { color: theme.axis },
          splitLine: { show: false },
        },
      ],
      series: [
        {
          name: 'Registered',
          type: 'line' as const,
          smooth: true,
          symbol: 'none' as const,
          lineStyle: { width: 2, color: registered },
          areaStyle: { color: theme.area },
          data: [] as number[],
        },
        {
          name: 'Comms online',
          type: 'line' as const,
          smooth: true,
          symbol: 'none' as const,
          lineStyle: { width: 2, color: comms },
          data: [] as number[],
        },
        {
          name: 'MUPs',
          type: 'line' as const,
          smooth: true,
          symbol: 'none' as const,
          yAxisIndex: 1,
          lineStyle: { width: 2, color: mups },
          data: [] as number[],
        },
      ],
      legend: { textStyle: { color: theme.axis }, top: 0 },
      grid: { left: 50, right: 50, top: 40, bottom: 30 },
      animation: false,
    }
  }

  function resize() {
    chart?.resize()
  }

  onMount(() => {
    if (container === null) return
    try {
      chart = echarts.init(container)
      chart.setOption(buildOption())
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
      series: [
        { data: points.map((p) => p.devices) },
        { data: points.map((p) => p.commsOnline) },
        { data: points.map((p) => p.mups) },
      ],
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
