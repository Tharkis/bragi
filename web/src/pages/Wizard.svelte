<script>
  // First-run setup: library, indexer, download client. The admin account
  // already exists by the time this runs.
  import { onMount } from 'svelte'
  import { get, post, put } from '../lib/api.js'
  import IntegrationForm from '../components/IntegrationForm.svelte'
  import ConnectionsSection from '../components/ConnectionsSection.svelte'

  let { onDone } = $props()

  const steps = ['TV library', 'Show information', 'Indexer', 'Download client', 'Done']
  let step = $state(0)
  let kinds = $state([])
  let presets = $state([])
  let library = $state({ mediaType: 'tv', name: 'TV', path: '/data/media/tv', qualityPresetId: null })
  let error = $state('')
  let busy = $state(false)
  let added = $state({ library: null, tmdb: false, indexer: null, client: null })
  let scanExisting = $state(true)

  onMount(async () => {
    try {
      ;[kinds, presets] = await Promise.all([get('/integrations/kinds'), get('/qualitypresets')])
      library.qualityPresetId = presets[0]?.id ?? null
      const [libs, indexers, clients, settings] = await Promise.all([
        get('/libraries'),
        get('/indexers'),
        get('/downloadclients'),
        get('/settings'),
      ])
      // Resume where a previous run of the wizard stopped.
      added = { library: libs[0] ?? null, tmdb: !!settings.tmdb_api_key, indexer: indexers[0] ?? null, client: clients[0] ?? null }
      step = !added.library ? 0 : !added.tmdb ? 1 : !added.indexer ? 2 : !added.client ? 3 : 4
    } catch (e) {
      error = e.message
    }
  })

  async function saveLibrary(event) {
    event.preventDefault()
    busy = true
    error = ''
    try {
      added.library = await post('/libraries', library)
      step = 1
    } catch (e) {
      error = e.message
    } finally {
      busy = false
    }
  }

  async function finish() {
    busy = true
    try {
      await put('/settings', { setup_complete: 'true' })
      if (scanExisting && added.tmdb) await post('/libraries/scan').catch(() => {})
      onDone()
    } catch (e) {
      error = e.message
      busy = false
    }
  }
</script>

<div class="center-page">
  <div class="wide">
    <div class="brand" style="margin-bottom: 16px"><img src="/icon.svg" alt="" /> Bragi setup</div>
    <ol class="steps">
      {#each steps as s, i (s)}
        <li class:done={i < step} class:current={i === step}>{s}</li>
      {/each}
    </ol>

    <div class="card">
      {#if error}<div class="notice bad">{error}</div>{/if}

      {#if step === 0}
        <h2>Where should TV shows go?</h2>
        <p class="muted small">
          Use a folder on the same <code>/data</code> mount as your downloads, so Bragi can hardlink instead of copy.
        </p>
        <form onsubmit={saveLibrary}>
          <label><span>TV folder</span><input bind:value={library.path} required /></label>
          <label>
            <span>Quality</span>
            <select bind:value={library.qualityPresetId}>
              {#each presets as p (p.id)}<option value={p.id}>{p.name}: {p.description}</option>{/each}
            </select>
          </label>
          <button class="primary" disabled={busy}>Continue</button>
        </form>
      {:else if step === 1}
        <h2>Where should show information come from?</h2>
        <ConnectionsSection
          compact
          onSaved={() => {
            added.tmdb = true
            step = 2
          }}
        />
        <p class="small" style="margin: 12px 0 0"><button class="link" onclick={() => (step = 2)}>Skip for now</button></p>
      {:else if step === 2}
        <h2>Connect an indexer</h2>
        <p class="muted small">Prowlarr is easiest: one connection covers every indexer you set up there.</p>
        <IntegrationForm
          endpoint="/indexers"
          kinds={kinds.filter((k) => k.role === 'indexer')}
          saveLabel="Save and continue"
          onSaved={(i) => {
            added.indexer = i
            step = 3
          }}
        />
        <p class="small" style="margin: 12px 0 0"><button class="link" onclick={() => (step = 3)}>Skip for now</button></p>
      {:else if step === 3}
        <h2>Connect a download client</h2>
        <IntegrationForm
          endpoint="/downloadclients"
          kinds={kinds.filter((k) => k.role === 'downloader')}
          initial={{ category: 'tv' }}
          saveLabel="Save and continue"
          onSaved={(c) => {
            added.client = c
            step = 4
          }}
        />
        <p class="small" style="margin: 12px 0 0"><button class="link" onclick={() => (step = 4)}>Skip for now</button></p>
      {:else}
        <h2>All set</h2>
        <ul class="summary">
          <li>TV library: {added.library ? added.library.path : 'not set'}</li>
          <li>Show information: {added.tmdb ? 'TMDB' : 'skipped'}</li>
          <li>Indexer: {added.indexer ? added.indexer.name : 'skipped'}</li>
          <li>Download client: {added.client ? added.client.name : 'skipped'}</li>
        </ul>
        {#if added.library && added.tmdb}
          <label class="check">
            <input type="checkbox" bind:checked={scanExisting} />
            <span>Add the shows already in {added.library.path}. Nothing is moved or downloaded.</span>
          </label>
        {/if}
        <p class="muted small">You can change any of this later in Settings.</p>
        <button class="primary" onclick={finish} disabled={busy}>Open Bragi</button>
      {/if}
    </div>
  </div>
</div>

<style>
  .steps {
    display: flex;
    gap: 8px;
    list-style: none;
    padding: 0;
    margin: 0 0 16px;
    flex-wrap: wrap;
  }
  .steps li {
    font-size: 0.85rem;
    padding: 4px 10px;
    border-radius: 999px;
    border: 1px solid var(--border);
    color: var(--muted);
  }
  .steps li.done {
    color: var(--good);
    border-color: transparent;
    background: var(--good-soft);
  }
  .steps li.current {
    color: var(--accent);
    border-color: var(--accent);
    font-weight: 600;
  }
  .summary {
    padding-left: 18px;
    margin: 0 0 12px;
  }
</style>
