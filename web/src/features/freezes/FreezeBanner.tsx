import { Note, exactTime } from '../../ui'
import { useFreezes } from '../runs/queries'

/** One warning per active freeze that covers this environment or label; nothing when none does. */
export function FreezeBanner({ environment, label }: { environment?: string; label?: string }) {
  const freezes = useFreezes()
  const matching = (freezes.data?.active ?? []).filter(
    (freeze) => (environment && freeze.environment === environment) || (label && freeze.label === label),
  )
  return (
    <>
      {matching.map((freeze) => (
        <Note key={freeze.id} tone="warning">
          {`Deployments to ${freeze.environment || freeze.label} are frozen until ${exactTime(freeze.expires_at)}: ${freeze.reason}`}
        </Note>
      ))}
    </>
  )
}
