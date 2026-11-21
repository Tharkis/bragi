<script>
  // Add or edit an indexer or download client. The fields come from the
  // server's kind registry, so a new adapter needs no UI change.
  import { post, put } from '../lib/api.js'

  let { endpoint, kinds, initial = null, onSaved, onCancel = null, saveLabel = 'Save' } = $props()

  const blank = {
    id: 0,
    name: '',
    kind: '',
    baseUrl: '',
    apiKey: '',
    username: '',
    password: '',
    category: '',
    enabled: true,
    priority: 25,
  }
  // The form is seeded once; callers re-key it to load another item.
  // svelte-ignore state_referenced_locally
  let item = $state({ ...blank, kind: kinds[0]?.id ?? '', ...(initial ?? {}) })
  let test = $state(null) // { ok, error }
  let error = $state('')
  let busy = $state(false)

  const kind = $derived(kinds.find((k) => k.id === item.kind))

  // A changed setting makes an earlier test result stale.
  $effect(() => {
    JSON.stringify(item)
    test = null
  })

  async function runTest() {
    busy = true
    error = ''
    try {
      const result = await post(endpoint + '/test', item)
      test = result
    } catch (e) {
      error = e.message
    } finally {
      busy = false
    }
  }

  async function save(event) {
    event.preventDefault()
    busy = true
    error = ''
    try {
      const saved = item.id ? await put(`${endpoint}/${item.id}`, item) : await post(endpoint, item)
      onSaved(saved)
    } catch (e) {
      error = e.message
    } finally {
      busy = false
    }
  }
</script>

<form onsubmit={save}>
  <label>
    <span>Type</span>
    <select bind:value={item.kind} disabled={!!item.id}>
      {#each kinds as k (k.id)}
        <option value={k.id}>{k.label}</option>
      {/each}
    </select>
  </label>
  <label>
    <span>Name</span>
    <input bind:value={item.name} placeholder={kind?.label ?? ''} />
  </label>
  {#each kind?.fields ?? [] as f (f.name)}
    <label>
      <span>{f.label}{f.required ? '' : ' (optional)'}</span>
      <input
        type={f.type === 'password' ? 'password' : 'text'}
        bind:value={item[f.name]}
        placeholder={f.placeholder ?? ''}
        required={f.required}
        autocomplete="off"
      />
      {#if f.help}<small>{f.help}</small>{/if}
    </label>
  {/each}
  <label class="check"><input type="checkbox" bind:checked={item.enabled} /> <span>Enabled</span></label>

  {#if test?.ok}
    <div class="notice good">Connected to {kind?.label}.</div>
  {:else if test}
    <div class="notice bad">{test.error}</div>
  {/if}
  {#if error}<div class="notice bad">{error}</div>{/if}

  <div class="row">
    <button type="button" onclick={runTest} disabled={busy}>Test</button>
    <button class="primary" disabled={busy}>{saveLabel}</button>
    {#if onCancel}<button type="button" class="link" onclick={onCancel}>Cancel</button>{/if}
  </div>
</form>
