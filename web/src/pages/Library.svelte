<script>
  import { onMount } from 'svelte'
  import { get, post } from '../lib/api.js'
  import { link } from '../lib/router.svelte.js'
  import { onEvents } from '../lib/events.js'
  import Poster from '../components/Poster.svelte'

  let { me } = $props()
  const admin = me.role === 'admin'

  let shows = $state([])
  let loaded = $state(false)
  let error = $state('')
  let scan = $state(null)
  let filter = $state('')

  async function load() {
    try {
      shows = await get('/series')
      if (admin) scan = await get('/libraries/scan')
      error = ''
    } catch (e) {
      error = e.message
    } finally {
      loaded = true
    }
  }

  onMount(() => {
    load()
    return onEvents(['series', 'episodes', 'download', 'scan'], load)
  })

  async function startScan() {
    error = ''
    try {
      scan = await post('/libraries/scan')
    } catch (e) {
      error = e.message
    }
  }

  const shown = $derived(
    filter ? shows.filter((s) => s.title.toLowerCase().includes(filter.toLowerCase())) : shows,
  )

  function summary(s) {
    if (s.activities) return [`Downloading ${s.activities}`, 'accent']
    if (s.missing) return [`${s.missing} missing`, 'warn']
    if (s.episodes) return ['Complete', 'good']
    return ['Waiting for new episodes', '']
  }
</script>

<div class="page-head">
  <div>
    <h1>Library</h1>
    <p class="muted">{shows.length} {shows.length === 1 ? 'show' : 'shows'}</p>
  </div>
  <div class="row">
    {#if shows.length > 8}<input class="filter" placeholder="Filter…" bind:value={filter} aria-label="Filter shows" />{/if}
    {#if admin}
      <button onclick={startScan} disabled={scan?.running}>{scan?.running ? 'Scanning…' : 'Scan TV folder'}</button>
    {/if}
  </div>
</div>

{#if error}<div class="notice bad">{error}</div>{/if}

{#if scan?.running}
  <div class="card scan">
    <div class="small">Looking through {scan.library}: {scan.done} of {scan.total} folders</div>
    <div class="bar"><div style="width: {scan.total ? (100 * scan.done) / scan.total : 0}%"></div></div>
  </div>
{:else if scan?.finished && (scan.added.length || scan.unmatched.length)}
  <details class="card scan">
    <summary class="small">
      Last scan added {scan.added.length} {scan.added.length === 1 ? 'show' : 'shows'} and linked {scan.files} files.
      {#if scan.unmatched.length}{scan.unmatched.length} folders were not recognised.{/if}
    </summary>
    {#if scan.unmatched.length}
      <ul class="small muted">
        {#each scan.unmatched as u (u)}<li>{u}</li>{/each}
      </ul>
      <p class="small muted">Rename a folder to “Title (Year)” and scan again.</p>
    {/if}
  </details>
{/if}

{#if loaded && shows.length === 0}
  <div class="card empty muted">
    <p>Nothing here yet.</p>
    <p class="small">
      <a href="/discover" onclick={link}>Request a show</a>{#if admin}, or press Scan TV folder to add the shows you already have{/if}.
    </p>
  </div>
{/if}

<div class="poster-grid">
  {#each shown as s (s.id)}
    {@const [label, tone] = summary(s)}
    <a href={`/library/${s.id}`} onclick={link}>
      <Poster url={s.posterUrl} title={s.title} />
      <div class="title">{s.title}</div>
      <div class="small muted">{s.year || ''} · {s.imported}/{s.episodes} episodes</div>
      <span class="badge {tone}">{label}</span>
    </a>
  {/each}
</div>

<style>
  .filter {
    width: 180px;
  }
  .scan {
    margin-bottom: 18px;
    padding: 12px 14px;
    display: grid;
    gap: 6px;
  }
  .scan ul {
    margin: 8px 0;
  }
  .poster-grid .badge {
    margin-top: 4px;
  }
</style>
