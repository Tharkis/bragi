<script>
  import { onMount } from 'svelte'
  import { get, post } from '../lib/api.js'
  import { navigate } from '../lib/router.svelte.js'
  import { requestStates, albumTypes } from '../lib/status.js'
  import Poster from './Poster.svelte'
  import Modal from './Modal.svelte'
  import Badge from './Badge.svelte'

  // Search MusicBrainz for artists and albums, and request either: follow
  // an artist, or ask for one album.
  let { me } = $props()

  let query = $state('')
  let results = $state(null)
  let trending = $state(null)
  let loading = $state(false)
  let error = $state('')
  let notice = $state('')
  let timer

  onMount(async () => {
    try {
      trending = await get('/discover/trending?type=music')
    } catch {
      trending = []
    }
  })

  function onInput() {
    clearTimeout(timer)
    timer = setTimeout(search, 450)
  }

  async function search(event) {
    event?.preventDefault()
    clearTimeout(timer)
    const q = query.trim()
    if (!q) {
      results = null
      return
    }
    loading = true
    error = ''
    try {
      const r = await get('/discover/search?type=music&q=' + encodeURIComponent(q))
      if (q === query.trim()) results = r
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  function done(name, r, what) {
    notice =
      r.status === 'pending'
        ? r.reason
          ? `${name} is requested, but could not be added yet: ${r.reason}`
          : `${name} is requested. An admin will approve it.`
        : `${name} is added. ${what}`
  }

  // ---- album request ----
  let busyAlbum = $state('')

  async function requestAlbum(al) {
    busyAlbum = al.albumId
    error = ''
    try {
      const r = await post('/requests', { type: 'music', albumId: al.albumId })
      al.requested = r.status
      if (r.progress?.albumId) al.itemId = r.progress.albumId
      done(al.title, r, 'Bragi is looking for it now.')
    } catch (e) {
      error = e.message
    } finally {
      busyAlbum = ''
    }
  }

  // ---- artist request ----
  let picking = $state(null) // the artist being followed
  let detail = $state(null)
  let monitor = $state('all')
  let chosen = $state([])
  let busy = $state(false)
  let requestError = $state('')
  let showAllTypes = $state(false)

  async function openArtist(a) {
    picking = a
    detail = null
    monitor = 'all'
    chosen = []
    showAllTypes = false
    requestError = ''
    try {
      detail = await get(`/discover/artist/${a.artistId}`)
    } catch (e) {
      requestError = e.message
    }
  }

  // The artist's albums by type, newest first, limited to the types Bragi
  // monitors unless asked for all.
  const groups = $derived.by(() => {
    if (!detail) return []
    const types = showAllTypes ? albumTypes : detail.monitorTypes
    return albumTypes
      .filter((t) => types.includes(t))
      .map((t) => [t, detail.albums.filter((a) => (albumTypes.includes(a.albumType) ? a.albumType : 'Other') === t).reverse()])
      .filter(([, list]) => list.length)
  })
  const hidden = $derived(detail ? detail.albums.filter((a) => !detail.monitorTypes.includes(a.albumType)).length : 0)
  const typeWords = $derived(detail ? detail.monitorTypes.map((t) => (t === 'EP' ? 'EPs' : t.toLowerCase() + 's')).join(', ') : '')

  async function sendArtist(event) {
    event.preventDefault()
    busy = true
    requestError = ''
    try {
      const body = { type: 'music', artistId: picking.artistId, monitor: monitor === 'pick' ? 'none' : monitor }
      if (monitor === 'pick') body.albums = chosen
      const r = await post('/requests', body)
      picking.requested = r.status
      if (r.progress?.artistId) picking.itemId = r.progress.artistId
      done(picking.name, r, monitor === 'future' ? 'Bragi grabs new releases as they come out.' : 'Bragi is searching for the albums now.')
      picking = null
    } catch (e) {
      requestError = e.message
    } finally {
      busy = false
    }
  }

  const choices = [
    ['all', 'All albums', 'Everything released so far, and new releases as they come out.'],
    ['future', 'New releases only', 'Nothing old. Albums that come out from now on.'],
    ['pick', 'Pick albums', 'Choose exactly which albums you want.'],
  ]

  const albumNote = (a) => [a.artist, a.year, a.albumType !== 'Album' ? a.albumType : ''].filter(Boolean).join(' · ')
</script>

{#snippet albumCard(al)}
  <div class="album">
    {#if al.itemId}
      <button class="cover" onclick={() => navigate(`/albums/${al.itemId}`)} aria-label={`Open ${al.title}`}>
        <Poster url={al.coverUrl} title={al.title} square />
      </button>
    {:else}
      <Poster url={al.coverUrl} title={al.title} square />
    {/if}
    <div class="title">{al.title}</div>
    <div class="small muted">{albumNote(al)}</div>
    <div class="act">
      {#if al.itemId && (al.status === 'imported' || al.status === 'upgrade_wanted')}
        <Badge label="In your library" tone="good" />
      {:else if al.requested}
        <Badge label={requestStates[al.requested]?.[0] ?? al.requested} tone={requestStates[al.requested]?.[1]} />
      {:else if al.itemId && al.monitored}
        <Badge label="Wanted" tone="warn" />
      {:else}
        <button class="small-btn" disabled={busyAlbum === al.albumId} onclick={() => requestAlbum(al)}>
          {busyAlbum === al.albumId ? 'Requesting…' : 'Request'}
        </button>
      {/if}
    </div>
  </div>
{/snippet}

<form class="search" onsubmit={search}>
  <input
    type="search"
    placeholder="Search for an artist or an album…"
    bind:value={query}
    oninput={onInput}
    aria-label="Search for an artist or an album"
  />
</form>

{#if notice}<div class="notice good">{notice}</div>{/if}
{#if error}<div class="notice bad">{error}</div>{/if}

{#if loading && !results}
  <p class="muted">Searching MusicBrainz…</p>
{:else if results && results.artists.length === 0 && results.albums.length === 0}
  <div class="card empty muted">Nothing matches “{query}”. Try “Artist - Album” for one album.</div>
{:else if results}
  {#if results.artists.length}
    <h2>Artists</h2>
    <div class="artists">
      {#each results.artists as a (a.artistId)}
        <div class="card artist">
          <div class="photo">
            {#if a.imageUrl}<img src={a.imageUrl} alt="" loading="lazy" />{:else}<span>{a.name[0]}</span>{/if}
          </div>
          <div class="body">
            <div class="title">{a.name}</div>
            <div class="small muted">{[a.disambiguation, a.country, a.genres?.slice(0, 2).join(', ')].filter(Boolean).join(' · ') || a.type || ' '}</div>
            <div class="row">
              {#if a.itemId}
                <button onclick={() => navigate(`/artists/${a.itemId}`)}>In your library</button>
                <button class="link small" onclick={() => openArtist(a)}>Request more</button>
              {:else if a.requested}
                <Badge label={requestStates[a.requested]?.[0] ?? a.requested} tone={requestStates[a.requested]?.[1]} />
              {:else}
                <button class="primary" onclick={() => openArtist(a)}>Follow</button>
              {/if}
            </div>
          </div>
        </div>
      {/each}
    </div>
  {/if}
  {#if results.albums.length}
    <h2>Albums</h2>
    <div class="albums">
      {#each results.albums as al (al.albumId)}{@render albumCard(al)}{/each}
    </div>
  {/if}
{:else}
  {#if trending?.length}
    <h2>Popular this week</h2>
    <div class="albums">
      {#each trending as al (al.albumId)}{@render albumCard(al)}{/each}
    </div>
  {:else}
    <div class="card empty muted">
      <p>Type an artist or an album above.</p>
      <p class="small">
        Music details come from MusicBrainz.
        {me.role === 'admin' ? 'Your requests are added straight away.' : 'An admin approves requests before Bragi downloads anything.'}
      </p>
    </div>
  {/if}
{/if}

{#if picking}
  <Modal title={`Follow ${picking.name}`} wide onClose={() => (picking = null)}>
    <form onsubmit={sendArtist}>
      {#each choices as [id, label, help] (id)}
        <label class="choice">
          <input type="radio" name="monitor" value={id} bind:group={monitor} />
          <span><strong>{label}</strong><span class="muted small">{help}</span></span>
        </label>
      {/each}
      {#if detail && monitor !== 'pick'}
        <p class="small muted">Bragi follows {typeWords}. Change that under Settings, Music.</p>
      {/if}
      {#if monitor === 'pick'}
        {#if !detail && !requestError}
          <p class="muted small">Loading albums… MusicBrainz is slow for artists with many releases.</p>
        {:else if detail}
          <div class="pick">
            {#each groups as [t, list] (t)}
              <div class="group">
                <div class="small strong">{t === 'EP' ? 'EPs' : t + 's'}</div>
                {#each list as al (al.albumId)}
                  <label class="check">
                    <input type="checkbox" value={al.albumId} bind:group={chosen} disabled={al.itemId && al.monitored} />
                    <span>{al.title} <span class="muted small">{al.year || ''}</span></span>
                  </label>
                {/each}
              </div>
            {/each}
          </div>
          {#if hidden && !showAllTypes}
            <button type="button" class="link small" onclick={() => (showAllTypes = true)}>Show {hidden} live albums, compilations and others</button>
          {/if}
        {/if}
      {/if}
      {#if requestError}<div class="notice bad">{requestError}</div>{/if}
      <div class="row">
        <button class="primary" disabled={busy || (monitor === 'pick' && chosen.length === 0)}>
          {busy ? 'Requesting…' : monitor === 'pick' ? `Request ${chosen.length || ''} ${chosen.length === 1 ? 'album' : 'albums'}` : 'Follow'}
        </button>
        <button type="button" class="link" onclick={() => (picking = null)}>Cancel</button>
      </div>
    </form>
  </Modal>
{/if}

<style>
  .search {
    margin-bottom: 20px;
  }
  .search input {
    font-size: 1.1rem;
    padding: 12px 14px;
  }
  h2 {
    font-size: 1.05rem;
    margin: 18px 0 10px;
  }
  .artists {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 12px;
  }
  .artist {
    display: grid;
    grid-template-columns: 72px 1fr;
    gap: 14px;
    padding: 12px;
    align-items: center;
  }
  .photo {
    width: 72px;
    height: 72px;
    border-radius: 50%;
    overflow: hidden;
    background: var(--surface-2);
    display: grid;
    place-items: center;
    color: var(--muted);
    font-size: 1.6rem;
    font-weight: 700;
  }
  .photo img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .body .row {
    padding-top: 8px;
  }
  .title {
    font-weight: 600;
    line-height: 1.25;
  }
  .albums {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
    gap: 18px 14px;
  }
  .album {
    display: flex;
    flex-direction: column;
    gap: 3px;
    min-width: 0;
  }
  .album .title {
    margin-top: 6px;
  }
  .cover {
    padding: 0;
    border: none;
    background: none;
    cursor: pointer;
  }
  .act {
    margin-top: 6px;
  }
  .small-btn {
    padding: 4px 12px;
    font-size: 0.85rem;
  }
  .pick {
    max-height: 320px;
    overflow-y: auto;
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 8px 12px;
    margin: 4px 0 10px;
  }
  .group + .group {
    margin-top: 10px;
  }
  .strong {
    font-weight: 600;
    margin-bottom: 2px;
  }
  .pick .check {
    display: flex;
    margin: 2px 0;
  }
</style>
