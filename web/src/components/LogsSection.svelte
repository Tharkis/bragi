<script>
  import { onMount } from 'svelte'
  import { get, put } from '../lib/api.js'

  // Settings, Logs: the live log with filters, the log level, and a
  // download of the log files to send in with a bug report.

  const MAX = 2000 // lines kept on screen

  let entries = $state([])
  let last = 0
  let level = $state('') // level in effect
  let envLevel = $state('info')
  let saved = $state('') // level saved under Settings ('' = env default)
  let components = $state([])
  let file = $state('')
  let error = $state('')

  let show = $state('debug') // minimum level shown
  let component = $state('')
  let query = $state('')
  let paused = $state(false)
  let follow = $state(true)
  let box

  function params(after) {
    const p = new URLSearchParams({ after: String(after), level: show, limit: String(MAX) })
    if (component) p.set('component', component)
    if (query.trim()) p.set('q', query.trim())
    return p.toString()
  }

  async function load(reset) {
    try {
      const r = await get('/system/logs?' + params(reset ? 0 : last))
      error = ''
      level = r.level
      envLevel = r.envLevel
      components = r.components
      file = r.file || ''
      last = r.last
      const next = reset ? r.entries : entries.concat(r.entries)
      entries = next.length > MAX ? next.slice(next.length - MAX) : next
      if (follow && box && r.entries.length) {
        requestAnimationFrame(() => (box.scrollTop = box.scrollHeight))
      }
    } catch (e) {
      error = e.message
    }
  }

  onMount(() => {
    get('/settings').then((s) => (saved = s.log_level || ''))
    load(true)
    const t = setInterval(() => {
      if (!paused && !document.hidden) load(false)
    }, 2000)
    return () => clearInterval(t)
  })

  let debounce
  function refilter() {
    clearTimeout(debounce)
    debounce = setTimeout(() => load(true), 250)
  }

  async function saveLevel() {
    try {
      await put('/settings', { log_level: saved })
      load(false)
    } catch (e) {
      error = e.message
    }
  }

  function onScroll() {
    follow = box.scrollTop + box.clientHeight >= box.scrollHeight - 24
  }

  const time = (t) => {
    const d = new Date(t)
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })
  }
  const day = (t) => new Date(t).toLocaleDateString([], { month: 'short', day: 'numeric' })
</script>

<h2>Logs</h2>
<p class="muted small">
  What Bragi is doing: requests, searches, grabs, downloads and imports. The same lines go to the container log
  {#if file}and to <code>{file}</code>{/if}. API keys and passwords are blanked out.
</p>

<div class="card controls">
  <label>
    <span>Log level</span>
    <select bind:value={saved} onchange={saveLevel}>
      <option value="">Default ({envLevel}, from BRAGI_LOG_LEVEL)</option>
      <option value="debug">Debug: every release and its verdict, every outgoing call</option>
      <option value="info">Info: requests, searches, grabs, imports</option>
      <option value="warn">Warn: problems only</option>
      <option value="error">Error: failures only</option>
    </select>
    <small>Takes effect right away. Use Debug when something does not download, then send the log file.</small>
  </label>
  <a class="button primary download" href="/api/v1/system/logs/download" download>Download logs</a>
</div>

<div class="filters">
  <select bind:value={show} onchange={refilter} aria-label="Show level">
    <option value="debug">All levels</option>
    <option value="info">Info and up</option>
    <option value="warn">Warnings and errors</option>
    <option value="error">Errors</option>
  </select>
  <select bind:value={component} onchange={refilter} aria-label="Component">
    <option value="">Everything</option>
    {#each components as c (c)}<option value={c}>{c}</option>{/each}
  </select>
  <input type="search" placeholder="Search the log" bind:value={query} oninput={refilter} />
  <button onclick={() => (paused = !paused)}>{paused ? 'Resume' : 'Pause'}</button>
</div>

{#if error}<p class="error">{error}</p>{/if}

<div class="log" bind:this={box} onscroll={onScroll}>
  {#each entries as e (e.id)}
    <div class="line lvl-{e.level}">
      <span class="time" title={new Date(e.time).toString()}>{day(e.time)} {time(e.time)}</span>
      <span class="level">{e.level}</span>
      {#if e.component}<span class="comp">{e.component}</span>{/if}
      <span class="msg">{e.message}</span>
      {#each e.attrs || [] as a, i (i)}
        <span class="attr"><span class="k">{a.key}=</span>{a.value}</span>
      {/each}
    </div>
  {:else}
    <p class="muted empty">No log lines match.{level && level !== 'debug' ? ` The level is ${level}; set it to Debug to see more.` : ''}</p>
  {/each}
</div>
<p class="muted small foot">
  Showing {entries.length} line{entries.length === 1 ? '' : 's'}{paused ? ', paused' : ''}. Level in effect: <strong>{level}</strong>.
</p>

<style>
  .controls {
    display: flex;
    gap: 16px;
    align-items: flex-end;
    flex-wrap: wrap;
    margin-bottom: 14px;
  }
  .controls label {
    flex: 1 1 320px;
    margin: 0;
  }
  .download {
    margin-bottom: 26px;
  }
  @media (max-width: 700px) {
    .download {
      margin-bottom: 0;
    }
    .log {
      font-size: 11.5px;
    }
  }
  .filters {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
    margin-bottom: 10px;
  }
  .filters select {
    width: auto;
  }
  .filters input {
    flex: 1 1 200px;
    width: auto;
  }
  .error {
    color: var(--bad);
  }
  .log {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    height: 60vh;
    min-height: 320px;
    overflow: auto;
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-size: 12.5px;
    line-height: 1.45;
    padding: 6px 0;
  }
  .line {
    padding: 2px 12px;
    border-left: 3px solid transparent;
    overflow-wrap: anywhere;
  }
  .line:hover {
    background: var(--surface-2);
  }
  .line > span + span {
    margin-left: 8px;
  }
  .time {
    color: var(--muted);
    white-space: nowrap;
  }
  .level {
    display: inline-block;
    min-width: 3.6em;
    text-transform: uppercase;
    font-weight: 600;
    color: var(--muted);
  }
  .comp {
    color: var(--accent);
  }
  .msg {
    font-weight: 600;
  }
  .attr {
    color: var(--text);
  }
  .k {
    color: var(--muted);
  }
  .lvl-debug .msg {
    font-weight: 400;
    color: var(--muted);
  }
  .lvl-warn {
    border-left-color: #e8a317;
  }
  .lvl-warn .level {
    color: #b7791f;
  }
  .lvl-error {
    border-left-color: var(--bad);
    background: var(--bad-soft);
  }
  .lvl-error .level {
    color: var(--bad);
  }
  .empty {
    padding: 12px;
    font-family: ui-sans-serif, system-ui, sans-serif;
  }
  .foot {
    margin-top: 6px;
  }
</style>
