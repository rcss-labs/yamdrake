// Times on the run screens (Plan 8b, R8): relative in the text, exact in the tooltip, durations as "2m 14s".
// Jobs' jobFormat.ts keeps its own helpers for the job screens.

const plural = (count: number, unit: string) => `${count} ${unit}${count === 1 ? '' : 's'}`

/**
 * "just now", "3 minutes ago", "2 hours ago", "5 days ago"; a time ahead of now reads "in 3 minutes".
 * `now` is in milliseconds (default: the clock) so a test can fix it.
 */
export function relativeTime(iso: string, now: number = Date.now()): string {
  const difference = Date.parse(iso) - now
  if (Number.isNaN(difference)) return ''
  const seconds = Math.round(Math.abs(difference) / 1000)
  if (seconds < 45) return 'just now'
  const minutes = Math.max(1, Math.round(seconds / 60))
  let words: string
  if (minutes < 60) {
    words = plural(minutes, 'minute')
  } else if (minutes < 48 * 60) {
    words = plural(Math.round(minutes / 60), 'hour')
  } else {
    words = plural(Math.round(minutes / (24 * 60)), 'day')
  }
  return difference > 0 ? `in ${words}` : `${words} ago`
}

/** "27 Nov 2026, 12:00:00 UTC" in the person's time zone; pass `timeZone` (for example "UTC") to fix it. */
export function exactTime(iso: string, timeZone?: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return ''
  return date.toLocaleString('en-GB', {
    day: 'numeric',
    month: 'short',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
    timeZoneName: 'short',
    timeZone,
  })
}

/** Milliseconds as "45s", "2m 14s" or "1h 2m". Negative or missing counts as zero. */
export function formatDuration(ms: number): string {
  const total = Math.max(0, Math.floor((Number.isFinite(ms) ? ms : 0) / 1000))
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  const seconds = total % 60
  if (hours > 0) return `${hours}h ${minutes}m`
  if (minutes > 0) return `${minutes}m ${seconds}s`
  return `${seconds}s`
}
