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

// movieState returns [label, tone] for a movie.
export function movieState(m) {
  if (m.filePath || m.status === 'imported') return ['In library', 'good']
  if (m.status === 'downloading' || m.downloading) return ['Downloading', 'accent']
  if (m.status === 'searching') return ['Searching', 'accent']
  if (!m.available || m.available > today()) return ['Not out yet', '']
  if (!m.monitored) return ['Not monitored', '']
  if (m.status === 'failed') return ['Failed', 'bad']
  return ['Wanted', 'warn']
}

// releaseNote says when a movie comes out, for movies that are not out yet.
export function releaseNote(m) {
  const fmt = (d) => new Date(d + 'T00:00:00Z').toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' })
  if (m.available && m.available > today()) return `Bragi starts looking on ${fmt(m.available)}`
  if (!m.available && !m.inCinemas) return 'No release date yet'
  return ''
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

// Words for the two book media types.
export const bookTypes = { book: 'Ebook', audiobook: 'Audiobook' }
export const isBook = (type) => type === 'book' || type === 'audiobook'

// bookState returns [label, tone] for a book.
export function bookState(b) {
  if (b.status === 'imported') return ['In library', 'good']
  if (b.status === 'downloading' || b.downloading) return ['Downloading', 'accent']
  if (b.status === 'searching') return ['Searching', 'accent']
  if (!b.monitored) return ['Not monitored', '']
  return ['Wanted', 'warn']
}

// albumState returns [label, tone] for an album.
export function albumState(a) {
  if (a.status === 'imported') return ['In library', 'good']
  if (a.status === 'upgrade_wanted') return ['Upgrade wanted', 'good']
  if (a.status === 'downloading' || a.downloading) return ['Downloading', 'accent']
  if (a.status === 'searching') return ['Searching', 'accent']
  if (a.releaseDate && a.releaseDate > today()) return ['Not out yet', '']
  if (!a.monitored) return ['Not monitored', '']
  if (a.importedTracks > 0) return [`${a.importedTracks} of ${a.tracks} tracks`, 'warn']
  return ['Wanted', 'warn']
}

// The album types MusicBrainz uses, in the order Bragi shows them.
export const albumTypes = ['Album', 'EP', 'Single', 'Live', 'Compilation', 'Other']

// itemPath links to the page of a show, movie, book or artist.
export function itemPath(type, id) {
  if (type === 'movie') return `/movies/${id}`
  if (type === 'music') return `/artists/${id}`
  if (isBook(type)) return `/books/${id}`
  return `/library/${id}`
}

export function scopeLabel(seasons, type) {
  if (type === 'movie') return 'Movie'
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
