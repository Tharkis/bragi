<script>
  import { link } from '../lib/router.svelte.js'

  // Tabs for the media types under one page: /discover, /discover/books, ...
  let { base, type } = $props()

  const tabs = [
    { type: 'tv', label: 'TV', path: '' },
    { type: 'book', label: 'Books', path: '/books' },
    { type: 'audiobook', label: 'Audiobooks', path: '/audiobooks' },
  ]
</script>

<div class="tabs">
  {#each tabs as t (t.type)}
    <a href={base + t.path} onclick={link} class:active={type === t.type}>{t.label}</a>
  {/each}
</div>
