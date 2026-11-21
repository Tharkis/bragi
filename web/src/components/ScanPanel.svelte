<script>
  import { onMount } from 'svelte'
  import { get, post, put } from '../lib/api.js'
  import { onEvents } from '../lib/events.js'
  import Modal from './Modal.svelte'
  import Poster from './Poster.svelte'

  // The scan button, its progress, and what it could not recognise, for one
  // media type's Library tab. Admins only.
  let { type, busy = $bindable(false), onChange = () => {} } = $props()

  const nouns = {
    tv: ['show', 'shows', 'folder'],
    movie: ['movie', 'movies', 'folder'],
    book: ['book', 'books', 'file'],
    audiobook: ['audiobook', 'audiobooks', 'item'],
  }
  const [one, many, thing] = nouns[type]
  const isBook = type === 'book' || type === 'audiobook'

  let scan = $state(null)
  let unmatched = $state([])
  let error = $state('')
  let showAll = $state(false)

  // Find match dialog
  let picking = $state(null)
  let query = $state('')
  let results = $state(null)
  let searching = $state(false)
  let matching = $state('')
  let matchError = $state('')

  async function load() {
    try {
      ;[scan, unmatched] = await Promise.all([get('/libraries/scan?type=' + type), get('/libraries/unmatched?type=' + type)])
    } catch (e) {
      error = e.message
    }
  }

  onMount(() => {
    load()
    return onEvents(['scan'], load)
  })

  export async function start() {
    error = ''
    try {
      scan = await post('/libraries/scan?type=' + type)
    } catch (e) {
      error = e.message
    }
  }
  $effect(() => {
    busy = !!(scan?.running || scan?.queued)
  })

  function open(u) {
    picking = u
    query = u.guess || u.path.split('/').pop()
    results = null
    matchError = ''
    search()
  }

  async function search(event) {
    event?.preventDefault()
    if (!query.trim()) return
    searching = true
    matchError = ''
    try {
      results = await get(`/discover/search?type=${type}&q=${encodeURIComponent(query)}`)
    } catch (e) {
      matchError = e.message
    } finally {
      searching = false
    }
  }

  async function choose(r) {
    const id = isBook ? r.bookId : String(r.tmdbId)
    matching = id
    matchError = ''
    try {
      await post(`/libraries/unmatched/${picking.id}/match`, { id })
      picking = null
      await load()
      onChange()
    } catch (e) {
      matchError = e.message
    } finally {
      matching = ''
    }
  }

  async function ignore(u) {
    try {
      await put(`/libraries/unmatched/${u.id}/ignore`, { ignored: true })
      await load()
    } catch (e) {
      error = e.message
    }
  }

  const shownUnmatched = $derived(showAll ? unmatched : unmatched.slice(0, 8))
  const unit = (n) => (n === 1 ? thing : thing === 'folder' ? 'folders' : thing === 'file' ? 'files' : 'items')
</script>

{#if error}<div class="notice bad">{error}</div>{/if}

{#if scan?.running || scan?.queued}
  <div class="card scan">
    {#if scan.queued && !scan.running}
      <div class="small">Waiting for another library's scan to finish…</div>
    {:else}
      <div class="small">Looking through {scan.library}{scan.total ? `: ${scan.done} of ${scan.total} ${unit(scan.total)}` : '…'}</div>
      <div class="bar"><div style="width: {scan.total ? (100 * scan.done) / scan.total : 0}%"></div></div>
    {/if}
  </div>
{:else if scan?.finished && (scan.added.length || scan.files)}
  <details class="card scan">
    <summary class="small">
      {scan.nightly ? 'Last night’s scan' : 'Last scan'} added {scan.added.length} {scan.added.length === 1 ? one : many} and linked
      {scan.files} {scan.files === 1 ? 'file' : 'files'}.
    </summary>
    {#if scan.added.length}<ul class="small muted">{#each scan.added as a (a)}<li>{a}</li>{/each}</ul>{/if}
  </details>
{/if}

{#if unmatched.length}
  <div class="card unmatched">
    <div class="spread">
      <div>
        <strong>{unmatched.length} {unit(unmatched.length)} not recognised</strong>
        <div class="small muted">Find the right {one} for each and Bragi adds it as it is on disk. Nothing is moved or downloaded.</div>
      </div>
    </div>
    <ul>
      {#each shownUnmatched as u (u.id)}
        <li>
          <div class="what">
            <div class="path">{u.path}</div>
            <div class="small muted">{u.reason}</div>
          </div>
          <div class="row">
            <button onclick={() => open(u)}>Find match</button>
            <button class="link" onclick={() => ignore(u)} title="Hide it and skip it in later scans">Ignore</button>
          </div>
        </li>
      {/each}
    </ul>
    {#if unmatched.length > 8}
      <button class="link small" onclick={() => (showAll = !showAll)}>{showAll ? 'Show fewer' : `Show all ${unmatched.length}`}</button>
    {/if}
  </div>
{/if}

{#if picking}
  <Modal title="Find match" onClose={() => (picking = null)} wide>
    <p class="small muted path">{picking.path}</p>
    <form class="row searchrow" onsubmit={search}>
      <input bind:value={query} placeholder={isBook ? 'Title and author' : 'Title'} aria-label="Search" />
      <button class="primary" disabled={searching}>{searching ? 'Searching…' : 'Search'}</button>
    </form>
    {#if matchError}<div class="notice bad">{matchError}</div>{/if}
    {#if results && results.length === 0}
      <p class="muted">Nothing found. Try fewer words, or just the title.</p>
    {/if}
    {#if results?.length}
      <div class="choices">
        {#each results.slice(0, 12) as r (isBook ? r.bookId : r.tmdbId)}
          <button class="choice" onclick={() => choose(r)} disabled={!!matching}>
            <Poster url={isBook ? r.coverUrl : r.posterUrl} title={r.title} />
            <span class="title">{r.title}</span>
            <span class="small muted">{[r.author, r.year].filter(Boolean).join(' · ')}</span>
            {#if matching === (isBook ? r.bookId : String(r.tmdbId))}<span class="small">Adding…</span>{/if}
          </button>
        {/each}
      </div>
    {/if}
  </Modal>
{/if}

<style>
  .scan {
    margin-bottom: 18px;
    padding: 12px 14px;
    display: grid;
    gap: 6px;
  }
  .scan ul {
    margin: 8px 0;
  }
  .unmatched {
    margin-bottom: 18px;
    padding: 12px 14px;
  }
  .unmatched ul {
    list-style: none;
    padding: 0;
    margin: 10px 0 4px;
    display: grid;
    gap: 2px;
  }
  .unmatched li {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 12px;
    padding: 6px 0;
    border-top: 1px solid var(--border);
  }
  .what {
    min-width: 0;
  }
  .path {
    overflow-wrap: anywhere;
  }
  .searchrow {
    margin: 8px 0 14px;
    flex-wrap: nowrap;
  }
  .searchrow input {
    flex: 1;
  }
  .choices {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
    gap: 14px;
  }
  .choice {
    display: grid;
    gap: 4px;
    text-align: left;
    background: none;
    border: 1px solid transparent;
    padding: 6px;
    border-radius: 8px;
    align-content: start;
    color: inherit;
    font: inherit;
    cursor: pointer;
  }
  .choice:hover:not(:disabled) {
    border-color: var(--accent);
  }
  .choice .title {
    font-weight: 600;
    line-height: 1.25;
  }
  @media (max-width: 560px) {
    .unmatched li {
      flex-direction: column;
      align-items: stretch;
    }
  }
</style>
