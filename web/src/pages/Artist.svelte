<script>
  import { onMount } from 'svelte'
  import { get, post, put, del } from '../lib/api.js'
  import { link, navigate } from '../lib/router.svelte.js'
  import { onEvents } from '../lib/events.js'
  import { albumState, albumTypes } from '../lib/status.js'
  import Poster from '../components/Poster.svelte'
  import Badge from '../components/Badge.svelte'

  let { id, me } = $props()
  const admin = me.role === 'admin'

  let artist = $state(null)
  let error = $state('')
  let notice = $state('')
  let busy = $state('')

  async function load() {
    try {
      artist = await get(`/artists/${id}`)
      error = ''
    } catch (e) {
      error = e.message
    }
  }

  onMount(() => {
    load()
    return onEvents(['artist', 'album', 'download'], (d) => {
      if (!d || d.id === Number(id) || d.artistId === Number(id) || d.albumId) load()
    })
  })

  function flash(msg) {
    notice = msg
    setTimeout(() => (notice = ''), 4000)
  }

  async function act(what, fn) {
    busy = what
    error = ''
    try {
      await fn()
    } catch (e) {
      error = e.message
    } finally {
      busy = ''
    }
  }

  const searchMissing = () =>
    act('search', async () => {
      const r = await post(`/artists/${id}/search`)
      flash(r.queued ? `Searching for ${r.queued} ${r.queued === 1 ? 'album' : 'albums'}.` : 'Nothing is missing.')
      await load()
    })
  const refresh = () =>
    act('refresh', async () => {
      await post(`/artists/${id}/refresh`)
      flash('Album list updated from MusicBrainz.')
      await load()
    })
  const setNew = (on) => act('new', async () => (artist = await put(`/artists/${id}/monitor`, { monitorNew: on })))
  const setAll = (albums) => act('all', async () => (artist = await put(`/artists/${id}/monitor`, { albums })))

  async function setAlbum(al, on) {
    try {
      await put(`/albums/${al.id}/monitor`, { monitored: on })
      al.monitored = on
    } catch (e) {
      error = e.message
    }
  }

  async function remove() {
    if (!window.confirm(`Stop following ${artist.name}? Files on disk are kept.`)) return
    await del(`/artists/${id}`)
    navigate('/library/music')
  }

  let showAll = $state(false)
  // Albums by type, newest first. Live albums, compilations and others are
  // folded away unless something of them is wanted or on disk.
  const groups = $derived.by(() => {
    if (!artist) return []
    const list = artist.albumList
    return albumTypes
      .map((t) => [t, list.filter((a) => (albumTypes.includes(a.albumType) ? a.albumType : 'Other') === t).reverse()])
      .filter(([, l]) => l.length)
  })
  const quiet = (t, l) => ['Live', 'Compilation', 'Other'].includes(t) && !l.some((a) => a.monitored || a.importedTracks)
  const plural = (t) => (t === 'EP' ? 'EPs' : t + 's')
</script>

{#if error}<div class="notice bad">{error}</div>{/if}

{#if artist}
  <a class="small" href="/library/music" onclick={link}>← Music</a>
  <div class="hero">
    <div class="photo">
      {#if artist.pictureUrl}<img src={artist.pictureUrl} alt="" />{:else}<span>{artist.name[0]}</span>{/if}
    </div>
    <div class="info">
      <h1>{artist.name}</h1>
      <div class="byline muted">
        {artist.albums} {artist.albums === 1 ? 'release' : 'releases'} · {artist.importedAlbums} in library{artist.wantedAlbums ? ` · ${artist.wantedAlbums} wanted` : ''}
      </div>
      {#if artist.overview}<p class="muted overview">{artist.overview}</p>{/if}
      {#if admin}
        {#if artist.path}<div class="small muted path"><code>{artist.path}</code></div>{/if}
        <div class="row">
          <button class="primary" onclick={searchMissing} disabled={!!busy}>{busy === 'search' ? 'Searching…' : 'Search missing'}</button>
          <button onclick={refresh} disabled={!!busy}>{busy === 'refresh' ? 'Refreshing…' : 'Refresh'}</button>
          <label class="check mon">
            <input type="checkbox" checked={artist.monitorNew} onchange={(e) => setNew(e.currentTarget.checked)} />
            <span class="small">Get new releases</span>
          </label>
          <button class="danger" onclick={remove}>Remove</button>
        </div>
        <div class="row small bulk">
          <span class="muted">Monitor:</span>
          <button class="link small" onclick={() => setAll('all')} disabled={!!busy}>every album, EP and single</button>
          <span class="muted">·</span>
          <button class="link small" onclick={() => setAll('none')} disabled={!!busy}>nothing</button>
        </div>
      {/if}
      {#if notice}<div class="notice good" style="margin-top: 12px">{notice}</div>{/if}
    </div>
  </div>

  {#each groups as [t, list] (t)}
    {#if showAll || !quiet(t, list)}
      <h2>{plural(t)} <span class="muted small">{list.length}</span></h2>
      <div class="card albums">
        {#each list as al (al.id)}
          {@const [label, tone] = albumState(al)}
          <div class="album" class:off={!al.monitored && !al.importedTracks}>
            <a class="cover" href={`/albums/${al.id}`} onclick={link}><Poster url={al.coverUrl} title={al.title} size="sm" square /></a>
            <div class="what">
              <a href={`/albums/${al.id}`} onclick={link} class="title">{al.title}</a>
              <div class="small muted">
                {al.year || 'No date'}{al.tracks ? ` · ${al.tracks} tracks` : ''}{al.quality ? ` · ${al.quality.replace('-', ' ')}` : ''}
              </div>
            </div>
            <Badge {label} {tone} />
            {#if admin}
              <label class="check" title="Search for it automatically">
                <input type="checkbox" checked={al.monitored} onchange={(e) => setAlbum(al, e.currentTarget.checked)} />
                <span class="small sr">Monitored</span>
              </label>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  {/each}
  {#if !showAll && groups.some(([t, l]) => quiet(t, l))}
    <button class="link small" style="margin-top: 14px" onclick={() => (showAll = true)}>Show live albums, compilations and others</button>
  {/if}
{/if}

<style>
  .hero {
    display: grid;
    grid-template-columns: 180px 1fr;
    gap: 24px;
    margin: 12px 0 24px;
  }
  .photo {
    width: 180px;
    height: 180px;
    border-radius: 50%;
    overflow: hidden;
    background: var(--surface-2);
    display: grid;
    place-items: center;
    color: var(--muted);
    font-size: 3rem;
    font-weight: 700;
  }
  .photo img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .info {
    min-width: 0;
  }
  .hero h1 {
    margin-bottom: 4px;
  }
  .byline {
    margin-bottom: 12px;
  }
  .overview {
    max-width: 640px;
  }
  .path {
    margin-bottom: 14px;
  }
  .path code {
    word-break: break-all;
  }
  .mon {
    margin: 0 6px;
  }
  .mon input {
    width: auto;
  }
  .bulk {
    margin-top: 8px;
    gap: 6px;
  }
  h2 {
    font-size: 1.05rem;
    margin: 22px 0 8px;
  }
  .albums {
    padding: 4px 14px;
  }
  .album {
    display: grid;
    grid-template-columns: 44px minmax(0, 1fr) auto auto;
    gap: 12px;
    align-items: center;
    padding: 8px 0;
    border-bottom: 1px solid var(--border);
  }
  .album:last-child {
    border-bottom: none;
  }
  .album.off .cover,
  .album.off .what {
    opacity: 0.55;
  }
  .title {
    font-weight: 600;
    color: var(--text);
    text-decoration: none;
  }
  .title:hover {
    text-decoration: underline;
  }
  .album .check {
    margin: 0;
  }
  .album .check input {
    width: auto;
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
  }
  @media (max-width: 720px) {
    .hero {
      grid-template-columns: 100px minmax(0, 1fr);
      gap: 14px;
    }
    .photo {
      width: 100px;
      height: 100px;
    }
  }
</style>
