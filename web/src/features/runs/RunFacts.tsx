// The facts row of the run page: where, who, when, how long, which versions, and the strip of services.
import type { RunDetail } from '../../api/types'
import { Facts, ServiceStrip, Tag, Time, formatDuration, type Fact } from '../../ui'
import { plural, serviceProgress, updatedCount } from './runModel'

function runningMilliseconds(run: RunDetail): number | undefined {
  if (!run.started_at) return undefined
  const end = run.finished_at ? Date.parse(run.finished_at) : Date.now()
  return Math.max(0, end - Date.parse(run.started_at))
}

export function RunFacts({ run }: { run: RunDetail }) {
  const services = serviceProgress(run)
  const versions = Array.from(new Set(services.map((service) => service.version).filter(Boolean)))
  const duration = runningMilliseconds(run)
  const who = [run.requested_by_name, run.via].filter(Boolean).join(', ')
  const items: Fact[] = [
    {
      label: 'Environment',
      value: (
        <>
          <span className="mono">{run.environment}</span> <Tag>{run.label}</Tag>
        </>
      ),
    },
    { label: 'Started by', value: who || 'Not available' },
    { label: 'Started', value: <Time iso={run.started_at} fallback="Not started yet" /> },
    { label: 'Duration', value: duration === undefined ? 'Not started yet' : formatDuration(duration) },
    { label: 'Versions', value: versions.length > 0 ? <span className="mono">{versions.join(', ')}</span> : 'Not available' },
  ]
  if (services.length > 0) {
    const updated = updatedCount(run)
    items.push({
      label: `Services, ${updated} updated`,
      value: (
        <ServiceStrip
          size="lg"
          label={`${plural(services.length, 'service')}, ${updated} updated`}
          cells={services.map((service) => ({ status: service.status, title: `${service.service} ${service.version}` }))}
        />
      ),
    })
  }
  return <Facts items={items} />
}
