// startPoll runs step now and again after each delay it returns, until the
// returned stop function is called. step gets a signal that stop aborts, and
// returns the milliseconds to wait before the next run, or undefined to end
// the poll. A step that settles after stop schedules nothing, so a read
// cancelled by teardown never starts another.
export function startPoll(step: (signal: AbortSignal) => Promise<number | undefined>): () => void {
  const ctrl = new AbortController()
  let timer: ReturnType<typeof setTimeout> | undefined
  async function run() {
    const next = await step(ctrl.signal)
    if (ctrl.signal.aborted || next === undefined) return
    timer = setTimeout(run, next)
  }
  void run()
  return () => {
    ctrl.abort()
    clearTimeout(timer)
  }
}
