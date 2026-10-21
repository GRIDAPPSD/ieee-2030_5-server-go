<script lang="ts">
  // The flat FSA template list from GET /api/fsas, which is the only
  // surface that reports which devices an FSA is assigned to: the
  // topology tree carries the FSAs but not their device assignments, so
  // unassigning is done from here. Each FSA row also carries that FSA's
  // program templates, which are keyed by the FSA mRID like the other FSA
  // routes.
  import FsaTemplates from './FsaTemplates.svelte'
  import { unassignDeviceFsa, unassignFailure, type AdminFSA } from '../lib/fsa'

  let {
    fsas,
    onChanged,
  }: {
    fsas: AdminFSA[]
    onChanged: () => void
  } = $props()

  let error = $state('')

  async function unassign(fsa: AdminFSA, deviceID: string) {
    const res = await unassignDeviceFsa(deviceID, fsa.mRID)
    if (!res.ok) {
      error = unassignFailure(res)
      return
    }
    error = ''
    onChanged()
  }
</script>

<div class="card full-width">
  <h2>FSA Templates</h2>
  {#if fsas.length === 0}
    <p class="hint" data-testid="fsa-catalog-empty">No FSA templates created yet.</p>
  {:else}
    <table data-testid="fsa-catalog-table">
      <thead>
        <tr><th>mRID</th><th>Description</th><th>Primacy</th><th>Programs</th><th>Assigned devices</th></tr>
      </thead>
      <tbody>
        {#each fsas as fsa (fsa.mRID)}
          <tr>
            <td class="mono" data-testid="fsa-catalog-mrid">{fsa.mRID}</td>
            <td data-testid="fsa-catalog-description">{fsa.description}</td>
            <td data-testid="fsa-catalog-primacy">{fsa.primacy}</td>
            <td class="mono">{(fsa.programs ?? []).join(', ') || '-'}</td>
            <td>
              {#if (fsa.devices ?? []).length === 0}
                <span class="stat-label">-</span>
              {:else}
                {#each fsa.devices ?? [] as deviceID (deviceID)}
                  <span class="assigned">
                    <span class="mono">{deviceID}</span>
                    <button
                      class="btn btn-red btn-small"
                      data-testid="unassign-{fsa.mRID}-{deviceID}"
                      onclick={() => unassign(fsa, deviceID)}>Unassign</button
                    >
                  </span>
                {/each}
              {/if}
            </td>
          </tr>
          <tr>
            <td colspan="5"><FsaTemplates fsaId={fsa.mRID} {onChanged} /></td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
  {#if error}
    <div class="result err" data-testid="fsa-catalog-error">{error}</div>
  {/if}
</div>

<style>
  .assigned {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    margin-right: 8px;
  }
</style>
