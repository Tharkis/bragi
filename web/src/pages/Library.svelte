<script>
  import { onMount } from 'svelte'
  import { get } from '../lib/api.js'
  import { link } from '../lib/router.svelte.js'
  import { onEvents } from '../lib/events.js'
  import Poster from '../components/Poster.svelte'
  import { movieState } from '../lib/status.js'
  import MediaTabs from '../components/MediaTabs.svelte'
  import ScanPanel from '../components/ScanPanel.svelte'

  let { me, kind = 'tv' } = $props()
  const admin = me.role === 'admin'
  const movies = kind === 'movie'
  const noun = movies ? ['movie', 'movies'] : ['show', 'shows']

  let shows = $state([])
  let loaded = $state(false)
  let error = $state('')
  let panel = $state(null)
  let scanning = $state(false)
  let filter = $state('')

  async function load() {
    try {
      shows = await get(movies ? '/movies' : '/series')
      error = ''
    } catch (e) {
      error = e.message
    } finally {
      loaded = true
    }
  }

  onMount(() => {
    load()
    return onEvents(movies ? ['movie', 'download'] : ['series', 'episodes', 'download'], load)
  })

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
    <p class="muted">{shows.length} {shows.length === 1 ? noun[0] : noun[1]}</p>
  </div>
  <div class="row">
    {#if shows.length > 8}<input class="filter" placeholder="Filter…" bind:value={filter} aria-label="Filter {noun[1]}" />{/if}
    {#if admin}
      <button onclick={() => panel?.start()} disabled={scanning}>
        {scanning ? 'Scanning…' : movies ? 'Scan movie folder' : 'Scan TV folder'}
      </button>
    {/if}
  </div>
</div>

<MediaTabs base="/library" type={kind} />

{#if error}<div class="notice bad">{error}</div>{/if}

{#if admin}<ScanPanel type={kind} bind:this={panel} bind:busy={scanning} onChange={load} />{/if}

{#if loaded && shows.length === 0}
  <div class="card empty muted">
    <p>Nothing here yet.</p>
    <p class="small">
      <a href="/discover" onclick={link}>Request a {noun[0]}</a>{#if admin}, or press {movies ? 'Scan movie folder' : 'Scan TV folder'} to add the
        {noun[1]} you already have{/if}.
    </p>
  </div>
{/if}

<div class="poster-grid">
  {#each shown as s (s.id)}
    {#if movies}
      {@const [label, tone] = movieState(s)}
      <a href={`/movies/${s.id}`} onclick={link}>
        <Poster url={s.posterUrl} title={s.title} />
        <div class="title">{s.title}</div>
        <div class="small muted">{s.year || ''}{s.quality ? ` · ${s.quality}` : ''}</div>
        <span class="badge {tone}">{label}</span>
      </a>
    {:else}
      {@const [label, tone] = summary(s)}
      <a href={`/library/${s.id}`} onclick={link}>
        <Poster url={s.posterUrl} title={s.title} />
        <div class="title">{s.title}</div>
        <div class="small muted">{s.year || ''} · {s.imported}/{s.episodes} episodes</div>
        <span class="badge {tone}">{label}</span>
      </a>
    {/if}
  {/each}
</div>

<style>
  .filter {
    width: 180px;
  }
  .poster-grid .badge {
    margin-top: 4px;
  }
</style>
