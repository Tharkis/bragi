// One set of words for item and download states, used on every screen.

const today = () => new Date().toISOString().slice(0, 10)

// episodeState returns [label, tone] for an episode.
export function episodeState(e) {
  if (e.status === 'imported') return ['Imported', 'good']
  if (e.status === 'downloading') return ['Downloading', 'accent']
  if (e.status === 'searching') return ['Searching', 'accent']
  if (!e.airDate || e.airDate > today()) return ['Not aired', '']
  if (!e.monitored) return ['Not monitored', '']
  if (e.status === 'failed') return ['Failed', 'bad']
  return ['Wanted', 'warn']
}

export const requestStates = {
  pending: ['Waiting for approval', 'warn'],
  approved: ['Approved', 'accent'],
  available: ['Available', 'good'],
  denied: ['Denied', 'bad'],
  failed: ['Failed', 'bad'],
}

export const downloadStates = {
  queued: ['Queued', ''],
  downloading: ['Downloading', 'accent'],
  completed: ['Importing', 'accent'],
  importing: ['Importing', 'accent'],
  imported: ['Imported', 'good'],
  failed: ['Failed', 'bad'],
}

export function scopeLabel(seasons) {
  if (seasons === 'all' || seasons === undefined) return 'All seasons'
  if (seasons === 'latest') return 'Latest season'
  if (seasons === 'future') return 'Future episodes'
  if (Array.isArray(seasons)) return (seasons.length === 1 ? 'Season ' : 'Seasons ') + seasons.join(', ')
  return ''
}

export function when(iso) {
  if (!iso) return ''
  const d = new Date(iso)
  const mins = (Date.now() - d.getTime()) / 60000
  if (mins < 1) return 'just now'
  if (mins < 60) return `${Math.round(mins)} min ago`
  if (mins < 24 * 60) return `${Math.round(mins / 60)} h ago`
  return d.toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' })
}
