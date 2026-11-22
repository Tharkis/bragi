<script>
  import { onMount } from 'svelte'
  import { get, post, put, del } from '../lib/api.js'
  import { onEvents } from '../lib/events.js'

  let libraries = $state([])
  let presets = $state([])
  let adding = $state(false)
  let draft = $state({ mediaType: 'tv', name: '', path: '/data/media/tv', qualityPresetId: null })
  const defaultPaths = { tv: '/data/media/tv', movie: '/data/media/movies', music: '/data/media/music', book: '/data/media/books', audiobook: '/data/media/audiobooks' }
  // Suggest the usual folder for the type until the path is edited.
  function pickType(t) {
    if (Object.values(defaultPaths).includes(draft.path)) draft.path = defaultPaths[t]
    if (usesPreset(t) && !presetsFor(t).some((p) => p.id === draft.qualityPresetId)) draft.qualityPresetId = presetsFor(t)[0]?.id ?? null
  }
  let error = $state('')

  const types = { tv: 'TV', movie: 'Movies', music: 'Music', book: 'Books', audiobook: 'Audiobooks' }
  // Video and music have quality presets; books rank by file format instead.
  const usesPreset = (t) => t === 'tv' || t === 'movie' || t === 'music'
  const presetsFor = (t) => presets.filter((p) => (p.mediaType || 'video') === (t === 'music' ? 'music' : 'video'))

  async function load() {
    try {
      ;[libraries, presets] = await Promise.all([get('/libraries'), get('/qualitypresets')])
      if (draft.qualityPresetId === null) draft.qualityPresetId = presetsFor(draft.mediaType)[0]?.id ?? null
    } catch (e) {
      error = e.message
    }
  }
  let scans = $state(null)
  async function loadScans() {
    try {
      scans = await get('/libraries/scan/all')
    } catch (e) {
      error = e.message
    }
  }
  onMount(() => {
    load()
    loadScans()
    return onEvents(['scan'], loadScans)
  })

  async function scanAll() {
    error = ''
    try {
      scans = await post('/libraries/scan/all')
    } catch (e) {
      error = e.message
    }
  }

  async function setNightly(on) {
    try {
      await put('/settings', { nightly_scan: on ? 'true' : 'false' })
      await loadScans()
    } catch (e) {
      error = e.message
    }
  }

  const hourLabel = (h) => new Date(2000, 0, 1, h).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
  const when = (iso) => {
    if (!iso) return 'never'
    const d = new Date(iso)
    return d.toLocaleString([], { weekday: 'short', hour: 'numeric', minute: '2-digit' })
  }
  const runningType = $derived(scans && Object.values(scans.types).find((t) => t.running))

  const presetName = (id, t) => presets.find((p) => p.id === id)?.name ?? (t === 'music' ? 'Lossless first' : 'None')

  async function add(event) {
    event.preventDefault()
    error = ''
    try {
      await post('/libraries', { ...draft, qualityPresetId: usesPreset(draft.mediaType) ? draft.qualityPresetId : null })
      adding = false
      await load()
    } catch (e) {
      error = e.message
    }
  }

  async function remove(lib) {
    if (!window.confirm(`Remove ${lib.name}? Files on disk are not touched.`)) return
    try {
      await del(`/libraries/${lib.id}`)
      await load()
    } catch (e) {
      error = e.message
    }
  }
</script>

<div class="spread" style="margin-bottom: 12px">
  <div>
    <h2 style="margin: 0">Libraries</h2>
    <p class="muted small" style="margin: 0">The folders Bragi imports into. Paths are as seen inside the container.</p>
  </div>
  {#if !adding}<button class="primary" onclick={() => (adding = true)}>Add</button>{/if}
</div>

{#if error}<div class="notice bad">{error}</div>{/if}

{#if adding}
  <form class="card" style="margin-bottom: 16px" onsubmit={add}>
    <label>
      <span>Media type</span>
      <select bind:value={draft.mediaType} onchange={(e) => pickType(e.currentTarget.value)}>
        {#each Object.entries(types) as [id, name] (id)}<option value={id}>{name}</option>{/each}
      </select>
    </label>
    <label><span>Folder</span><input bind:value={draft.path} required /></label>
    <label><span>Name (optional)</span><input bind:value={draft.name} placeholder="Taken from the folder name" /></label>
    {#if usesPreset(draft.mediaType)}
      <label>
        <span>Quality preset</span>
        <select bind:value={draft.qualityPresetId}>
          {#each presetsFor(draft.mediaType) as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
        </select>
      </label>
    {:else if draft.mediaType === 'book' || draft.mediaType === 'audiobook'}
      <p class="small muted">
        {draft.mediaType === 'audiobook' ? 'Bragi prefers M4B, then MP3.' : 'Bragi prefers EPUB, then AZW3, MOBI and PDF.'}
      </p>
    {/if}
    <div class="row">
      <button class="primary">Add library</button>
      <button type="button" class="link" onclick={() => (adding = false)}>Cancel</button>
    </div>
  </form>
{/if}

{#if libraries.length}
  <div class="card" style="padding: 4px 12px">
    <table>
      <thead><tr><th>Name</th><th>Type</th><th>Quality</th><th></th></tr></thead>
      <tbody>
        {#each libraries as lib (lib.id)}
          <tr>
            <td><div>{lib.name}</div><div class="muted small">{lib.path}</div></td>
            <td>{types[lib.mediaType]}</td>
            <td>{usesPreset(lib.mediaType) ? presetName(lib.qualityPresetId, lib.mediaType) : lib.mediaType === 'audiobook' ? 'M4B first' : lib.mediaType === 'book' ? 'EPUB first' : '—'}</td>
            <td class="actions"><button class="danger" onclick={() => remove(lib)}>Remove</button></td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{:else if !adding}
  <div class="card muted">None yet.</div>
{/if}

{#if libraries.length && scans}
  <div class="card scanall">
    <div class="spread">
      <div>
        <strong>Find what's already there</strong>
        <div class="small muted">
          Scanning adds the shows, movies and books already in these folders, as they are. Nothing is moved, renamed or downloaded.
        </div>
      </div>
      <button onclick={scanAll} disabled={scans.running}>{scans.running ? 'Scanning…' : 'Scan all libraries'}</button>
    </div>
    {#if runningType}
      <div class="small">Scanning {runningType.library}{runningType.total ? `: ${runningType.done} of ${runningType.total}` : '…'}</div>
      <div class="bar"><div style="width: {runningType.total ? (100 * runningType.done) / runningType.total : 0}%"></div></div>
    {/if}
    <label class="check">
      <input type="checkbox" checked={scans.nightly} onchange={(e) => setNightly(e.currentTarget.checked)} />
      <span>Scan every library each night at {hourLabel(scans.hour)} to pick up files added outside Bragi</span>
    </label>
    <div class="small muted">Last full scan: {when(scans.lastFull)}</div>
  </div>
{/if}

<style>
  .scanall {
    margin-top: 16px;
    display: grid;
    gap: 10px;
  }
</style>
