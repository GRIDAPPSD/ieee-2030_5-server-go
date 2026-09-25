// jsdom implements <canvas> as an element but not its 2D context, so a
// chart library that measures text and paints paths cannot initialize
// under it. This stub supplies the subset of CanvasRenderingContext2D
// that ECharts touches, so a component test can assert the BUNDLED chart
// library initializes with no network access at all. It is a test-
// environment shim, not a fake of the chart: whether the chart visibly
// draws is asserted in a real browser by e2e/chart_offline.spec.ts.
const noop = () => {}

function stubContext(canvas: HTMLCanvasElement): unknown {
  return {
    canvas,
    // Painting and path operations: called for their side effects only.
    save: noop,
    restore: noop,
    scale: noop,
    rotate: noop,
    translate: noop,
    transform: noop,
    setTransform: noop,
    resetTransform: noop,
    clearRect: noop,
    fillRect: noop,
    strokeRect: noop,
    beginPath: noop,
    closePath: noop,
    moveTo: noop,
    lineTo: noop,
    bezierCurveTo: noop,
    quadraticCurveTo: noop,
    arc: noop,
    arcTo: noop,
    ellipse: noop,
    rect: noop,
    fill: noop,
    stroke: noop,
    clip: noop,
    fillText: noop,
    strokeText: noop,
    drawImage: noop,
    setLineDash: noop,
    getLineDash: () => [],
    putImageData: noop,
    // Queries: return shapes with the fields the caller reads.
    measureText: (text: string) => ({ width: text.length * 6 }),
    getImageData: () => ({ data: new Uint8ClampedArray(4) }),
    createLinearGradient: () => ({ addColorStop: noop }),
    createRadialGradient: () => ({ addColorStop: noop }),
    createPattern: () => null,
    isPointInPath: () => false,
  }
}

export function installCanvasStub(): void {
  HTMLCanvasElement.prototype.getContext = function (this: HTMLCanvasElement) {
    return stubContext(this)
  } as typeof HTMLCanvasElement.prototype.getContext
}
