import { useSyncExternalStore } from 'react'
import { relativeTime, exactTime } from './time'

// One timer for every <Time> on the page: relative times refresh every 30 seconds.
const tickMs = 30_000
const listeners = new Set<() => void>()
let timer: ReturnType<typeof setInterval> | undefined

function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  if (timer === undefined) {
    timer = setInterval(() => listeners.forEach((notify) => notify()), tickMs)
  }
  return () => {
    listeners.delete(listener)
    if (listeners.size === 0 && timer !== undefined) {
      clearInterval(timer)
      timer = undefined
    }
  }
}

const currentTick = () => Math.floor(Date.now() / tickMs)

export interface TimeProps {
  /** An ISO 8601 time; null or empty shows `fallback`. */
  iso: string | null | undefined
  /** What to show when there is no time. Default "never". */
  fallback?: string
  className?: string
}

/** A time as words ("3 minutes ago") with the exact time as the tooltip. */
export function Time({ iso, fallback = 'never', className }: TimeProps) {
  useSyncExternalStore(subscribe, currentTick)
  if (!iso) return <span className={className}>{fallback}</span>
  return (
    <time dateTime={iso} title={exactTime(iso)} className={className}>
      {relativeTime(iso)}
    </time>
  )
}
