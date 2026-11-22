<script>
  import { link } from '../lib/router.svelte.js'

  // The media-type tabs at the top of the library: /library, /library/movies, ...
  let { base, type } = $props()

  const tabs = [
    { type: 'tv', label: 'TV shows', path: '' },
    { type: 'movie', label: 'Movies', path: '/movies' },
    { type: 'book', label: 'Books', path: '/books' },
    { type: 'audiobook', label: 'Audiobooks', path: '/audiobooks' },
    { type: 'music', label: 'Music', path: '/music' },
  ]
</script>

<div class="tabs">
  {#each tabs as t (t.type)}
    <a href={base + t.path} onclick={link} class:active={type === t.type}>{t.label}</a>
  {/each}
</div>
