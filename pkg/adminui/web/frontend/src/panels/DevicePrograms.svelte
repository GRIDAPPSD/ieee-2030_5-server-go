<script lang="ts">
  // The Programs drawer of one device: every DER program it holds, grouped by
  // FSA, with where each came from and what a restart does to it. Template
  // copies are edited on the FSAs tab and only link there; fixture and device
  // programs can be edited and deleted here, and a device program added.
  // The list is the whole catalog (GET /api/der-programs) filtered to this
  // device, because the server has no per-device filter.
  import { onDestroy } from 'svelte'
  import DerProgramForm from './DerProgramForm.svelte'
  import ConfirmAction from './ConfirmAction.svelte'
  import TabLink from './TabLink.svelte'
  import { focusOnMount } from '../lib/focus'
  import {
    createDeviceProgram,
    deleteDeviceProgram,
    groupByFsa,
    isDeviceEditable,
    listAllPrograms,
    originLabel,
    refusalOf,
    restartLabel,
    updateDeviceProgram,
    type ProgramBody,
    type ProgramCatalog,
  } from '../lib/derprogram'
  import type { ApiResult } from '../lib/api'
  import { unassignDeviceFsa, unassignFailure, type AdminFSA } from '../lib/fsa'

  // onFsaChanged reloads the FSA list the shell holds, since an unassign
  // changes which FSAs this device is assigned to; it is awaited so the group
  // does not show as empty while still assigned.
  let {
    deviceId,
    fsas,
    onClose,
    onFsaChanged,
  }: { deviceId: string; fsas: AdminFSA[]; onClose: () => void; onFsaChanged: () => void | Promise<void> } = $props()

  let catalog = $state<ProgramCatalog | null>(null)
  let loadError = $state('')
  let adding = $state('')
  let editing = $state('')
  // One action is out at a time, keyed 'program:<id>' or 'fsa:<mRID>'; every
  // confirm button of the panel is held while it is.
  let pending = $state('')
  let failure = $state<{ key: string; message: string; controls: string[] } | null>(null)

  // A newer read, a device switch or the drawer closing makes an older answer
  // stale: the sequence number drops it, the controller cancels its request,
  // and `closed` stops a mutation that was out from reloading afterwards.
  let loadSeq = 0
  let ctrl: AbortController | undefined
  let closed = false

  async function load() {
    ctrl?.abort()
    const mine = new AbortController()
    ctrl = mine
    const seq = ++loadSeq
    const res = await listAllPrograms({ signal: mine.signal })
    if (seq !== loadSeq || closed) return
    if (!res.ok) {
      loadError = `Could not load programs (${res.status}): ${res.error}`
      catalog = null
      return
    }
    loadError = ''
    catalog = res.data
  }

  $effect(() => {
    void deviceId
    void load()
  })

  onDestroy(() => {
    closed = true
    loadSeq++
    ctrl?.abort()
  })

  async function changed() {
    await load()
  }

  const assigned = $derived(fsas.filter((f) => f.devices?.includes(deviceId)).map((f) => f.mRID))
  const groups = $derived(catalog ? groupByFsa(catalog.programs, deviceId, assigned) : [])
  const editingProgram = $derived(catalog?.programs.find((p) => p.device === deviceId && p.id === editing))

  function fsaName(id: string): string {
    const f = fsas.find((o) => o.mRID === id)
    return f?.description ? `${f.description} (${id})` : id
  }

  async function add(fsaId: string, body: ProgramBody) {
    const res = await createDeviceProgram(deviceId, fsaId, body)
    if (closed) return { ok: true as const }
    if (!res.ok) return { ok: false as const, message: `Error (${res.status}): ${res.error}` }
    adding = ''
    await changed()
    return { ok: true as const }
  }

  async function save(id: string, body: ProgramBody) {
    const res = await updateDeviceProgram(deviceId, id, body)
    if (closed) return { ok: true as const }
    if (!res.ok) return { ok: false as const, message: `Error (${res.status}): ${res.error}` }
    // The operator may have opened another row while this save was out.
    if (editing === id) editing = ''
    await changed()
    return { ok: true as const }
  }

  // act runs one confirmed action: it guards against a second while one is
  // out, records a refusal under its key, and reloads after a success.
  async function act(
    key: string,
    call: () => Promise<ApiResult<void>>,
    refuse: (res: Extract<ApiResult<void>, { ok: false }>) => { message: string; controls: string[] },
    after: () => Promise<void>,
  ) {
    if (pending !== '') return
    pending = key
    failure = null
    const res = await call()
    if (closed) return
    pending = ''
    if (!res.ok) {
      failure = { key, ...refuse(res) }
      return
    }
    await after()
  }

  function remove(id: string) {
    return act(
      `program:${id}`,
      () => deleteDeviceProgram(deviceId, id),
      (res) => {
        const r = refusalOf(res)
        return r.code === 'live_controls'
          ? { message: r.message, controls: r.controls }
          : { message: `Delete failed (${r.status}): ${r.message}`, controls: [] }
      },
      changed,
    )
  }

  // A 404 means the assignment is already gone, which is what the operator
  // asked for, so it reloads like a success and drops the stale group.
  function unassign(fsaId: string) {
    return act(
      `fsa:${fsaId}`,
      async () => {
        const res = await unassignDeviceFsa(deviceId, fsaId)
        return !res.ok && res.status === 404 ? { ok: true as const, data: undefined } : res
      },
      (res) => ({ message: unassignFailure(res), controls: [] }),
      async () => {
        await onFsaChanged()
        await changed()
      },
    )
  }
</script>

{#snippet failureBox(testid: string)}
  {#if failure}
    <div class="result err" role="alert" tabindex="-1" use:focusOnMount data-testid={testid}>
      {failure.message}
      {#if failure.controls.length > 0}
        <ul>
          {#each failure.controls as c (c)}
            <li class="mono">{c}</li>
          {/each}
        </ul>
      {/if}
    </div>
  {/if}
{/snippet}

<div class="programs" data-testid="device-programs-{deviceId}">
  <div class="programs-head">
    <strong>Programs</strong>
    <button class="btn btn-small" aria-label="Close programs" onclick={onClose}>Close</button>
  </div>

  {#if catalog && !catalog.persists}
    <div class="result err" role="status" data-testid="programs-persist-banner">
      This server keeps no data directory, so nothing here survives a restart.
    </div>
  {/if}

  {#if loadError}
    <div class="result err" role="alert" tabindex="-1" use:focusOnMount data-testid="programs-error">{loadError}</div>
  {:else if catalog === null}
    <p class="hint">Loading programs...</p>
  {:else if groups.length === 0}
    <p class="hint" data-testid="programs-empty">This device holds no programs and is assigned to no FSA.</p>
  {:else}
    {#each groups as group (group.fsa)}
      <div class="fsa-group" data-testid="programs-fsa-{group.fsa}">
        <div class="programs-head">
          <strong>{fsaName(group.fsa)}</strong>
          <button
            class="btn btn-small"
            data-testid="program-add-{group.fsa}"
            disabled={group.fsa === ''}
            onclick={() => {
              adding = adding === group.fsa ? '' : group.fsa
              editing = ''
            }}>Add program</button
          >
          {#if group.fsa !== '' && assigned.includes(group.fsa)}
            <ConfirmAction
              triggerId="fsa-unassign-{group.fsa}"
              confirmId="fsa-unassign-confirm-{group.fsa}"
              trigger="Unassign from FSA"
              triggerLabel="Unassign from FSA {fsaName(group.fsa)}"
              confirm="Confirm unassign"
              confirmLabel="Confirm unassign from {fsaName(group.fsa)}"
              busyText="Unassigning..."
              keepLabel="Keep assignment to {fsaName(group.fsa)}"
              busy={pending === `fsa:${group.fsa}`}
              disabled={pending !== ''}
              onOpen={() => (failure = null)}
              onConfirm={() => unassign(group.fsa)}
            />
          {/if}
        </div>
        {#if failure?.key === `fsa:${group.fsa}`}
          {@render failureBox(`fsa-unassign-error-${group.fsa}`)}
        {/if}
        {#if group.programs.length === 0}
          <p class="hint">No programs from this FSA yet.</p>
        {:else}
          <table>
            <thead>
              <tr><th>Description</th><th>Primacy</th><th>Version</th><th>Origin</th><th>On restart</th><th></th></tr>
            </thead>
            <tbody>
              {#each group.programs as p (p.id)}
                <tr data-testid="program-row-{p.id}">
                  <td data-testid="program-description">{p.description}</td>
                  <td data-testid="program-primacy">{p.primacy}</td>
                  <td data-testid="program-version">{p.version}</td>
                  <td><span class="badge origin-{p.origin}" data-testid="program-origin">{originLabel(p.origin)}</span></td>
                  <td data-testid="program-restart">{restartLabel(p.onRestart)}</td>
                  <td>
                    {#if isDeviceEditable(p)}
                      <button
                        class="btn btn-small"
                        data-testid="program-edit-{p.id}"
                        aria-label="Edit {p.description}"
                        onclick={() => {
                          editing = editing === p.id ? '' : p.id
                          adding = ''
                        }}>Edit</button
                      >
                      <ConfirmAction
                        triggerId="program-delete-{p.id}"
                        confirmId="program-delete-confirm-{p.id}"
                        trigger="Delete"
                        triggerLabel="Delete {p.description}"
                        confirm="Confirm delete"
                        confirmLabel="Confirm delete {p.description}"
                        busyText="Deleting..."
                        keepLabel="Keep {p.description}"
                        busy={pending === `program:${p.id}`}
                        disabled={pending !== ''}
                        onOpen={() => (failure = null)}
                        onConfirm={() => remove(p.id)}
                      />
                    {:else}
                      <span>Edit the template on the <TabLink path="/ui/fsas" testid="program-fsa-link-{p.id}">FSAs tab</TabLink></span>
                    {/if}
                    {#if failure?.key === `program:${p.id}`}
                      {@render failureBox(failure.controls.length > 0 ? `program-conflict-${p.id}` : `program-delete-error-${p.id}`)}
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
        {#if adding === group.fsa}
          <DerProgramForm
            mode="create"
            noun="device program"
            onSubmit={(body) => add(group.fsa, body)}
            onCancel={() => (adding = '')}
          />
        {/if}
      </div>
    {/each}
  {/if}

  {#if editingProgram}
    {#key editingProgram.id}
      <DerProgramForm
        mode="edit"
        noun="device program"
        initial={editingProgram}
        onSubmit={(body) => save(editingProgram.id, body)}
        onCancel={() => (editing = '')}
      />
    {/key}
  {/if}
</div>

<style>
  .programs {
    margin: 4px 0 8px 0;
  }

  .programs-head {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .fsa-group {
    margin-top: 8px;
  }

  .badge {
    font-size: 11px;
    padding: 1px 6px;
    border: 1px solid var(--border);
    border-radius: 8px;
  }
</style>
