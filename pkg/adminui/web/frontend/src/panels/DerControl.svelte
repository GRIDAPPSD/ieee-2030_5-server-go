<script lang="ts">
  // Send DER Control: pick a device, or an FSA and one of its program
  // templates, choose a default or a scheduled control, tick functions and
  // fill their values, then Send shows a one-sentence summary and only
  // Confirm posts. One form serves both targets. The form is built from the control
  // function catalog by value kind, so this file names no function. The result
  // area reports what the server stored, never a claim of delivery: devices
  // pull their control list, so "stored" is all the server can promise.
  import { onMount, untrack } from 'svelte'
  import FunctionField from './FunctionField.svelte'
  import TabLink from './TabLink.svelte'
  import { requestPrograms } from '../lib/programsRequest'
  import { fetchJSON, postJSON } from '../lib/api'
  import { deviceLabel } from '../lib/devices'
  import { deviceIdFromHref, type AdminFSA, type AdminFSAList } from '../lib/fsa'
  import { listTemplates, type ProgramTemplate } from '../lib/derprogram'
  import {
    cancelFsaControl,
    canCancelFsaControl,
    createFsaControl,
    fsaControlStatus,
    holdingText,
    listFsaControls,
    saveFsaDefault,
    type FsaControlView,
  } from '../lib/fsacontrol'
  import type { DashboardDevice } from '../lib/dashboard'
  import {
    buildDeviceControl,
    buildFunctions,
    functionsText,
    initialValue,
    loadCatalog as loadControlCatalog,
    loadCurves as loadStoredCurves,
    primacyText,
    type CatalogRow,
    type CurveInfo,
    type DeviceControlBody,
    type FieldValue,
  } from '../lib/controlcatalog'
  import {
    canCancel,
    expectedDeviceReadTime,
    fmtTime,
    localDateTimeInput,
    keptControl,
    POLL_RATE_SECONDS,
    type DERControlListItem,
    type DERControlListResponse,
    type DERControlView,
    type DERProgramListResponse,
    type DERProgramView,
    type DeviceControlCreated,
  } from '../lib/dercontrol'

  let { devices }: { devices: DashboardDevice[] } = $props()

  let target = $state<'device' | 'fsa'>('device')
  // This panel owns the FSA list and reads it each time the FSA target is
  // chosen, never on entering the tab, so an FSA or assignment changed on
  // another tab is neither missing nor counted stale.
  let fsas = $state<AdminFSA[]>([])
  let fsasError = $state('')
  let fsasSeq = 0

  let catalog = $state<CatalogRow[]>([])
  let catalogError = $state('')
  let curves = $state<CurveInfo[]>([])
  let curvesError = $state('')

  let selectedDeviceId = $state('')
  let programs = $state<DERProgramView[]>([])
  let programsChecked = $state(false)
  let programsError = $state('')
  // The table shows the controls of one program, or of all when blank.
  let programFilter = $state('')

  // The FSA target: the FSA, one of its program templates, and the controls
  // the FSA holds on that template.
  let selectedFsaId = $state('')
  let templates = $state<ProgramTemplate[]>([])
  let templatesChecked = $state(false)
  let templatesError = $state('')
  let selectedTemplateId = $state('')
  let fsaControls = $state<FsaControlView[]>([])
  let fsaControlsError = $state('')
  let fsaControlsLoaded = $state(false)

  let mode = $state<'scheduled' | 'default'>('scheduled')
  let ticked = $state<Record<string, boolean>>({})
  let values = $state<Record<string, FieldValue>>({})
  let startNow = $state(true)
  let startAt = $state('')
  let durationMinutes = $state<number | null>(null)
  let description = $state('')

  let pendingBody = $state<DeviceControlBody | null>(null)
  let pendingSummary = $state('')
  let posting = $state(false)
  let result = $state('')
  let primacyLine = $state('')
  let ok = $state(false)

  // The selected device's stored default, read from the server on a device
  // switch and set from the reply of a default send. programHref is null when
  // it came from the read, which does not say which program holds it.
  let storedDefault = $state<{ deviceId: string; programHref: string | null; type: string; base: Record<string, unknown> } | null>(null)
  let defaultError = $state('')

  let controls = $state<DERControlListItem[]>([])
  let controlsError = $state('')
  let controlsLoaded = $state(false)
  // The server's clock at the last successful controls read (its Date
  // header), which decides which Active rows have ended; null when the
  // response carried none.
  let serverNow = $state<number | null>(null)
  let cancelTarget = $state<string | null>(null)
  let cancelReason = $state('')
  let cancelPosting = $state(false)
  let cancelResult = $state('')

  // programSeq/controlSeq are sequence guards: a device switch mid-flight
  // must not let a stale response overwrite a newer selection.
  let programSeq = 0
  let controlSeq = 0
  let templateSeq = 0
  let fsaControlSeq = 0
  // selectionSeq advances on every device, FSA or program change. A create
  // or cancel remembers the value it was sent under; a reply that finds it
  // changed is shown named with its own target, not as the new selection's.
  let selectionSeq = 0
  let defaultSeq = 0
  // Cleared on unmount so a reply that lands afterwards writes nothing.
  let mounted = true

  onMount(() => {
    void loadCatalog()
    void loadCurves()
    return () => {
      mounted = false
    }
  })

  async function loadCatalog() {
    const res = await loadControlCatalog()
    if (!mounted) return
    if (!res.ok) {
      catalogError = res.error
      return
    }
    catalogError = ''
    catalog = res.data
    for (const row of catalog) {
      ticked[row.name] = false
      values[row.name] = initialValue(row)
    }
  }

  async function loadFsas() {
    const seq = ++fsasSeq
    const res = await fetchJSON<AdminFSAList>('/api/fsas')
    if (!mounted || seq !== fsasSeq) return
    if (!res.ok) {
      fsasError = `FSAs could not be read: ${res.error}`
      return
    }
    fsasError = ''
    fsas = res.data.fsas ?? []
  }

  function switchTarget(next: 'device' | 'fsa') {
    if (next === target) return
    target = next
    dropSelectionState()
    if (next === 'fsa') void loadFsas()
  }

  async function loadCurves() {
    const res = await loadStoredCurves()
    if (!mounted) return
    if (!res.ok) {
      curvesError = res.error
      return
    }
    curvesError = ''
    curves = res.data
  }

  // A pending confirmation, a shown result and an open cancel belong to the
  // selection they were made under, so any change of device drops them.
  function dropSelectionState() {
    selectionSeq++
    pendingBody = null
    pendingSummary = ''
    result = ''
    primacyLine = ''
    cancelTarget = null
    cancelResult = ''
  }

  $effect(() => {
    const id = selectedDeviceId
    programs = []
    programsChecked = false
    programsError = ''
    programFilter = ''
    controls = []
    controlsError = ''
    controlsLoaded = false
    dropSelectionState()
    const pseq = ++programSeq
    const cseq = ++controlSeq
    const dseq = ++defaultSeq
    storedDefault = null
    defaultError = ''
    if (!id) return
    void loadPrograms(id, pseq)
    void loadControls(id, cseq)
    void loadDefault(id, dseq)
  })

  $effect(() => {
    const id = selectedFsaId
    templates = []
    templatesChecked = false
    templatesError = ''
    selectedTemplateId = ''
    fsaControls = []
    fsaControlsError = ''
    fsaControlsLoaded = false
    dropSelectionState()
    const seq = ++templateSeq
    if (!id) return
    void loadTemplates(id, seq)
  })

  $effect(() => {
    const templateId = selectedTemplateId
    const fsaId = untrack(() => selectedFsaId)
    fsaControls = []
    fsaControlsError = ''
    fsaControlsLoaded = false
    dropSelectionState()
    const seq = ++fsaControlSeq
    if (!fsaId || !templateId) return
    void loadFsaControls(fsaId, templateId, seq)
  })

  async function loadTemplates(fsaId: string, seq: number) {
    const res = await listTemplates(fsaId)
    if (seq !== templateSeq) return
    templatesChecked = true
    if (!res.ok) {
      templatesError = res.error
      templates = []
      return
    }
    templates = res.data.templates ?? []
  }

  async function loadFsaControls(fsaId: string, templateId: string, seq: number) {
    const res = await listFsaControls(fsaId, templateId)
    if (seq !== fsaControlSeq) return
    if (!res.ok) {
      fsaControlsError = res.error
      fsaControls = []
      return
    }
    fsaControlsError = ''
    fsaControlsLoaded = true
    fsaControls = res.data.controls ?? []
  }

  async function loadPrograms(deviceId: string, seq: number) {
    const res = await fetchJSON<DERProgramListResponse>(`/api/devices/${encodeURIComponent(deviceId)}/der-programs`)
    if (seq !== programSeq) return
    programsChecked = true
    if (!res.ok) {
      programsError = res.error
      programs = []
      return
    }
    programsError = ''
    programs = res.data.programs ?? []
  }

  async function loadDefault(deviceId: string, seq: number) {
    const res = await fetchJSON<{ mRID: string; type: string; derControlBase: Record<string, unknown> }>(
      `/api/devices/${encodeURIComponent(deviceId)}/default-control`,
    )
    if (seq !== defaultSeq) return
    if (!res.ok) {
      // 404 is the answer for a device with no default: nothing to show.
      if (res.status !== 404) defaultError = `The stored default could not be read: ${res.error}`
      return
    }
    storedDefault = { deviceId, programHref: null, type: res.data.type, base: res.data.derControlBase }
  }

  async function loadControls(deviceId: string, seq: number) {
    const res = await fetchJSON<DERControlListResponse>(`/api/der/controls?device=${encodeURIComponent(deviceId)}`)
    if (seq !== controlSeq) return
    if (!res.ok) {
      controlsError = res.error
      controls = []
      return
    }
    controlsError = ''
    controlsLoaded = true
    serverNow = res.serverTime === undefined ? null : Math.floor(res.serverTime / 1000)
    controls = res.data.controls ?? []
  }

  // reload re-reads the tables of the target a write was made for, which a
  // target switch while the write was in flight may no longer be the current
  // one.
  function reload(forTarget: 'device' | 'fsa' = target) {
    if (forTarget === 'fsa') {
      if (!selectedFsaId) return
      void loadTemplates(selectedFsaId, ++templateSeq)
      if (selectedTemplateId) void loadFsaControls(selectedFsaId, selectedTemplateId, ++fsaControlSeq)
      return
    }
    if (!selectedDeviceId) return
    void loadPrograms(selectedDeviceId, ++programSeq)
    void loadControls(selectedDeviceId, ++controlSeq)
  }

  const selectedTemplate = $derived(templates.find((t) => t.id === selectedTemplateId))

  // targetText names the selection in the summary and in a late reply.
  function targetText(): string {
    if (target === 'fsa') return `FSA ${selectedFsaId}, program ${selectedTemplate?.description ?? selectedTemplateId}`
    return `device ${selectedDeviceText()}`
  }

  function selectedDeviceText(): string {
    const device = devices.find((d) => deviceIdFromHref(d.href) === selectedDeviceId)
    return device === undefined ? selectedDeviceId : deviceLabel(device.lfdi, device.sfdi)
  }

  // send only builds and shows the pending request; it never posts. Only
  // confirmSend below calls postJSON, so a re-render or a repeated Send
  // click cannot issue a second request on its own.
  function send() {
    const fns = buildFunctions(catalog, ticked, values)
    const built = fns.ok
      ? buildDeviceControl({
          mode,
          target: targetText(),
          functions: fns.functions,
          startNow,
          startAtLocal: startAt,
          durationMinutes,
          description,
        })
      : fns
    if (built.ok && target === 'device' && !selectedDeviceId) {
      ok = false
      result = 'Pick a device first.'
      return
    }
    if (built.ok && target === 'fsa' && !selectedTemplate) {
      ok = false
      result = 'Pick an FSA and one of its programs first.'
      return
    }
    if (!built.ok) {
      ok = false
      result = built.error
      pendingBody = null
      pendingSummary = ''
      return
    }
    pendingBody = built.body
    pendingSummary = built.summary
    result = ''
    primacyLine = ''
  }

  // The picker opens at the moment the box is unchecked, not at page load.
  // A start time the user already typed is kept unless it has passed.
  function onStartNowChange(e: Event) {
    startNow = (e.currentTarget as HTMLInputElement).checked
    if (!startNow && (startAt === '' || Date.parse(startAt) < Date.now())) startAt = localDateTimeInput()
  }

  function editPending() {
    pendingBody = null
    pendingSummary = ''
  }

  // For a create 500, the server says the control may be live. stale means
  // the table now on screen belongs to another device.
  function keptCreateText(kept: { mRID: string; href: string }, stale: boolean): string {
    const where = stale ? 'Select that target to see it in the table' : 'Cancel it from the table if it is not wanted'
    return `The control was kept and may be live: ${kept.mRID} (${kept.href}). ${where}.`
  }

  // For a cancel 500, only what the server says: the cancellation may or may
  // not have taken, so the control still exists and its status is unknown.
  function keptCancelText(kept: { mRID: string; href: string }, stale: boolean): string {
    const where = stale ? 'Select that target to see its status' : 'The reloaded table shows its status'
    return `The control ${kept.mRID} (${kept.href}) still exists. ${where}.`
  }

  async function confirmSend() {
    if (!pendingBody || posting) return
    if (target === 'fsa') {
      await confirmFsaSend(pendingBody)
      return
    }
    posting = true
    const body = pendingBody
    const deviceId = selectedDeviceId
    const sentUnder = selectionSeq
    const sentTo = targetText()
    pendingBody = null
    pendingSummary = ''
    const res = await postJSON<DeviceControlCreated>(`/api/devices/${encodeURIComponent(deviceId)}/controls`, body)
    posting = false
    const stale = sentUnder !== selectionSeq
    const prefix = stale ? `For ${sentTo}: ` : ''
    if (!res.ok) {
      ok = false
      const kept = keptControl(res.body)
      result = `${prefix}Error: ${res.error}` + (kept ? `. ${keptCreateText(kept, stale)}` : '')
      primacyLine = ''
      reload('device')
      return
    }
    ok = true
    const d = res.data
    primacyLine = prefix + primacyText(d.primacy, d.primacyReason)
    if (d.control) {
      const c = d.control
      let text =
        prefix +
        `Stored ${c.mRID} at ${fmtTime(c.creationTime)}; starts ${fmtTime(c.interval.start)}. ` +
        `Status: ${c.eventStatus.status} (as of ${fmtTime(c.eventStatus.dateTime)}). ` +
        `Nominal estimate only, not a delivery: a device polling every ${POLL_RATE_SECONDS} s would read it by ` +
        `${expectedDeviceReadTime(c.creationTime)} (stored time + ${POLL_RATE_SECONDS} s, computed here).`
      if (c.supersedes.length > 0) {
        text += ` Superseded ${c.supersedes.length} earlier control${c.supersedes.length === 1 ? '' : 's'}.`
      }
      if (!c.persisted) {
        text += ' A server restart forgets this control.'
      }
      result = text
    } else if (d.defaultControl) {
      result = `${prefix}Stored the default control ${d.defaultControl.mRID} on program ${d.programHref}. It is in force now, has no end, and devices send no responses.`
      if (!stale) {
        defaultSeq++
        defaultError = ''
        storedDefault = { deviceId, programHref: d.programHref, type: d.defaultControl.type, base: d.defaultControl.derControlBase }
      }
    } else {
      result = `${prefix}Stored on program ${d.programHref}.`
    }
    if (!stale) programFilter = ''
    reload('device')
  }

  type FsaSent = { ok: true; text: string } | { ok: false; error: string; body?: unknown }

  async function postFsaSend(fsaId: string, template: ProgramTemplate, body: DeviceControlBody): Promise<FsaSent> {
    if (body.kind === 'default') {
      const saved = await saveFsaDefault(fsaId, template, body.functions)
      if (!saved.ok) return saved
      return {
        ok: true,
        text: `Stored the default control on program ${template.description} of FSA ${fsaId}. Every device assigned to the FSA takes it; it is in force now, has no end, and devices send no responses.`,
      }
    }
    const created = await createFsaControl(fsaId, template.id, body)
    if (!created.ok) return created
    const c = created.data
    return {
      ok: true,
      text:
        `Stored ${c.mRID} at ${fmtTime(c.creationTime)}; starts ${fmtTime(c.interval.start)}. ` +
        `Held by ${holdingText(c)}.` +
        (c.persisted ? '' : ' A server restart forgets this control.'),
    }
  }

  // confirmFsaSend posts to the FSA's program: a default through the
  // template's edit route, a scheduled control to the FSA control route.
  async function confirmFsaSend(body: DeviceControlBody) {
    const template = selectedTemplate
    if (!template) return
    posting = true
    const fsaId = selectedFsaId
    const sentUnder = selectionSeq
    const sentTo = targetText()
    pendingBody = null
    pendingSummary = ''
    const res = await postFsaSend(fsaId, template, body)
    posting = false
    const stale = sentUnder !== selectionSeq
    const prefix = stale ? `For ${sentTo}: ` : ''
    if (!res.ok) {
      ok = false
      const kept = keptControl(res.body)
      result = `${prefix}Error: ${res.error}` + (kept ? `. ${keptCreateText(kept, stale)}` : '')
      reload('fsa')
      return
    }
    ok = true
    result = prefix + res.text
    reload('fsa')
  }

  function startCancel(mrid: string) {
    cancelTarget = mrid
    cancelReason = ''
    cancelResult = ''
  }

  function abortCancel() {
    cancelTarget = null
  }

  function onFilterChange() {
    cancelTarget = null
    cancelResult = ''
  }

  async function confirmCancel(mrid: string) {
    if (cancelPosting) return
    cancelPosting = true
    const sentUnder = selectionSeq
    const sentTo = targetText()
    const body: { reason?: string } = {}
    if (cancelReason) body.reason = cancelReason
    // An FSA cancel reaches the control on every device the FSA holds it on.
    const fsaControl = target === 'fsa' ? fsaControls.find((c) => c.mRID === mrid) : undefined
    const res = fsaControl
      ? await cancelFsaControl(selectedFsaId, selectedTemplateId, fsaControl.id, cancelReason)
      : await postJSON<DERControlView>(`/api/der/controls/${encodeURIComponent(mrid)}/cancel`, body)
    cancelPosting = false
    cancelTarget = null
    const stale = sentUnder !== selectionSeq
    if (!res.ok) {
      const kept = keptControl(res.body)
      const text = kept ? `${res.error}. ${keptCancelText(kept, stale)}` : `Cancel failed: ${res.error}`
      if (stale) {
        // The table and its cancel box now belong to another device.
        ok = false
        result = `For ${sentTo}, control ${mrid}: ${text}`
      } else {
        cancelResult = text
      }
    } else if (!stale) {
      cancelResult = ''
    }
    reload(fsaControl ? 'fsa' : 'device')
  }

  // liveOnly narrows both control tables to what is pending or running, so
  // the controls still to come are not buried under cancelled and finished ones.
  let liveOnly = $state(false)
  const inProgram = $derived(programFilter ? controls.filter((c) => c.derProgramHref === programFilter) : controls)
  const isLive = (c: DERControlListItem): boolean => canCancel(c.eventStatus.status, c.interval.start, c.interval.duration, serverNow)
  const shownControls = $derived(liveOnly ? inProgram.filter(isLive) : inProgram)
  const liveControlCount = $derived(inProgram.filter(isLive).length)
  const shownFsaControls = $derived(liveOnly ? fsaControls.filter(canCancelFsaControl) : fsaControls)
  const liveFsaControlCount = $derived(fsaControls.filter(canCancelFsaControl).length)
  const shownDefault = $derived(storedDefault && storedDefault.deviceId === selectedDeviceId ? storedDefault : null)
  const primacyOf = (href: string): string => {
    const p = programs.find((x) => x.href === href)
    return p ? String(p.primacy) : '-'
  }
  const shownFsaDefault = $derived(selectedTemplate?.defaultControl ?? null)
  const programName = (p: DERProgramView): string => p.description || p.mRID
  const controlCount = (href: string): number => controls.filter((c) => c.derProgramHref === href).length
</script>

{#snippet cancelCell(mrid: string)}
  {#if cancelTarget === mrid}
    <input type="text" placeholder="reason (optional)" style="max-width: 100px;" bind:value={cancelReason} />
    <button
      class="btn btn-red btn-small"
      data-testid="der-control-confirm-cancel-{mrid}"
      disabled={cancelPosting}
      onclick={() => confirmCancel(mrid)}>Confirm Cancel</button
    >
    <button class="btn btn-small" disabled={cancelPosting} onclick={abortCancel}>Back</button>
  {:else}
    <button class="btn btn-red btn-small" data-testid="der-control-cancel-{mrid}" onclick={() => startCancel(mrid)}>Cancel</button
    >
  {/if}
{/snippet}

<div class="card full-width">
  <h2>Send DER Control</h2>

  <div class="form-row">
    <span class="stat-label">Target</span>
    <label>
      <input type="radio" name="controlTarget" data-testid="control-target-device" checked={target === 'device'} onchange={() => switchTarget('device')} />
      Device
    </label>
    <label>
      <input type="radio" name="controlTarget" data-testid="control-target-fsa" checked={target === 'fsa'} onchange={() => switchTarget('fsa')} />
      FSA
    </label>
    {#if target === 'device'}
      <select id="controlDevice" aria-label="Device" bind:value={selectedDeviceId} disabled={posting || cancelPosting}>
        <option value="">(pick device)</option>
        {#each devices as device (device.href)}
          <option value={deviceIdFromHref(device.href)}>{deviceLabel(device.lfdi, device.sfdi)}</option>
        {/each}
      </select>
    {:else}
      <select id="controlFsa" aria-label="FSA" bind:value={selectedFsaId} disabled={posting || cancelPosting}>
        <option value="">(pick FSA)</option>
        {#each fsas as fsa (fsa.mRID)}
          <option value={fsa.mRID}>{fsa.description || fsa.mRID}</option>
        {/each}
      </select>
      <select id="controlFsaProgram" aria-label="Program" bind:value={selectedTemplateId} disabled={posting || cancelPosting || !selectedFsaId}>
        <option value="">(pick program)</option>
        {#each templates as template (template.id)}
          <option value={template.id}>{template.description}</option>
        {/each}
      </select>
    {/if}
  </div>

  {#if target === 'fsa'}
    {#if fsasError}
      <div class="result err" data-testid="fsa-control-fsas-error">{fsasError}</div>
    {/if}
    {#if templatesError}
      <div class="result err" data-testid="fsa-control-templates-error">{templatesError}</div>
    {:else if selectedFsaId && templatesChecked && templates.length === 0}
      <p class="hint" data-testid="fsa-control-no-templates">
        This FSA has no program templates yet. Add one on the <TabLink path="/ui/fsas">FSAs tab</TabLink>.
      </p>
    {/if}
    {#if selectedFsaId}
      <p class="hint" data-testid="fsa-control-reach">
        Reaches the {fsas.find((f) => f.mRID === selectedFsaId)?.devices?.length ?? 0} devices assigned to this FSA, and any assigned later.
      </p>
    {/if}
  {/if}

  {#if target === 'device' && programsError}
    <div class="result err" data-testid="der-control-programs-error">{programsError}</div>
  {:else if target === 'device' && selectedDeviceId && programsChecked && programs.length === 0}
    <p class="hint" data-testid="der-control-no-programs">
      This device has no DER programs yet; sending a control creates one for this device alone. Program templates are
      added on the <TabLink path="/ui/fsas">FSAs tab</TabLink>, and a device's programs are managed on the
      <TabLink path="/ui/devices" testid="der-control-devices-link" onNavigate={() => requestPrograms(selectedDeviceId)}
        >Devices tab</TabLink
      >.
    </p>
  {/if}

  <div class="form-row">
    <label>
      <input type="radio" name="controlMode" value="default" data-testid="control-mode-default" bind:group={mode} />
      Default control: in force now, no end, devices send no responses
    </label>
    <label>
      <input type="radio" name="controlMode" value="scheduled" data-testid="control-mode-scheduled" bind:group={mode} />
      Scheduled control
    </label>
  </div>

  {#if catalogError}
    <div class="result err" data-testid="control-catalog-error">Functions could not be read: {catalogError}</div>
  {/if}
  <div class="control-functions" data-testid="control-functions">
    {#each catalog as row (row.name)}
      <FunctionField {row} bind:ticked={ticked[row.name]} bind:value={values[row.name]} {curves} {curvesError} />
    {/each}
  </div>

  {#if mode === 'scheduled'}
    <div class="form-row">
      <label>
        <input type="checkbox" id="controlStartNow" checked={startNow} onchange={onStartNowChange} /> Start now
      </label>
      {#if !startNow}
        <input type="datetime-local" id="controlStartAt" step="1" bind:value={startAt} />
      {/if}
      <input
        type="number"
        id="controlDuration"
        placeholder="Duration (minutes)"
        min="1"
        style="max-width: 120px;"
        bind:value={durationMinutes}
      />
      <input type="text" id="controlDescription" placeholder="Description (optional)" bind:value={description} />
    </div>
  {/if}

  {#if pendingSummary}
    <div class="form-row" data-testid="der-control-confirm">
      <span>{pendingSummary}</span>
      <button class="btn btn-green" onclick={confirmSend} disabled={posting}>Confirm</button>
      <button class="btn btn-small" onclick={editPending} disabled={posting}>Edit</button>
    </div>
  {:else}
    <div class="form-row">
      <button class="btn btn-green" onclick={send}>Send</button>
    </div>
  {/if}

  <div class="result" class:ok class:err={result !== '' && !ok} id="controlResult">{result}</div>
  {#if primacyLine}
    <div class="hint" data-testid="der-control-primacy">{primacyLine}</div>
  {/if}

  {#if target === 'device' && selectedDeviceId && programs.length > 0}
    <table id="derProgramsTable">
      <thead>
        <tr><th>Program</th><th>Primacy</th><th>Controls</th></tr>
      </thead>
      <tbody>
        {#each programs as program (program.href)}
          <tr data-testid="der-control-program-row">
            <td>{programName(program)}</td>
            <td>{program.primacy}</td>
            <td>{controlCount(program.href)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
    <div class="form-row">
      <select id="controlProgram" bind:value={programFilter} onchange={onFilterChange} disabled={posting || cancelPosting}>
        <option value="">(all programs)</option>
        {#each programs as program (program.href)}
          <option value={program.href}>{programName(program)}</option>
        {/each}
      </select>
    </div>
  {/if}

  {#if target === 'device' && defaultError}
    <div class="result err" data-testid="der-control-default-error">{defaultError}</div>
  {/if}

  {#if (target === 'device' && selectedDeviceId) || (target === 'fsa' && selectedTemplate)}
    <div class="form-row">
      {#if target === 'device' ? controlsLoaded && !controlsError : fsaControlsLoaded && !fsaControlsError}
        <span data-testid="der-control-live-count">
          {target === 'device' ? liveControlCount : liveFsaControlCount} pending or active
        </span>
      {/if}
      <label>
        <input type="checkbox" data-testid="der-control-live-only" bind:checked={liveOnly} />
        Show pending and active only
      </label>
    </div>
  {/if}

  {#if target === 'device' && selectedDeviceId}
    <table id="derControlsTable">
      <thead>
        <tr>
          <th>Type</th>
          <th>Value</th>
          <th>Program (primacy)</th>
          <th>Start</th>
          <th>End</th>
          <th>Status</th>
          <th>Responses (reported by device, not verified by server)</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        {#if shownDefault}
          <tr data-testid="der-control-default-row">
            <td>default</td>
            <td>{functionsText(shownDefault.base)}</td>
            <td>{shownDefault.programHref ? `${shownDefault.programHref} (${primacyOf(shownDefault.programHref)})` : '-'}</td>
            <td colspan="5" class="stat-label">Stored default; in force now, no end, no responses</td>
          </tr>
        {/if}
        {#if controlsError}
          <tr>
            <td colspan="8" class="result err" data-testid="der-control-controls-error">{controlsError}</td>
          </tr>
        {:else if shownControls.length === 0}
          <tr>
            <td colspan="8" class="stat-label" data-testid="der-control-empty">
              {liveOnly && inProgram.length > 0 ? 'No pending or active controls for this selection' : 'No admin-issued controls for this selection'}
            </td>
          </tr>
        {:else}
          {#each shownControls as ctrl (ctrl.mRID)}
            <tr data-testid="der-control-row" data-mrid={ctrl.mRID}>
              <td>{ctrl.type}</td>
              <td>{functionsText(ctrl.functions)}</td>
              <td>{ctrl.derProgramHref} ({primacyOf(ctrl.derProgramHref)})</td>
              <td>{fmtTime(ctrl.interval.start)}</td>
              <td>{fmtTime(ctrl.interval.start + ctrl.interval.duration)}</td>
              <td data-testid="der-control-status">{ctrl.eventStatus.status}{ctrl.ended && ctrl.eventStatus.status === 'active' ? ' (ended)' : ''}</td>
              <td>
                {ctrl.responses.total} reported
                {#if ctrl.responses.total > 0}
                  ({Object.entries(ctrl.responses.byStatus)
                    .map(([status, count]) => `${status}: ${count}`)
                    .join(', ')})
                {/if}
              </td>
              <td>
                {#if canCancel(ctrl.eventStatus.status, ctrl.interval.start, ctrl.interval.duration, serverNow)}
                  {@render cancelCell(ctrl.mRID)}
                {/if}
              </td>
            </tr>
          {/each}
        {/if}
      </tbody>
    </table>
  {/if}

  {#if target === 'fsa' && selectedTemplate}
    <table id="fsaControlsTable">
      <thead>
        <tr>
          <th>Type</th>
          <th>Value</th>
          <th>Start</th>
          <th>End</th>
          <th>Status</th>
          <th>Held by</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        {#if shownFsaDefault}
          <tr data-testid="fsa-control-default-row">
            <td>default</td>
            <td>{functionsText(shownFsaDefault.derControlBase)}</td>
            <td colspan="5" class="stat-label">Stored default; in force now, no end, no responses</td>
          </tr>
        {/if}
        {#if fsaControlsError}
          <tr>
            <td colspan="7" class="result err" data-testid="fsa-control-controls-error">{fsaControlsError}</td>
          </tr>
        {:else if shownFsaControls.length === 0}
          <tr>
            <td colspan="7" class="stat-label" data-testid="fsa-control-empty">
              {liveOnly && fsaControls.length > 0 ? 'No pending or active controls for this program' : 'No admin-issued controls for this program'}
            </td>
          </tr>
        {:else}
          {#each shownFsaControls as ctrl (ctrl.id)}
            <tr data-testid="fsa-control-row" data-mrid={ctrl.mRID}>
              <td>{ctrl.type}</td>
              <td>{functionsText(ctrl.functions)}</td>
              <td>{fmtTime(ctrl.interval.start)}</td>
              <td>{fmtTime(ctrl.interval.start + ctrl.interval.duration)}</td>
              <td data-testid="fsa-control-status">{fsaControlStatus(ctrl)}</td>
              <td data-testid="fsa-control-holding">{holdingText(ctrl)}</td>
              <td>
                {#if canCancelFsaControl(ctrl)}
                  {@render cancelCell(ctrl.mRID)}
                {/if}
              </td>
            </tr>
          {/each}
        {/if}
      </tbody>
    </table>
  {/if}

  {#if cancelResult}
    <div class="result err" data-testid="der-control-cancel-result">{cancelResult}</div>
  {/if}
</div>
