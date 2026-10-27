<script lang="ts">
  // A chart section. Series names and the unit reach the page only through
  // ECharts' canvas text: the tooltip uses renderMode 'richText', since the
  // default HTML tooltip interpolates a series name into markup.
  import * as echarts from 'echarts/core'
  import { BarChart, LineChart } from 'echarts/charts'
  import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
  import { CanvasRenderer } from 'echarts/renderers'
  import { chartTooltip, richTextSafe, seriesOption } from '../lib/chart'
  import { chartTheme, tooltipStyle } from '../lib/chartTheme'
  import type { DrawnChartBody } from '../lib/descriptor'

  echarts.use([BarChart, LineChart, GridComponent, LegendComponent, TooltipComponent, CanvasRenderer])

  // frame draws the axes even with no points, with the empty message inside the
  // frame; range, in epoch milliseconds, is the time span the empty frame shows.
  let {
    body,
    empty,
    frame = false,
    range = null,
  }: { body: DrawnChartBody; empty: string; frame?: boolean; range?: [number, number] | null } = $props()

  let container: HTMLDivElement | null = $state(null)
  let chart: echarts.ECharts | null = $state.raw(null)
  let initError = $state('')

  const hasPoints = $derived(body.series.some((s) => s.points.length > 0))

  $effect(() => {
    if (container === null) return
    let instance: echarts.ECharts
    try {
      instance = echarts.init(container)
      initError = ''
    } catch (err) {
      initError = err instanceof Error ? err.message : String(err)
      return
    }
    chart = instance
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(() => instance.resize())
    observer?.observe(container)
    return () => {
      observer?.disconnect()
      instance.dispose()
      chart = null
    }
  })

  $effect(() => {
    if (chart === null) return
    const theme = chartTheme()
    const textColor = theme.axis
    chart.setOption(
      {
        color: theme.palette,
        backgroundColor: 'transparent',
        animation: false,
        tooltip: { trigger: 'axis', renderMode: 'richText', formatter: chartTooltip(body.unit), ...tooltipStyle(theme) },
        legend: { type: 'scroll', textStyle: { color: textColor }, pageTextStyle: { color: textColor }, top: 0 },
        grid: { left: 60, right: 30, top: 56, bottom: 30 },
        xAxis: {
          type: 'time',
          splitNumber: 4,
          ...(frame && !hasPoints && range !== null ? { min: range[0], max: range[1] } : {}),
          axisLabel: { color: textColor, formatter: '{HH}:{mm}:{ss}', hideOverlap: true },
        },
        yAxis: {
          type: 'value',
          // Bars start at zero, or a discharge-only window exaggerates them.
          scale: !body.series.some((s) => s.type === 'bar'),
          boundaryGap: ['5%', '5%'],
          name: richTextSafe(body.unit),
          nameTextStyle: { color: textColor },
          axisLabel: { color: textColor },
          splitLine: { lineStyle: { color: theme.grid } },
        },
        series: body.series.map((s, i) => seriesOption(s, theme, i)),
      },
      true,
    )
  })
</script>

{#if hasPoints || frame}
  <div class:chart-frame={frame}>
    <div class="chart-container chart-tall" data-testid="descriptor-chart" bind:this={container}></div>
    {#if !hasPoints}
      <p class="chart-empty" data-testid="descriptor-chart-empty">{empty || 'No data points.'}</p>
    {/if}
  </div>
  {#if initError}
    <div class="result err" data-testid="descriptor-chart-error">Chart unavailable: {initError}</div>
  {/if}
{:else}
  <p class="hint" data-testid="descriptor-chart-empty">{empty || 'No data points.'}</p>
{/if}
