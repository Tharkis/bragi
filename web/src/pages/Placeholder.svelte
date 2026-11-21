<script>
  let { path } = $props()

  const pages = {
    '/discover': ['Discover', 'Search for a show and request it. Arrives with the TV slice.'],
    '/library': ['Library', 'Everything Bragi tracks, with a status for each episode. Arrives with the TV slice.'],
    '/activity': ['Activity', 'The download queue and history. Arrives with the TV slice.'],
    '/requests': ['Requests', 'Requests waiting for approval and their progress. Arrives with the TV slice.'],
  }
  const page = $derived(pages[path] ?? ['Not found', 'There is no page here.'])
</script>

<h1>{page[0]}</h1>
<p class="muted">{page[1]}</p>
