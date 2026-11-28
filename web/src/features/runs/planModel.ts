// Small questions about a plan that more than one screen asks.
import type { Check, ConfigItem, Plan, PlanService } from '../../api/types'

/** The checks that failed; the run cannot be started while there is one. A plan not loaded yet has none. */
export function failedChecks(plan: Plan | undefined): Check[] {
  return plan?.checks.filter((check) => check.status === 'failed') ?? []
}

/** Config items that used the dev branch because the version's own branch does not exist. */
export function fallbackItems(plan: Plan | undefined): { service: PlanService; item: ConfigItem }[] {
  return (plan?.services ?? []).flatMap((service) => service.config.filter((item) => item.fallback).map((item) => ({ service, item })))
}
