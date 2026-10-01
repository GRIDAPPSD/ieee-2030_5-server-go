<script lang="ts">
  // A chart section. Series names and the unit reach the page only through
  // ECharts' canvas text: the tooltip uses renderMode 'richText', since the
  // default HTML tooltip interpolates a series name into markup.
  import * as echarts from 'echarts/core'
  import { LineChart } from 'echarts/charts'
  import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
  import { CanvasRenderer } from 'echarts/renderers'
  import type { DescriptorChartBody } from '../lib/descriptor'

  echarts.use([LineChart, GridComponent, LegendComponent, TooltipComponent, CanvasRenderer])

  let { body, empty }: { body: DescriptorChartBody; empty: string } = $props()

  let container: HTMLDivElement | null = $state(null)
  let chart: echarts.ECharts | null = $state.raw(null)
  let initError = $state('')

  const hasPoints = $derived(body.series.some((s) => s.points.length > 0))

  const textColor = '#94a3b8'

  $effect(() => {
    if (container === null) return
    let instance: echarts.ECharts
    try {
      instance = echarts.init(container)
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
    chart.setOption(
      {
        backgroundColor: 'transparent',
        animation: false,
        tooltip: { trigger: 'axis', renderMode: 'richText' },
        legend: { textStyle: { color: textColor }, top: 0 },
        grid: { left: 60, right: 30, top: 40, bottom: 30 },
        xAxis: { type: 'time', axisLabel: { color: textColor } },
        yAxis: {
          type: 'value',
          name: body.unit,
          nameTextStyle: { color: textColor },
          axisLabel: { color: textColor },
          splitLine: { lineStyle: { color: '#334155' } },
        },
        series: body.series.map((s) => ({
          name: s.name,
          type: 'line',
          showSymbol: false,
          data: s.points,
        })),
      },
      true,
    )
  })
</script>

{#if hasPoints}
  <div class="chart-container" data-testid="descriptor-chart" bind:this={container}></div>
  {#if initError}
    <div class="result err" data-testid="descriptor-chart-error">Chart unavailable: {initError}</div>
  {/if}
{:else}
  <p class="hint" data-testid="descriptor-chart-empty">{empty || 'No data points.'}</p>
{/if}
