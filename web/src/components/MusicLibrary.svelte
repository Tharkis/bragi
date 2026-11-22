<script>
  import { onMount } from 'svelte'
  import { get } from '../lib/api.js'
  import { link } from '../lib/router.svelte.js'
  import { onEvents } from '../lib/events.js'
  import Poster from './Poster.svelte'
  import MediaTabs from './MediaTabs.svelte'
  import ScanPanel from './ScanPanel.svelte'

  // The Music tab of the library: followed artists, A to Z.
  let { me } = $props()

  let list = $state([])
  let loaded = $state(false)
  let error = $state('')
  let filter = $state('')
  let panel = $state(null)
  let scanning = $state(false)
  const admin = me.role === 'admin'

  async function load() {
    try {
      list = await get('/artists')
      error = ''
    } catch (e) {
      error = e.message
    } finally {
      loaded = true
    }
  }

  onMount(() => {
    load()
    return onEvents(['artist', 'album', 'download'], load)
  })

  const shown = $derived(filter ? list.filter((a) => a.name.toLowerCase().includes(filter.toLowerCase())) : list)
  const albums = $derived(list.reduce((n, a) => n + a.importedAlbums, 0))

  function note(a) {
    if (a.downloading) return [`Downloading ${a.downloading}`, 'accent']
    if (a.wantedAlbums) return [`${a.wantedAlbums} wanted`, 'warn']
    if (a.importedAlbums) return [`${a.importedAlbums} ${a.importedAlbums === 1 ? 'album' : 'albums'}`, 'good']
    return [a.monitorNew ? 'Waiting for new releases' : 'Nothing monitored', '']
  }
</script>

<div class="page-head">
  <div>
    <h1>Library</h1>
    <p class="muted">
      {list.length} {list.length === 1 ? 'artist' : 'artists'}{list.length ? `, ${albums} ${albums === 1 ? 'album' : 'albums'} on disk` : ''}
    </p>
  </div>
  <div class="row">
    {#if list.length > 8}<input class="filter" placeholder="Filter by artist…" bind:value={filter} aria-label="Filter" />{/if}
    {#if admin}
      <button onclick={() => panel?.start()} disabled={scanning}>{scanning ? 'Scanning…' : 'Scan music folder'}</button>
    {/if}
  </div>
</div>

<MediaTabs base="/library" type="music" />

{#if error}<div class="notice bad">{error}</div>{/if}

{#if admin}<ScanPanel type="music" bind:this={panel} bind:busy={scanning} onChange={load} />{/if}

{#if loaded && list.length === 0}
  <div class="card empty muted">
    <p>No artists yet.</p>
    <p class="small">
      <a href="/discover" onclick={(e) => { try { localStorage.setItem('bragi.discover', 'music') } catch {} link(e) }}>Follow an artist</a>{#if admin}, or press Scan music folder to add the music you already have{/if}.
    </p>
  </div>
{/if}

<div class="poster-grid">
  {#each shown as a (a.id)}
    {@const [label, tone] = note(a)}
    <a href={`/artists/${a.id}`} onclick={link}>
      <Poster url={a.pictureUrl} title={a.name} square />
      <div class="title">{a.name}</div>
      <span class="badge {tone}">{label}</span>
    </a>
  {/each}
</div>

<style>
  .filter {
    width: 220px;
  }
  .poster-grid .badge {
    margin-top: 4px;
  }
</style>
