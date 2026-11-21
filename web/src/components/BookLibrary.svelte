<script>
  import { onMount } from 'svelte'
  import { get } from '../lib/api.js'
  import { link } from '../lib/router.svelte.js'
  import { onEvents } from '../lib/events.js'
  import { bookState } from '../lib/status.js'
  import Poster from './Poster.svelte'
  import MediaTabs from './MediaTabs.svelte'
  import ScanPanel from './ScanPanel.svelte'

  // The Books or Audiobooks tab of the library: a cover grid sorted by
  // author, like a shelf.
  let { me, type } = $props()

  let list = $state([])
  let loaded = $state(false)
  let error = $state('')
  let filter = $state('')
  let panel = $state(null)
  let scanning = $state(false)
  const admin = me.role === 'admin'

  const noun = $derived(type === 'audiobook' ? 'audiobook' : 'book')

  async function load() {
    try {
      list = await get(`/books?type=${type}`)
      error = ''
    } catch (e) {
      error = e.message
    } finally {
      loaded = true
    }
  }

  onMount(() => {
    load()
    return onEvents(['book', 'download'], load)
  })

  const shown = $derived(
    filter
      ? list.filter((b) => (b.title + ' ' + b.author).toLowerCase().includes(filter.toLowerCase()))
      : list,
  )
  const have = $derived(list.filter((b) => b.status === 'imported').length)
</script>

<div class="page-head">
  <div>
    <h1>Library</h1>
    <p class="muted">
      {list.length} {list.length === 1 ? noun : noun + 's'}{list.length ? `, ${have} on disk` : ''}
    </p>
  </div>
  <div class="row">
    {#if list.length > 8}<input class="filter" placeholder="Filter by title or author…" bind:value={filter} aria-label="Filter" />{/if}
    {#if admin}
      <button onclick={() => panel?.start()} disabled={scanning}>
        {scanning ? 'Scanning…' : type === 'audiobook' ? 'Scan audiobooks folder' : 'Scan books folder'}
      </button>
    {/if}
  </div>
</div>

<MediaTabs base="/library" {type} />

{#if error}<div class="notice bad">{error}</div>{/if}

{#if admin}{#key type}<ScanPanel {type} bind:this={panel} bind:busy={scanning} onChange={load} />{/key}{/if}

{#if loaded && list.length === 0}
  <div class="card empty muted">
    <p>No {noun}s yet.</p>
    <p class="small"><a href="/discover" onclick={(e) => { try { localStorage.setItem('bragi.discover', type) } catch {} link(e) }}>Request one</a>{#if admin}, or press {type === 'audiobook' ? 'Scan audiobooks folder' : 'Scan books folder'} to add the {noun}s you already have{/if}.</p>
  </div>
{/if}

<div class="poster-grid">
  {#each shown as b (b.id)}
    {@const [label, tone] = bookState(b)}
    <a href={`/books/${b.id}`} onclick={link}>
      <Poster url={b.coverUrl} title={b.title} />
      <div class="title">{b.title}</div>
      <div class="small muted">{b.author}</div>
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
