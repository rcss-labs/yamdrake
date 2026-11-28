// What the run page works out from a run's legs: which services it changes, how far each has got, and what
// Plan again, Restore and the strip need.
import type { LegState, RunDetail, RunLeg, Selection } from '../../api/types'
import type { StatusKey } from '../../ui'

const stepLegs = (run: RunDetail): RunLeg[] => run.legs.filter((leg) => leg.kind === 'step' && leg.service !== '')

export interface ServiceProgress {
  service: string
  version: string
  /** The one status that stands for all of the service's step legs. */
  status: LegState
  /** Every step of the service succeeded. */
  updated: boolean
  /** Something of the service succeeded, so a restore has something to undo. */
  changed: boolean
}

function overall(states: LegState[]): LegState {
  for (const worst of ['failed', 'unknown'] as const) if (states.includes(worst)) return worst
  for (const live of ['running', 'claimed', 'queued'] as const) if (states.includes(live)) return live
  if (states.length > 0 && states.every((state) => state === 'succeeded')) return 'succeeded'
  if (states.includes('cancelled')) return 'cancelled'
  if (states.includes('skipped')) return 'skipped'
  if (states.includes('succeeded')) return 'running'
  return 'pending'
}

/** One entry per service, in the order the run first mentions them. */
export function serviceProgress(run: RunDetail): ServiceProgress[] {
  const byService = new Map<string, RunLeg[]>()
  for (const leg of stepLegs(run)) byService.set(leg.service, [...(byService.get(leg.service) ?? []), leg])
  return Array.from(byService, ([service, legs]) => {
    const states = legs.map((leg) => leg.state)
    return {
      service,
      version: legs[0]?.version ?? '',
      status: overall(states),
      updated: states.every((state) => state === 'succeeded'),
      changed: states.includes('succeeded'),
    }
  })
}

export const statusOfService = (progress: ServiceProgress): StatusKey => progress.status

export function updatedCount(run: RunDetail): number {
  return serviceProgress(run).filter((service) => service.updated).length
}

/** The services a restore would put back: the ones this run changed. */
export function changedServices(run: RunDetail): string[] {
  return serviceProgress(run)
    .filter((service) => service.changed)
    .map((service) => service.service)
}

/** The selection to plan again: the one the run was made with, or one rebuilt from its step legs (group, version, per-service versions). */
export function selectionToRepeat(run: RunDetail): Selection {
  if (run.selection && Object.keys(run.selection).length > 0) return run.selection
  const groupOfStep = new Map(run.steps.map((step) => [step.name, step.group]))
  const perGroup = new Map<string, Map<string, string>>()
  for (const leg of stepLegs(run)) {
    const group = groupOfStep.get(leg.step) ?? ''
    const services = perGroup.get(group) ?? new Map<string, string>()
    if (!services.has(leg.service)) services.set(leg.service, leg.version)
    perGroup.set(group, services)
  }
  const selection: Selection = {}
  for (const [group, services] of perGroup) {
    const versions = Array.from(services.values())
    selection[group] = { version: versions[0] ?? '', services: Object.fromEntries(services) }
  }
  return selection
}

export const plural = (count: number, singular: string, pluralForm = `${singular}s`) => `${count} ${count === 1 ? singular : pluralForm}`
