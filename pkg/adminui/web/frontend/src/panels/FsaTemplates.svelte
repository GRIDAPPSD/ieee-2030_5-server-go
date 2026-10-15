<script lang="ts">
  // The program templates of one FSA: the list from
  // GET /api/fsas/{id}/der-programs with Add, Edit and Delete. Every device
  // assigned to the FSA holds a copy of each template, so an edit or delete
  // here reaches all of them; the device count shown is how many copies exist.
  import { onDestroy, tick } from 'svelte'
  import DerProgramForm from './DerProgramForm.svelte'
  import { focusOnMount } from '../lib/focus'
  import {
    createTemplate,
    deleteTemplate,
    listTemplates,
    MAX_TEMPLATES_PER_FSA,
    refusalOf,
    updateTemplate,
    type ProgramBody,
    type ProgramTemplate,
  } from '../lib/derprogram'

  let { fsaId, onChanged }: { fsaId: string; onChanged: () => void } = $props()

  let templates = $state<ProgramTemplate[] | null>(null)
  let loadError = $state('')
  let adding = $state(false)
  let editing = $state('')
  let confirming = $state('')
  let deleting = $state('')
  let deleteButtons: Record<string, HTMLButtonElement | undefined> = {}
  let deleteError = $state<{ id: string; message: string; controls: string[] } | null>(null)

  // A newer load, or the panel going away, makes an older answer stale.
  let loadSeq = 0

  async function load(id: string) {
    const seq = ++loadSeq
    const res = await listTemplates(id)
    if (seq !== loadSeq) return
    if (!res.ok) {
      loadError = `Could not load program templates (${res.status}): ${res.error}`
      templates = null
      return
    }
    if (!Array.isArray(res.data?.templates)) {
      loadError = 'Could not load program templates (200): the reply has no templates list'
      templates = null
      return
    }
    loadError = ''
    templates = res.data.templates
  }

  $effect(() => {
    void load(fsaId)
  })

  onDestroy(() => {
    loadSeq++
  })

  async function changed() {
    await load(fsaId)
    onChanged()
  }

  async function add(body: ProgramBody) {
    const res = await createTemplate(fsaId, body)
    if (!res.ok) return { ok: false as const, message: `Error (${res.status}): ${res.error}` }
    adding = false
    await changed()
    return { ok: true as const }
  }

  async function save(id: string, body: ProgramBody) {
    const res = await updateTemplate(fsaId, id, body)
    if (!res.ok) return { ok: false as const, message: `Error (${res.status}): ${res.error}` }
    // The operator may have opened another row while this save was out.
    if (editing === id) editing = ''
    await changed()
    return { ok: true as const }
  }

  async function keep(id: string) {
    confirming = ''
    await tick()
    deleteButtons[id]?.focus()
  }

  async function remove(id: string) {
    if (deleting !== '') return
    deleting = id
    deleteError = null
    const res = await deleteTemplate(fsaId, id)
    deleting = ''
    confirming = ''
    if (!res.ok) {
      const r = refusalOf(res)
      deleteError =
        r.code === 'live_controls'
          ? { id, message: r.message, controls: r.controls }
          : { id, message: `Delete failed (${r.status}): ${r.message}`, controls: [] }
      return
    }
    await changed()
  }

  const full = $derived((templates?.length ?? 0) >= MAX_TEMPLATES_PER_FSA)
  const editingTemplate = $derived(templates?.find((t) => t.id === editing))
</script>

<div class="templates" data-testid="fsa-templates-{fsaId}">
  <div class="templates-head">
    <strong>Program templates</strong>
    <button
      class="btn btn-small"
      data-testid="template-add-{fsaId}"
      disabled={full || templates === null}
      onclick={() => {
        adding = !adding
        editing = ''
      }}>New template</button
    >
    {#if full}
      <span class="hint" data-testid="templates-full-{fsaId}">
        This FSA holds the most templates the server allows ({MAX_TEMPLATES_PER_FSA}).
      </span>
    {/if}
  </div>

  {#if loadError}
    <div class="result err" role="alert" tabindex="-1" use:focusOnMount data-testid="templates-error-{fsaId}">
      {loadError}
    </div>
  {:else if templates === null}
    <p class="hint">Loading program templates...</p>
  {:else if templates.length === 0}
    <p class="hint" data-testid="templates-empty-{fsaId}">No program templates on this FSA yet.</p>
  {:else}
    <table>
      <thead>
        <tr><th>Description</th><th>Primacy</th><th>Version</th><th>Default control</th><th>Devices</th><th></th></tr>
      </thead>
      <tbody>
        {#each templates as t (t.id)}
          <tr data-testid="template-row-{t.id}">
            <td data-testid="template-description">{t.description}</td>
            <td data-testid="template-primacy">{t.primacy}</td>
            <td data-testid="template-version">{t.version}</td>
            <td data-testid="template-control">{t.defaultControl.type}</td>
            <td data-testid="template-devices">{t.copies.length}</td>
            <td>
              <button
                class="btn btn-small"
                data-testid="template-edit-{t.id}"
                aria-label="Edit {t.description}"
                onclick={() => {
                  editing = editing === t.id ? '' : t.id
                  adding = false
                }}>Edit</button
              >
              {#if confirming === t.id}
                <button
                  class="btn btn-red btn-small"
                  data-testid="template-delete-confirm-{t.id}"
                  aria-label="Confirm delete {t.description}"
                  disabled={deleting !== ''}
                  use:focusOnMount
                  onclick={() => remove(t.id)}>{deleting === t.id ? 'Deleting...' : 'Confirm delete'}</button
                >
                <button
                  class="btn btn-small"
                  aria-label="Keep {t.description}"
                  disabled={deleting === t.id}
                  onclick={() => keep(t.id)}>Keep</button
                >
              {:else}
                <button
                  class="btn btn-red btn-small"
                  data-testid="template-delete-{t.id}"
                  aria-label="Delete {t.description}"
                  bind:this={deleteButtons[t.id]}
                  onclick={() => {
                    confirming = t.id
                    deleteError = null
                  }}>Delete</button
                >
              {/if}
              {#if deleteError?.id === t.id}
                {#if deleteError.controls.length > 0}
                  <div class="result err" role="alert" tabindex="-1" use:focusOnMount data-testid="template-conflict-{t.id}">
                    {deleteError.message}
                    <ul>
                      {#each deleteError.controls as c (c)}
                        <li class="mono">{c}</li>
                      {/each}
                    </ul>
                  </div>
                {:else}
                  <div class="result err" role="alert" tabindex="-1" use:focusOnMount data-testid="template-delete-error-{t.id}">
                    {deleteError.message}
                  </div>
                {/if}
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}

  {#if adding}
    <DerProgramForm mode="create" onSubmit={add} onCancel={() => (adding = false)} />
  {/if}
  {#if editingTemplate}
    {#key editingTemplate.id}
      <DerProgramForm
        mode="edit"
        initial={editingTemplate}
        onSubmit={(body) => save(editingTemplate.id, body)}
        onCancel={() => (editing = '')}
      />
    {/key}
  {/if}
</div>

<style>
  .templates {
    margin: 4px 0 8px 0;
  }

  .templates-head {
    display: flex;
    align-items: center;
    gap: 8px;
  }
</style>
