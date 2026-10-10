import { useState } from 'react'
import Home from './Home'
import Workspace from './Workspace'

export default function App() {
  const [sessionId, setSessionId] = useState<string | null>(
    () => new URLSearchParams(location.search).get('s'),
  )

  if (sessionId) {
    return <Workspace sessionId={sessionId} onExit={() => {
      setSessionId(null)
      history.pushState(null, '', '/')
    }} />
  }
  return <Home onCreated={(id) => {
    setSessionId(id)
    history.pushState(null, '', `?s=${id}`)
  }} />
}
