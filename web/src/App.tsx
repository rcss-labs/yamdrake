import { useMe } from './auth/session'

// A temporary signed-in page; the shell and route table replace it.
export function App() {
  const me = useMe()
  return (
    <main>
      <h1>Yumdrake</h1>
      <p>Signed in as {me.name}</p>
    </main>
  )
}
