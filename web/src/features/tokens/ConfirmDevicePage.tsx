import { useSearchParams } from 'react-router-dom'
import { errorMessage } from '../../api/client'
import { Button, Card, Note, PageHeader } from '../../ui'
import { useConfirmDevice } from './queries'
import './tokens.css'

export function ConfirmDevicePage() {
  const [params] = useSearchParams()
  const code = (params.get('code') ?? '').trim()
  const confirm = useConfirmDevice()

  return (
    <>
      <PageHeader title="Confirm a CLI sign-in" />
      <Card className="confirm-box">
        {code === '' ? (
          <p>
            There is no sign-in code in this address. Run <span className="mono">yumdrake login</span> in your terminal and open the link it prints.
          </p>
        ) : confirm.isSuccess ? (
          <>
            <p>
              Code <span className="mono">{code}</span> is confirmed. You can return to the terminal; it signs in on its own.
            </p>
          </>
        ) : (
          <>
            <p>The command line is asking to sign in as you. Check that this code matches the one in your terminal.</p>
            <span className="mono confirm-code">{code}</span>
            {confirm.isError && <Note tone="error">{errorMessage(confirm.error)}</Note>}
            <Button variant="primary" loading={confirm.isPending} onClick={() => confirm.mutate({ user_code: code })}>
              Confirm this sign-in for the command line
            </Button>
          </>
        )}
      </Card>
    </>
  )
}
