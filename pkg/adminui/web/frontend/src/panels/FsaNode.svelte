<script lang="ts">
  // One FSA hanging off a topology node: its attached DERPrograms, the
  // attach form, and (at the system root only) delete. The attach input
  // and its result span keep the per-mRID ids the previous markup
  // generated, since the Playwright suite addresses them by id.
  import { deleteJSON, postJSON } from '../lib/api'
  import type { TopologyFSA } from '../lib/fsa'

  let {
    fsa,
    depth,
    allowDelete,
    onChanged,
  }: {
    fsa: TopologyFSA
    depth: number
    allowDelete: boolean
    onChanged: () => void
  } = $props()

  let programHref = $state('')
  let result = $state('')
  let ok = $state(false)

  async function attachProgram() {
    if (!programHref) {
      ok = false
      result = 'programHref required'
      return
    }
    const res = await postJSON<{ programHref: string }>(
      `/api/fsas/${encodeURIComponent(fsa.mRID)}/programs`,
      { programHref },
    )
    if (!res.ok) {
      ok = false
      result = `Error (${res.status}): ${res.error}`
      return
    }
    ok = true
    result = `Attached ${res.data.programHref}`
    programHref = ''
    onChanged()
  }

  async function detachProgram(href: string) {
    const res = await deleteJSON<void>(
      `/api/fsas/${encodeURIComponent(fsa.mRID)}/programs?href=${encodeURIComponent(href)}`,
    )
    if (!res.ok) {
      ok = false
      result = `Error (${res.status}): ${res.error}`
      return
    }
    ok = true
    result = `Detached ${href}`
    onChanged()
  }

  async function deleteFSA() {
    const res = await deleteJSON<void>(`/api/fsas/${encodeURIComponent(fsa.mRID)}`)
    if (!res.ok) {
      ok = false
      result = `Delete failed (${res.status}): ${res.error}`
      return
    }
    onChanged()
  }
</script>

<div class="fsa" style="margin-left: {depth * 16}px;">
  <div>FSA {fsa.mRID} - {fsa.description ?? ''}</div>

  {#each fsa.programs ?? [] as program (program)}
    <div class="program">
      <span>PROG {program}</span>
      <button
        class="btn btn-red btn-small"
        data-testid="detach-program-{fsa.mRID}"
        onclick={() => detachProgram(program)}>Detach</button
      >
    </div>
  {/each}

  <div class="controls">
    <input id="attachInp-{fsa.mRID}" type="text" placeholder="programHref to attach" bind:value={programHref} />
    <button class="btn btn-small" onclick={attachProgram}>Attach program</button>
    {#if allowDelete}
      <button class="btn btn-red btn-small" onclick={deleteFSA}>Delete</button>
    {/if}
    <span
      id="attachResult-{fsa.mRID}"
      class="attach-result"
      class:ok
      class:err={result !== '' && !ok}>{result}</span
    >
  </div>
</div>

<style>
  .fsa {
    color: #fbbf24;
  }

  .program {
    margin-left: 16px;
    color: #a3e635;
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .controls {
    margin-left: 16px;
    margin-top: 4px;
    color: var(--dim);
    display: flex;
    align-items: center;
    gap: 4px;
  }

  .controls input {
    background: var(--bg);
    border: 1px solid var(--border);
    color: var(--text);
    padding: 2px 4px;
    font-size: 12px;
    width: 320px;
  }

  .attach-result {
    margin-left: 8px;
    font-size: 12px;
  }

  .attach-result.ok {
    color: var(--green);
  }

  .attach-result.err {
    color: var(--red);
  }
</style>
