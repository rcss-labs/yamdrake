import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import type { Inventory, Service, ServiceEnvironment } from '../../api/types'
import { Card, CodeBlock, ErrorState, Loading, SearchBox, Segmented } from '../../ui'
import { useInventoryService } from './queries'

function EnvironmentBlock({ name, block }: { name: string; block: ServiceEnvironment }) {
  const envFiles = block.env_files ?? []
  const tomls = block.tomls ?? []
  return (
    <section className="inv-block" aria-label={name}>
      <h3 className="mono">{name}</h3>
      <dl className="inv-pairs">
        <div>
          <dt>ECS service</dt>
          <dd className="mono">{block.ecs_service}</dd>
        </div>
        <div>
          <dt>ECR repository</dt>
          <dd className="mono">{block.ecr_repository}</dd>
        </div>
      </dl>
      <FileList title="Env files" emptyText="No env files" files={envFiles} />
      <FileList title="TOML files" emptyText="No TOML files" files={tomls} />
    </section>
  )
}

function FileList({
  title,
  emptyText,
  files,
}: {
  title: string
  emptyText: string
  files: { name: string; source: string; destination: string }[]
}) {
  return (
    <div className="inv-files">
      <h4>{title}</h4>
      {files.length === 0 ? (
        <p className="inv-empty-line">{emptyText}</p>
      ) : (
        <ul>
          {files.map((file) => (
            <li key={`${file.name}:${file.source}`}>
              <span className="mono inv-strong">{file.name}</span>
              <span className="inv-file-line">
                <span className="inv-file-label">Source</span>
                <span className="mono">{file.source}</span>
              </span>
              <span className="inv-file-line">
                <span className="inv-file-label">Destination</span>
                <span className="mono">{file.destination}</span>
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function ServiceDetail({ name, inventory, environment }: { name: string; inventory: Inventory; environment: string }) {
  const [params, setParams] = useSearchParams()
  const query = useInventoryService(name)

  if (query.isPending) return <Loading />
  if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />

  const service: Service = query.data
  // Keep the order of the inventory's environments rather than the object's key order.
  const present = inventory.environments.map((env) => env.name).filter((env) => env in service.environments)
  const shown = environment !== '' && present.includes(environment) ? [environment] : present
  const options = [{ value: '', label: 'All' }, ...present.map((env) => ({ value: env, label: env }))]

  function pick(value: string) {
    const next = new URLSearchParams(params)
    if (value === '') next.delete('env')
    else next.set('env', value)
    setParams(next)
  }

  return (
    <div className="inv-detail">
      <div>
        <h2 className="mono">{service.name}</h2>
        <dl className="inv-pairs inv-rule">
          <div>
            <dt>Version pattern</dt>
            <dd className="mono">{service.versions.pattern}</dd>
          </div>
          <div>
            <dt>Sorted by</dt>
            <dd className="mono">{service.versions.sort}</dd>
          </div>
          <div>
            <dt>Versions offered</dt>
            <dd className="mono">{service.versions.limit}</dd>
          </div>
        </dl>
      </div>
      <Segmented label="Environment to show" options={options} value={shown.length === 1 && environment !== '' ? environment : ''} onChange={pick} />
      {shown.map((env) => {
        const block = service.environments[env]
        return block ? <EnvironmentBlock key={env} name={env} block={block} /> : null
      })}
      {service.source !== '' && <CodeBlock label={service.file}>{service.source}</CodeBlock>}
    </div>
  )
}

export function ServicesTab({ inventory }: { inventory: Inventory }) {
  const [params, setParams] = useSearchParams()
  const [search, setSearch] = useState('')
  const selected = params.get('service') ?? ''
  const environment = params.get('env') ?? ''
  const total = inventory.environments.length
  const needle = search.trim().toLowerCase()
  const visible = inventory.services.filter((service) => service.name.toLowerCase().includes(needle))

  function choose(name: string) {
    const next = new URLSearchParams(params)
    next.set('service', name)
    next.delete('env')
    setParams(next)
  }

  return (
    <div className="inv-services">
      <Card flush className="inv-list-card">
        <div className="inv-search">
          <SearchBox label="Find a service" value={search} onChange={setSearch} />
        </div>
        {inventory.services.length === 0 ? (
          <p className="inv-empty">No services are defined in this commit.</p>
        ) : visible.length === 0 ? (
          <p className="inv-empty">
            <strong>No service matches</strong>
            <br />
            Clear the search to see every service.
          </p>
        ) : (
          <ul className="inv-list" aria-label="Services">
            {visible.map((service) => (
              <li key={service.name}>
                <button
                  type="button"
                  className="inv-item"
                  aria-current={service.name === selected ? 'true' : undefined}
                  onClick={() => choose(service.name)}
                >
                  <span className="mono">{service.name}</span>
                  <span className="inv-item-sub">
                    {service.environments.length === total ? 'all environments' : `${service.environments.length} of ${total} environments`}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </Card>
      <div className="inv-main">
        {selected === '' ? (
          <p className="inv-empty-line">Choose a service to see its environments.</p>
        ) : (
          <ServiceDetail key={selected} name={selected} inventory={inventory} environment={environment} />
        )}
      </div>
    </div>
  )
}
