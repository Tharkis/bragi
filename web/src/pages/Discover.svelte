<script>
  import { get, post } from '../lib/api.js'
  import { navigate } from '../lib/router.svelte.js'
  import Poster from '../components/Poster.svelte'
  import Modal from '../components/Modal.svelte'
  import Badge from '../components/Badge.svelte'
  import { requestStates, isBook } from '../lib/status.js'
  import BookDiscover from '../components/BookDiscover.svelte'
  import MusicDiscover from '../components/MusicDiscover.svelte'

  let { me } = $props()

  const kinds = { tv: 'TV shows', movie: 'Movies', book: 'Books', audiobook: 'Audiobooks', music: 'Music' }
  let kind = $state(readKind())
  let query = $state('')
  let results = $state(null)
  let loading = $state(false)
  let error = $state('')
  let picking = $state(null) // the show being requested
  let notice = $state('')
  let timer

  function readKind() {
    try {
      const k = localStorage.getItem('bragi.discover')
      return k in kinds ? k : 'tv'
    } catch {
      return 'tv'
    }
  }

  function setKind(k) {
    kind = k
    notice = ''
    try {
      localStorage.setItem('bragi.discover', k)
    } catch {}
    results = null
    search()
  }

  function onInput() {
    clearTimeout(timer)
    timer = setTimeout(search, 350)
  }

  async function search(event) {
    if (isBook(kind) || kind === 'music') return // BookDiscover and MusicDiscover search on their own
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
      const k = kind
      const r = await get(`/discover/search?type=${k}&q=` + encodeURIComponent(q))
      if (k === kind) results = r
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  // ---- request dialog ----
  let scope = $state('all')
  let seasons = $state([])
  let chosen = $state([])
  let busy = $state(false)
  let requestError = $state('')

  async function requestMovie(movie) {
    busy = true
    error = ''
    try {
      const r = await post('/requests', { type: 'movie', tmdbId: movie.tmdbId })
      movie.requested = r.status
      if (r.progress) movie.movieId = r.progress.movieId
      notice =
        r.status === 'pending'
          ? r.reason
            ? `${movie.title} is requested, but could not be added yet: ${r.reason}`
            : `${movie.title} is requested. An admin will approve it.`
          : `${movie.title} is added. Bragi is looking for it now.`
    } catch (e) {
      error = e.message
    } finally {
      busy = false
    }
  }

  const today = new Date().toISOString().slice(0, 10)
  function movieNote(m) {
    if (m.inCinemas && m.inCinemas > today) return 'Coming to cinemas ' + m.inCinemas
    return ''
  }

  async function openRequest(show) {
    picking = show
    scope = 'all'
    chosen = []
    seasons = []
    requestError = ''
    try {
      seasons = (await get(`/discover/tv/${show.tmdbId}`)).seasons ?? []
    } catch (e) {
      requestError = e.message
    }
  }

  async function sendRequest(event) {
    event.preventDefault()
    busy = true
    requestError = ''
    try {
      const body = { type: 'tv', tmdbId: picking.tmdbId, seasons: scope === 'pick' ? chosen : scope }
      const r = await post('/requests', body)
      picking.requested = r.status
      if (r.progress) picking.seriesId = r.progress.seriesId
      notice =
        r.status === 'pending'
          ? r.reason
            ? `${picking.title} is requested, but could not be added yet: ${r.reason}`
            : `${picking.title} is requested. An admin will approve it.`
          : `${picking.title} is added. Bragi is searching for it now.`
      picking = null
    } catch (e) {
      requestError = e.message
    } finally {
      busy = false
    }
  }

  const choices = [
    ['all', 'All seasons', 'Every episode that has aired, and new ones as they air.'],
    ['latest', 'Latest season', 'The newest season, and new episodes as they air.'],
    ['future', 'Future episodes only', 'Nothing old. New episodes from now on.'],
    ['pick', 'Pick seasons', 'Choose exactly which seasons you want.'],
  ]
</script>

<div class="page-head">
  <div>
    <h1>Discover</h1>
    <p class="muted">Find a show, a movie, a book or some music and request it. Bragi does the rest.</p>
  </div>
</div>

<div class="tabs">
  {#each Object.entries(kinds) as [k, label] (k)}
    <button class:active={kind === k} onclick={() => setKind(k)}>{label}</button>
  {/each}
</div>

{#if isBook(kind)}
  {#key kind}<BookDiscover {me} type={kind} />{/key}
{:else if kind === 'music'}
  <MusicDiscover {me} />
{:else}
<form class="search" onsubmit={search}>
  <input
    type="search"
    placeholder={kind === 'movie' ? 'Search for a movie…' : 'Search for a TV show…'}
    bind:value={query}
    oninput={onInput}
    aria-label={kind === 'movie' ? 'Search for a movie' : 'Search for a TV show'}
  />
</form>

{#if notice}<div class="notice good">{notice}</div>{/if}
{#if error}<div class="notice bad">{error}</div>{/if}

{#if loading && !results}
  <p class="muted">Searching…</p>
{:else if results && results.length === 0}
  <div class="card empty muted">No {kind === 'movie' ? 'movies' : 'shows'} match “{query}”.</div>
{:else if results && kind === 'movie'}
  <div class="results">
    {#each results as movie (movie.tmdbId)}
      <div class="card result">
        <Poster url={movie.posterUrl} title={movie.title} />
        <div class="body">
          <div class="title">{movie.title} {#if movie.year}<span class="muted">({movie.year})</span>{/if}</div>
          <p class="muted small clamp">{movie.overview || 'No description yet.'}</p>
          {#if movieNote(movie)}<p class="small muted note">{movieNote(movie)}</p>{/if}
          <div class="row">
            {#if movie.movieId}
              <button onclick={() => navigate(`/movies/${movie.movieId}`)}>In your library</button>
            {:else if movie.requested}
              <Badge label={requestStates[movie.requested]?.[0] ?? movie.requested} tone={requestStates[movie.requested]?.[1]} />
            {:else}
              <button class="primary" disabled={busy} onclick={() => requestMovie(movie)}>Request</button>
            {/if}
          </div>
        </div>
      </div>
    {/each}
  </div>
{:else if results}
  <div class="results">
    {#each results as show (show.tmdbId)}
      <div class="card result">
        <Poster url={show.posterUrl} title={show.title} />
        <div class="body">
          <div class="title">{show.title} {#if show.year}<span class="muted">({show.year})</span>{/if}</div>
          <p class="muted small clamp">{show.overview || 'No description yet.'}</p>
          <div class="row">
            {#if show.seriesId}
              <button onclick={() => navigate(`/library/${show.seriesId}`)}>In your library</button>
              <button class="link small" onclick={() => openRequest(show)}>Request more</button>
            {:else if show.requested}
              <Badge label={requestStates[show.requested]?.[0] ?? show.requested} tone={requestStates[show.requested]?.[1]} />
            {:else}
              <button class="primary" onclick={() => openRequest(show)}>Request</button>
            {/if}
          </div>
        </div>
      </div>
    {/each}
  </div>
{:else}
  <div class="card empty muted">
    <p>Type a {kind === 'movie' ? "movie's" : "show's"} name above.</p>
    <p class="small">
      {me.role === 'admin' ? 'Your requests are added straight away.' : 'An admin approves requests before Bragi downloads anything.'}
    </p>
  </div>
{/if}

{/if}

{#if picking}
  <Modal title={`Request ${picking.title}`} onClose={() => (picking = null)}>
    <form onsubmit={sendRequest}>
      {#each choices as [id, label, help] (id)}
        <label class="choice">
          <input type="radio" name="scope" value={id} bind:group={scope} />
          <span><strong>{label}</strong><span class="muted small">{help}</span></span>
        </label>
      {/each}
      {#if scope === 'pick'}
        <div class="row seasons">
          {#each seasons as n (n)}
            <label class="check"><input type="checkbox" value={n} bind:group={chosen} /> <span>Season {n}</span></label>
          {:else}
            <span class="muted small">Loading seasons…</span>
          {/each}
        </div>
      {/if}
      {#if requestError}<div class="notice bad">{requestError}</div>{/if}
      <div class="row">
        <button class="primary" disabled={busy || (scope === 'pick' && chosen.length === 0)}>
          {busy ? 'Requesting…' : 'Request'}
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
  .results {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
    gap: 14px;
  }
  .result {
    display: grid;
    grid-template-columns: 96px 1fr;
    gap: 14px;
    padding: 12px;
  }
  .body {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .body .row {
    margin-top: auto;
  }
  .title {
    font-weight: 600;
    margin-bottom: 4px;
  }
  .note {
    margin: 0 0 8px;
  }
  .seasons {
    margin: 4px 0 14px;
    gap: 14px;
  }
  .seasons label {
    margin: 0;
  }
</style>
