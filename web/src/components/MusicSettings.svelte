<script>
  // Music options (which album types to follow, upgrades) and the
  // Navidrome connection.
  import { onMount } from 'svelte'
  import { get, put, post } from '../lib/api.js'
  import { albumTypes } from '../lib/status.js'

  let types = $state(['Album', 'EP', 'Single'])
  let upgrades = $state(true)
  let ndUrl = $state('')
  let ndUser = $state('')
  let ndPassword = $state('')
  let ndTest = $state(null)
  let saved = $state('')
  let error = $state('')
  let busy = $state(false)

  const help = {
    Album: 'Studio albums.',
    EP: 'Short releases.',
    Single: 'Singles, often one or two songs.',
    Live: 'Concert recordings.',
    Compilation: 'Best-ofs and collections, mostly songs you already have.',
    Other: 'Soundtracks, remixes, demos and the rest.',
  }

  onMount(async () => {
    try {
      const s = await get('/settings')
      if (s.music_album_types) types = s.music_album_types.split(',').map((t) => t.trim()).filter(Boolean)
      upgrades = s.music_upgrades !== 'false'
      ndUrl = s.navidrome_url
      ndUser = s.navidrome_user
      ndPassword = s.navidrome_password
    } catch (e) {
      error = e.message
    }
  })

  async function testNavidrome() {
    ndTest = { pending: true }
    ndTest = await post('/settings/test/navidrome', { url: ndUrl, user: ndUser, password: ndPassword }).catch((e) => ({ ok: false, error: e.message }))
  }

  async function save(event) {
    event.preventDefault()
    busy = true
    error = ''
    saved = ''
    try {
      await put('/settings', {
        music_album_types: albumTypes.filter((t) => types.includes(t)).join(','),
        music_upgrades: upgrades ? 'true' : 'false',
        navidrome_url: ndUrl,
        navidrome_user: ndUser,
        navidrome_password: ndPassword,
      })
      saved = 'Saved.'
    } catch (e) {
      error = e.message
    } finally {
      busy = false
    }
  }
</script>

<form onsubmit={save}>
  <div class="card section">
    <h2>Followed artists</h2>
    <p class="muted small">
      When you follow an artist, Bragi wants these kinds of releases, old and new. You can still tick any single album on the
      artist's page.
    </p>
    <div class="types">
      {#each albumTypes as t (t)}
        <label class="check">
          <input type="checkbox" value={t} bind:group={types} />
          <span><strong>{t === 'EP' ? 'EPs' : t + 's'}</strong> <span class="muted small">{help[t]}</span></span>
        </label>
      {/each}
    </div>
    {#if types.length === 0}<div class="notice bad">Pick at least one, or Bragi falls back to albums, EPs and singles.</div>{/if}
    <label class="check up">
      <input type="checkbox" bind:checked={upgrades} />
      <span>
        <strong>Upgrade albums</strong>
        <span class="muted small">Keep looking for a better copy until an album is at the top of its quality list, such as FLAC.</span>
      </span>
    </label>
  </div>

  <div class="card section">
    <h2>Navidrome (optional)</h2>
    <p class="muted small">
      Bragi asks Navidrome to scan right after it imports an album, so new music shows up straight away. Use an admin user;
      Navidrome only lets admins start a scan.
    </p>
    <label><span>Navidrome URL</span><input bind:value={ndUrl} placeholder="http://192.168.1.10:4533" /></label>
    <label><span>Username</span><input bind:value={ndUser} autocomplete="off" /></label>
    <label><span>Password</span><input type="password" bind:value={ndPassword} autocomplete="off" /></label>
    {#if ndTest?.ok}<div class="notice good">Connected to Navidrome.</div>
    {:else if ndTest && !ndTest.pending}<div class="notice bad">{ndTest.error}</div>{/if}
    <button type="button" onclick={testNavidrome} disabled={!ndUrl || !ndUser || ndTest?.pending}>Test</button>
  </div>

  {#if error}<div class="notice bad">{error}</div>{/if}
  {#if saved}<div class="notice good">{saved}</div>{/if}
  <div class="row">
    <button class="primary" disabled={busy}>Save</button>
  </div>
</form>

<style>
  .section {
    margin-bottom: 16px;
  }
  .section > button {
    margin-top: 2px;
  }
  .types {
    display: grid;
    gap: 4px;
    margin-bottom: 12px;
  }
  .check {
    display: flex;
    gap: 8px;
    align-items: baseline;
  }
  .check input {
    width: auto;
  }
  .up {
    border-top: 1px solid var(--border);
    padding-top: 12px;
  }
</style>
