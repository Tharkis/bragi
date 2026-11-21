<script>
  import { get, post } from '../lib/api.js'
  import { navigate } from '../lib/router.svelte.js'
  import { requestStates, bookTypes } from '../lib/status.js'
  import Poster from './Poster.svelte'
  import Modal from './Modal.svelte'
  import Badge from './Badge.svelte'

  // Search Open Library and request an ebook or an audiobook.
  let { me, type } = $props()

  let query = $state('')
  let results = $state(null)
  let loading = $state(false)
  let error = $state('')
  let notice = $state('')
  let picking = $state(null)
  let busy = $state(false)
  let requestError = $state('')
  let timer

  const noun = $derived(type === 'audiobook' ? 'audiobook' : 'book')

  function onInput() {
    clearTimeout(timer)
    timer = setTimeout(search, 400)
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
      results = await get(`/discover/search?type=${type}&q=` + encodeURIComponent(q))
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  async function openRequest(b) {
    picking = { ...b }
    requestError = ''
    // Fetch the description for the dialog; the search has none.
    try {
      const full = await get(`/discover/book/${b.bookId}?type=${type}`)
      if (picking && picking.bookId === b.bookId) picking = { ...picking, overview: full.overview, coverUrl: full.coverUrl || b.coverUrl }
    } catch {}
  }

  async function sendRequest(event) {
    event.preventDefault()
    busy = true
    requestError = ''
    try {
      const r = await post('/requests', { type, bookId: picking.bookId })
      const hit = results?.find((x) => x.bookId === picking.bookId)
      if (hit) {
        hit.requested = r.status
        if (r.progress) hit.itemId = r.progress.seriesId
      }
      notice =
        r.status === 'pending'
          ? r.reason
            ? `${picking.title} is requested, but could not be added yet: ${r.reason}`
            : `${picking.title} is requested. An admin will approve it.`
          : `${picking.title} is added. Bragi is looking for the ${noun} now.`
      picking = null
    } catch (e) {
      requestError = e.message
    } finally {
      busy = false
    }
  }
</script>

<form class="search" onsubmit={search}>
  <input
    type="search"
    placeholder={`Search for a ${noun} by title, author or ISBN…`}
    bind:value={query}
    oninput={onInput}
    aria-label={`Search for a ${noun}`}
  />
</form>

{#if notice}<div class="notice good">{notice}</div>{/if}
{#if error}<div class="notice bad">{error}</div>{/if}

{#if loading && !results}
  <p class="muted">Searching Open Library…</p>
{:else if results && results.length === 0}
  <div class="card empty muted">No books match “{query}”.</div>
{:else if results}
  <div class="results">
    {#each results as b (b.bookId)}
      <div class="card result">
        <Poster url={b.coverUrl} title={b.title} />
        <div class="body">
          <div class="title">{b.title}</div>
          <div class="muted small">{b.author || 'Unknown author'}{b.year ? ` · ${b.year}` : ''}</div>
          <div class="row">
            {#if b.itemId}
              <button onclick={() => navigate(`/books/${b.itemId}`)}>In your library</button>
            {:else if b.requested}
              <Badge label={requestStates[b.requested]?.[0] ?? b.requested} tone={requestStates[b.requested]?.[1]} />
            {:else}
              <button class="primary" onclick={() => openRequest(b)}>Request {bookTypes[type].toLowerCase()}</button>
            {/if}
          </div>
        </div>
      </div>
    {/each}
  </div>
{:else}
  <div class="card empty muted">
    <p>Type a {noun}'s title or author above.</p>
    <p class="small">
      Book details come from Open Library.
      {me.role === 'admin' ? 'Your requests are added straight away.' : 'An admin approves requests before Bragi downloads anything.'}
    </p>
  </div>
{/if}

{#if picking}
  <Modal title={`Request ${picking.title}`} onClose={() => (picking = null)}>
    <form onsubmit={sendRequest}>
      <div class="pick">
        <Poster url={picking.coverUrl} title={picking.title} />
        <div>
          <div class="title">{picking.title}</div>
          <div class="muted small">{picking.author}{picking.year ? ` · ${picking.year}` : ''}</div>
          <p class="small muted clamp desc">{picking.overview || ''}</p>
        </div>
      </div>
      <p class="small">
        {type === 'audiobook'
          ? 'Bragi looks for an M4B first, then MP3, on your indexers and on Soulseek.'
          : 'Bragi looks for an EPUB first, then AZW3, MOBI or PDF, on your indexers and on Soulseek.'}
      </p>
      {#if requestError}<div class="notice bad">{requestError}</div>{/if}
      <div class="row">
        <button class="primary" disabled={busy}>{busy ? 'Requesting…' : `Request ${bookTypes[type].toLowerCase()}`}</button>
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
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 14px;
  }
  .result {
    display: grid;
    grid-template-columns: 84px 1fr;
    gap: 14px;
    padding: 12px;
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .body .row {
    margin-top: auto;
    padding-top: 10px;
  }
  .title {
    font-weight: 600;
  }
  .pick {
    display: grid;
    grid-template-columns: 90px 1fr;
    gap: 14px;
    margin-bottom: 12px;
  }
  .desc {
    margin-top: 8px;
  }
</style>
