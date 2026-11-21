<script>
  import { onMount } from 'svelte'
  import { get, post, put, del } from '../lib/api.js'
  import { link, navigate } from '../lib/router.svelte.js'
  import { onEvents } from '../lib/events.js'
  import { bookState, bookTypes } from '../lib/status.js'
  import Poster from '../components/Poster.svelte'
  import Badge from '../components/Badge.svelte'
  import Modal from '../components/Modal.svelte'

  let { id, me } = $props()
  const admin = me.role === 'admin'

  let book = $state(null)
  let error = $state('')
  let notice = $state('')

  const back = $derived(book?.type === 'audiobook' ? '/library/audiobooks' : '/library/books')

  async function load() {
    try {
      book = await get(`/books/${id}`)
      error = ''
    } catch (e) {
      error = e.message
    }
  }

  onMount(() => {
    load()
    return onEvents(['book', 'download'], (d) => {
      if (!d || d.id === Number(id) || d.bookId === Number(id)) load()
    })
  })

  function flash(msg) {
    notice = msg
    setTimeout(() => (notice = ''), 4000)
  }

  async function searchNow() {
    error = ''
    try {
      await post(`/books/${id}/search`)
      flash('Searching now.')
      await load()
    } catch (e) {
      error = e.message
    }
  }

  async function setMonitored(on) {
    try {
      book = await put(`/books/${id}/monitor`, { monitored: on })
    } catch (e) {
      error = e.message
    }
  }

  async function remove() {
    if (!window.confirm(`Stop tracking ${book.title}? Files on disk are kept.`)) return
    await del(`/books/${id}`)
    navigate(back)
  }

  // ---- manual pick ----
  let picking = $state(false)
  let releases = $state(null)
  let pickError = $state('')
  let grabbing = $state('')
  let showRejected = $state(false)

  async function manual() {
    picking = true
    releases = null
    pickError = ''
    showRejected = false
    try {
      releases = await get(`/books/${id}/releases`)
    } catch (e) {
      pickError = e.message
    }
  }

  async function grab(r) {
    grabbing = r.guid || r.title
    pickError = ''
    try {
      await post(`/books/${id}/grab`, { release: r })
      picking = false
      flash(`Sent ${r.title} to the download client.`)
      await load()
    } catch (e) {
      pickError = e.message
    } finally {
      grabbing = ''
    }
  }

  const accepted = $derived(releases?.filter((r) => !r.rejected) ?? [])
  const rejected = $derived(releases?.filter((r) => r.rejected) ?? [])

  function source(r) {
    if (r.protocol === 'soulseek') return r.seeders >= 1000 ? 'Free slot now' : 'Queued at the peer'
    if (r.protocol === 'torrent') return `${r.seeders} seeders`
    return 'Usenet'
  }

  const human = (n) => {
    const u = ['B', 'KB', 'MB', 'GB']
    let i = 0
    while (n >= 1024 && i < u.length - 1) {
      n /= 1024
      i++
    }
    return `${i ? n.toFixed(1) : n} ${u[i]}`
  }
  const fileName = (p) => p.split('/').pop()
</script>

{#if error}<div class="notice bad">{error}</div>{/if}

{#if book}
  {@const [label, tone] = bookState(book)}
  <a class="small" href={back} onclick={link}>← {book.type === 'audiobook' ? 'Audiobooks' : 'Books'}</a>
  <div class="hero">
    <Poster url={book.coverUrl} title={book.title} size="lg" />
    <div class="info">
      <h1>{book.title}</h1>
      <div class="byline">
        {book.author || 'Unknown author'}{book.year ? ` · ${book.year}` : ''}
        <span class="badge">{bookTypes[book.type]}</span>
        <Badge {label} {tone} />
      </div>
      {#if book.overview}<p class="muted overview">{book.overview}</p>{/if}
      {#if book.searchNote && book.status !== 'imported'}<p class="small muted">{book.searchNote}</p>{/if}
      {#if admin}
        <div class="small muted path"><code>{book.path}</code></div>
        <div class="row">
          {#if book.status !== 'imported'}
            <button class="primary" onclick={searchNow} disabled={book.status === 'downloading'}>Search now</button>
          {/if}
          <button onclick={manual}>{book.status === 'imported' ? 'Find another copy' : 'Pick a release'}</button>
          <label class="check mon">
            <input type="checkbox" checked={book.monitored} onchange={(e) => setMonitored(e.currentTarget.checked)} />
            <span class="small">Search automatically</span>
          </label>
          <button class="danger" onclick={remove}>Remove</button>
        </div>
      {/if}
      {#if notice}<div class="notice good" style="margin-top: 12px">{notice}</div>{/if}
    </div>
  </div>

  {#if book.files?.length}
    <h2>Files</h2>
    <div class="card files">
      <table>
        <tbody>
          {#each book.files as f (f.path)}
            <tr>
              <td class="fname">{fileName(f.path)}</td>
              <td><span class="badge">{f.format || '?'}</span></td>
              <td class="muted small nowrap">{human(f.sizeBytes)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/if}

{#if picking}
  <Modal title={`Pick a release: ${book.title}`} wide onClose={() => (picking = false)}>
    {#if pickError}<div class="notice bad">{pickError}</div>{/if}
    {#if releases === null && !pickError}
      <p class="muted">Asking your indexers and Soulseek… this takes about 20 seconds.</p>
    {:else if releases}
      <p class="muted small">
        {accepted.length} of {releases.length} results look like this {book.type === 'audiobook' ? 'audiobook' : 'ebook'}. The
        first one is what Bragi would pick.
      </p>
      <div class="table-wrap">
        <table class="releases">
          <thead><tr><th>Release</th><th>Format</th><th>Size</th><th>Source</th><th></th></tr></thead>
          <tbody>
            {#each showRejected ? releases : accepted as r, i (r.guid + r.title + i)}
              <tr class:dim={r.rejected}>
                <td>
                  <div class="rel">{r.title}</div>
                  {#if r.rejected}<div class="small bad-text">{r.rejected}</div>{/if}
                </td>
                <td><span class="badge">{r.format || '?'}</span></td>
                <td class="nowrap">{r.size}</td>
                <td class="small nowrap">
                  {r.indexer}<br /><span class="muted">{source(r)}{r.age ? ` · ${r.age}` : ''}</span>
                </td>
                <td class="actions">
                  <button class:primary={i === 0 && !r.rejected} onclick={() => grab(r)} disabled={!!grabbing}>
                    {grabbing === (r.guid || r.title) ? 'Sending…' : 'Grab'}
                  </button>
                </td>
              </tr>
            {:else}
              <tr><td colspan="5" class="muted">Nothing suitable found.</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if rejected.length}
        <button class="link small" style="margin-top: 10px" onclick={() => (showRejected = !showRejected)}>
          {showRejected ? 'Hide' : 'Show'} {rejected.length} rejected {rejected.length === 1 ? 'release' : 'releases'}
        </button>
      {/if}
    {/if}
  </Modal>
{/if}

<style>
  .hero {
    display: grid;
    grid-template-columns: 180px 1fr;
    gap: 24px;
    margin: 12px 0 24px;
  }
  .info {
    min-width: 0;
  }
  .hero h1 {
    margin-bottom: 4px;
  }
  .byline {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
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
  h2 {
    font-size: 1.1rem;
  }
  .files {
    padding: 4px 16px;
  }
  .files tr:last-child td {
    border-bottom: none;
  }
  .fname {
    word-break: break-all;
  }
  .table-wrap {
    overflow-x: auto;
  }
  .releases .rel {
    word-break: break-all;
    font-size: 0.9rem;
  }
  .nowrap {
    white-space: nowrap;
  }
  .bad-text {
    color: var(--bad);
  }
  tr.dim td {
    opacity: 0.6;
  }
  @media (max-width: 720px) {
    .hero {
      grid-template-columns: 100px minmax(0, 1fr);
      gap: 14px;
    }
    .hero :global(.lg) {
      width: 100px;
    }
  }
</style>
