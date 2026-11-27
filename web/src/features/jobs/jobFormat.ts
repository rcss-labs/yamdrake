const second = 1e9
const minute = 60 * second
const hour = 60 * minute

const plural = (count: number, unit: string) => `${count} ${unit}${count === 1 ? '' : 's'}`

/** Go durations arrive as nanoseconds; this gives plain words such as "1 hour 30 minutes". */
export function formatDuration(nanoseconds: number): string {
  const hours = Math.floor(nanoseconds / hour)
  const minutes = Math.floor((nanoseconds % hour) / minute)
  const seconds = Math.round((nanoseconds % minute) / second)
  const parts = [
    hours > 0 ? plural(hours, 'hour') : '',
    minutes > 0 ? plural(minutes, 'minute') : '',
    seconds > 0 ? plural(seconds, 'second') : '',
  ].filter(Boolean)
  return parts.length > 0 ? parts.join(' ') : '0 seconds'
}

/** "5 minutes ago" for a timestamp; "never" when there is none. */
export function formatAgo(timestamp: string | null, now: number = Date.now()): string {
  if (!timestamp) return 'never'
  const seconds = Math.max(0, Math.round((now - new Date(timestamp).getTime()) / 1000))
  if (seconds < 60) return 'just now'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${plural(minutes, 'minute')} ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return `${plural(hours, 'hour')} ago`
  return `${plural(Math.floor(hours / 24), 'day')} ago`
}

export const shortCommit = (commit: string) => commit.slice(0, 7)
