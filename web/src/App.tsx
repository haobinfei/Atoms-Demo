import { useEffect, useState } from 'react'
import { getMe } from './api'
import Home from './Home'
import Workspace from './Workspace'

export default function App() {
  const [sessionId, setSessionId] = useState<string | null>(
    () => new URLSearchParams(location.search).get('s'),
  )
  const [user, setUser] = useState<string | null>(null)
  const [authChecked, setAuthChecked] = useState(false)

  // Login gate: without a valid AuthSphere session the API answers 401 and
  // the browser is sent to the authorize page; the callback returns here
  // with the session cookie set. In dev mode /api/me always succeeds.
  useEffect(() => {
    let cancelled = false
    void getMe()
      .then((u) => {
        if (cancelled) return
        setUser(u)
        setAuthChecked(true)
      })
      .catch(() => {
        window.location.href = '/api/auth/login'
      })
    return () => {
      cancelled = true
    }
  }, [])

  if (!authChecked) {
    return <div className="auth-gate">正在检查登录状态…</div>
  }

  if (sessionId) {
    return (
      <Workspace
        sessionId={sessionId}
        user={user ?? ''}
        onExit={() => {
          setSessionId(null)
          history.pushState(null, '', '/')
        }}
      />
    )
  }
  return (
    <Home
      user={user ?? ''}
      onCreated={(id) => {
        setSessionId(id)
        history.pushState(null, '', `?s=${id}`)
      }}
    />
  )
}
