<script>
  import { onMount } from 'svelte'
  import { get, post } from './lib/api.js'
  import { route, navigate, link } from './lib/router.svelte.js'
  import CreateAdmin from './pages/CreateAdmin.svelte'
  import Login from './pages/Login.svelte'
  import Wizard from './pages/Wizard.svelte'
  import Settings from './pages/Settings.svelte'
  import Placeholder from './pages/Placeholder.svelte'
  import Discover from './pages/Discover.svelte'
  import Library from './pages/Library.svelte'
  import Series from './pages/Series.svelte'
  import Movie from './pages/Movie.svelte'
  import Activity from './pages/Activity.svelte'
  import Requests from './pages/Requests.svelte'
  import Book from './pages/Book.svelte'
  import BookLibrary from './components/BookLibrary.svelte'

  let status = $state(null)
  let me = $state(null)
  let loading = $state(true)
  let error = $state('')

  async function load() {
    loading = true
    error = ''
    try {
      status = await get('/setup')
      if (!status.needsAdmin) {
        try {
          me = await get('/auth/me')
        } catch (e) {
          if (e.status !== 401) throw e
          me = null
        }
      }
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  onMount(load)

  async function logout() {
    await post('/auth/logout')
    me = null
  }

  const nav = [
    { path: '/discover', label: 'Discover' },
    { path: '/library', label: 'Library' },
    { path: '/activity', label: 'Activity' },
    { path: '/requests', label: 'Requests' },
    { path: '/settings', label: 'Settings', admin: true },
  ]

  const inShell = $derived(me && !status?.needsAdmin && (me.role !== 'admin' || status?.setupComplete))

  $effect(() => {
    if (inShell && route.path === '/') navigate('/discover', true)
  })

  const seriesId = $derived(route.path.match(/^\/library\/(\d+)$/)?.[1])
  const movieId = $derived(route.path.match(/^\/movies\/(\d+)$/)?.[1])
  const bookId = $derived(route.path.match(/^\/books\/(\d+)$/)?.[1])

  const active = (path) =>
    route.path === path || route.path.startsWith(path + '/') || (path === '/library' && /^\/(movies|books)\//.test(route.path))
</script>

{#if loading}
  <div class="center-page muted">Loading…</div>
{:else if error}
  <div class="center-page">
    <div class="narrow card">
      <div class="notice bad">Bragi is not answering: {error}</div>
      <button onclick={load}>Try again</button>
    </div>
  </div>
{:else if status.needsAdmin}
  <CreateAdmin
    onDone={(u) => {
      me = u
      status.needsAdmin = false
    }}
  />
{:else if !me}
  <Login onDone={(u) => (me = u)} />
{:else if !inShell}
  <Wizard
    onDone={() => {
      status.setupComplete = true
      navigate('/discover')
    }}
  />
{:else}
  <div class="shell">
    <aside>
      <div class="brand"><img src="/icon.svg" alt="" /> Bragi</div>
      <nav>
        {#each nav.filter((n) => !n.admin || me.role === 'admin') as n (n.path)}
          <a href={n.path} onclick={link} class:active={active(n.path)}>{n.label}</a>
        {/each}
      </nav>
      <div class="who">
        <div class="small">{me.username} <span class="muted">· {me.role}</span></div>
        <button class="link small" onclick={logout}>Sign out</button>
      </div>
    </aside>
    <main>
      {#if active('/settings') && me.role === 'admin'}
        <Settings {me} />
      {:else if route.path === '/discover'}
        <Discover {me} />
      {:else if route.path === '/library' || route.path === '/library/movies'}
        {#key route.path}<Library {me} kind={route.path === '/library/movies' ? 'movie' : 'tv'} />{/key}
      {:else if route.path === '/library/books' || route.path === '/library/audiobooks'}
        {#key route.path}<BookLibrary {me} type={route.path === '/library/books' ? 'book' : 'audiobook'} />{/key}
      {:else if seriesId}
        {#key seriesId}<Series id={seriesId} {me} />{/key}
      {:else if movieId}
        {#key movieId}<Movie id={movieId} {me} />{/key}
      {:else if bookId}
        {#key bookId}<Book id={bookId} {me} />{/key}
      {:else if route.path === '/activity' || route.path === '/activity/history'}
        <Activity {me} tab={route.path === '/activity/history' ? 'history' : 'queue'} />
      {:else if route.path === '/requests'}
        <Requests {me} />
      {:else}
        <Placeholder path={route.path} />
      {/if}
    </main>
  </div>
{/if}

<style>
  .shell {
    display: grid;
    grid-template-columns: 220px 1fr;
    min-height: 100vh;
  }
  aside {
    background: var(--surface);
    border-right: 1px solid var(--border);
    padding: 20px 14px;
    display: flex;
    flex-direction: column;
    gap: 24px;
    position: sticky;
    top: 0;
    height: 100vh;
  }
  nav {
    display: flex;
    flex-direction: column;
    gap: 2px;
    flex: 1;
  }
  nav a {
    padding: 8px 12px;
    border-radius: 8px;
    color: var(--text);
    text-decoration: none;
  }
  nav a:hover {
    background: var(--surface-2);
  }
  nav a.active {
    background: var(--accent-soft);
    color: var(--accent);
    font-weight: 600;
  }
  .who {
    border-top: 1px solid var(--border);
    padding-top: 12px;
  }
  main {
    padding: 28px 32px;
    max-width: 1000px;
    width: 100%;
    min-width: 0;
  }
  @media (max-width: 720px) {
    .shell {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: auto 1fr;
    }
    aside {
      position: static;
      height: auto;
      flex-direction: row;
      flex-wrap: wrap;
      align-items: center;
      gap: 12px;
      border-right: none;
      border-bottom: 1px solid var(--border);
      padding: 12px 16px;
    }
    nav {
      flex-direction: row;
      overflow-x: auto;
      min-width: 0;
      order: 3;
      flex-basis: 100%;
    }
    .who {
      border: none;
      padding: 0;
      margin-left: auto;
    }
    main {
      padding: 20px 16px;
    }
  }
</style>
