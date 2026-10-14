import { useEffect, useState } from 'react'
import { acceptInvitation, getMe, logout, switchWorkspace, type Account } from './api'
import Auth from './Auth'
import Home from './Home'
import { AccountSettings, AdminSettings, TeamSettings, Workspaces } from './Management'
import Workspace from './Workspace'
import { apply, readChoice, save, watchSystem, type ThemeChoice } from './theme'

type Page = 'projects' | 'workspaces' | 'team' | 'account' | 'admin'

// Offered in the account menu rather than the header: the theme is a personal
// preference, and the header already carries the workspace controls.
const THEME_OPTIONS = [
  ['system', '跟随系统'],
  ['light', '浅色'],
  ['dark', '深色'],
] as const

export default function App() {
  const [sessionId, setSessionId] = useState<string | null>(() => new URLSearchParams(location.search).get('s'))
  const [account, setAccount] = useState<Account | null>(null)
  const [authChecked, setAuthChecked] = useState(false)
  const [authError, setAuthError] = useState('')
  const [homeView, setHomeView] = useState<'new' | 'projects'>('new')
  const [page, setPage] = useState<Page>('projects')
  const [switching, setSwitching] = useState(false)
  const [notice, setNotice] = useState('')
  const [noticeError, setNoticeError] = useState(false)
  const [inviteToken, setInviteToken] = useState(() => new URLSearchParams(location.search).get('invite'))
  const [menuOpen, setMenuOpen] = useState(false)
  const [theme, setTheme] = useState<ThemeChoice>(() => readChoice())

  // The choice is already on <html> from index.html; this keeps it there when
  // the choice changes and while "follow the system" is in effect.
  useEffect(() => {
    apply(theme)
    return watchSystem(theme, () => apply(theme))
  }, [theme])

  function chooseTheme(next: ThemeChoice) {
    save(next)
    setTheme(next)
  }

  async function refresh() {
    const next = await getMe()
    setAccount(next)
    setAuthError('')
  }

  useEffect(() => {
    let active = true
    void getMe().then((next) => {
      if (active) setAccount(next)
    }).catch((cause) => {
      if (active && !(cause instanceof Error && /\b401\b/.test(cause.message))) {
        setAuthError(cause instanceof Error ? cause.message : '无法连接服务。')
      }
    }).finally(() => { if (active) setAuthChecked(true) })
    return () => { active = false }
  }, [])

  const activeWorkspace = account?.workspaces.find((workspace) => workspace.id === account.currentWorkspace)

  async function changeWorkspace(id: string) {
    if (id === account?.currentWorkspace || switching) return
    setSwitching(true); setNotice('')
    try {
      await switchWorkspace(id)
      await refresh()
      setPage('projects')
      setSessionId(null)
      setHomeView('projects')
      history.replaceState(null, '', '/')
    } catch (cause) {
      setNoticeError(true)
      setNotice(cause instanceof Error ? cause.message : '切换工作区失败。')
    } finally { setSwitching(false) }
  }

  async function signOut() {
    try {
      await logout()
      setAccount(null)
      setSessionId(null)
      setPage('projects')
      history.replaceState(null, '', '/')
    } catch (cause) {
      setNoticeError(true)
      setNotice(cause instanceof Error ? cause.message : '退出失败，请稍后重试。')
    }
  }

  if (!authChecked) return <div className="auth-gate">正在检查登录状态…</div>
  if (!account) return <>
    {authError && <div className="startup-error" role="alert">{authError}<button onClick={() => void refresh().catch((cause) => setAuthError(cause instanceof Error ? cause.message : '连接失败'))}>重试</button></div>}
    <Auth onLogin={refresh} />
  </>

  return <div className="app-shell">
    <header className="app-header">
      <button className="app-logo" onClick={() => { setSessionId(null); setPage('projects'); history.replaceState(null, '', '/') }}>Atoms</button>
      <select className="workspace-select" aria-label="当前工作区" value={account.currentWorkspace} disabled={switching} onChange={(event) => void changeWorkspace(event.target.value)}>
        {account.workspaces.map((workspace) => <option key={workspace.id} value={workspace.id}>{workspace.name}</option>)}
      </select>
      <nav className="app-nav" aria-label="主导航">
        <button className={page === 'projects' ? 'active' : ''} onClick={() => setPage('projects')}>项目</button>
        <button className={page === 'workspaces' ? 'active' : ''} onClick={() => setPage('workspaces')}>工作区</button>
        {activeWorkspace?.kind === 'team' && <button className={page === 'team' ? 'active' : ''} onClick={() => setPage('team')}>成员</button>}
        <button className={page === 'account' ? 'active' : ''} onClick={() => setPage('account')}>账户</button>
        {account.admin && <button className={page === 'admin' ? 'active' : ''} onClick={() => setPage('admin')}>平台管理</button>}
      </nav>
      <div className="menu">
        <button
          className="menu-trigger"
          aria-haspopup="menu"
          aria-expanded={menuOpen}
          onClick={() => setMenuOpen((open) => !open)}
        >
          <span className="avatar">{account.email.slice(0, 1)}</span>
          <span className="app-email" title={account.email}>{account.email}</span>
        </button>
        {menuOpen && (
          <>
            <div className="menu-backdrop" onClick={() => setMenuOpen(false)} />
            <div className="menu-panel" role="menu">
              <div className="menu-header">{account.email}</div>
              <button role="menuitem" onClick={() => { setMenuOpen(false); setPage('account') }}>账户设置</button>
              <button role="menuitem" onClick={() => { setMenuOpen(false); setPage('workspaces') }}>工作区</button>
              <div className="menu-section">
                <span className="menu-section-title">主题</span>
                {THEME_OPTIONS.map(([value, label]) => (
                  <button
                    key={value}
                    role="menuitemradio"
                    aria-checked={theme === value}
                    className={theme === value ? 'is-checked' : ''}
                    onClick={() => chooseTheme(value)}
                  >
                    {label}
                  </button>
                ))}
              </div>
              <button role="menuitem" className="is-danger" onClick={() => { setMenuOpen(false); void signOut() }}>退出登录</button>
            </div>
          </>
        )}
      </div>
    </header>
    {notice && <div className={noticeError ? 'app-notice app-notice--error' : 'app-notice'} role="alert">{notice}<button onClick={() => { setNotice(''); setNoticeError(false) }} aria-label="关闭提示">×</button></div>}
    {inviteToken && <div className="invite-banner">
      <span>你收到了工作区邀请。</span>
      <button className="primary-action" onClick={async () => {
        try {
          await acceptInvitation(inviteToken)
          await refresh()
          setInviteToken(null)
          history.replaceState(null, '', '/')
          setNoticeError(false)
          setNotice('已加入工作区。可从顶部切换。')
        } catch (cause) { setNoticeError(true); setNotice(cause instanceof Error ? cause.message : '邀请无效或已过期。') }
      }}>接受邀请</button>
      <button onClick={() => { setInviteToken(null); history.replaceState(null, '', '/') }}>忽略</button>
    </div>}
    {page === 'workspaces' ? <Workspaces account={account} refresh={refresh} onWorkspaceDeleted={(id) => {
      if (id !== account.currentWorkspace) return
      setSessionId(null); setPage('projects'); setHomeView('projects')
      history.replaceState(null, '', '/')
    }} />
      : page === 'team' && activeWorkspace?.kind === 'team' ? <TeamSettings key={activeWorkspace.id} workspace={activeWorkspace} account={account} onLeft={async () => {
        await refresh(); setPage('projects'); setSessionId(null); setHomeView('projects')
        history.replaceState(null, '', '/')
      }} />
      : page === 'account' ? <AccountSettings account={account} onDeleted={() => { setAccount(null); history.replaceState(null, '', '/') }} />
      : page === 'admin' && account.admin ? <AdminSettings currentAccountId={account.id} />
      : sessionId ? <Workspace sessionId={sessionId} user={account.email} canPublish={activeWorkspace?.role === 'owner'} onExit={() => { setSessionId(null); history.pushState(null, '', '/') }} />
      : <Home user={account.email} view={homeView} canDelete={activeWorkspace?.role === 'owner'} onViewChange={setHomeView} onCreated={(id) => { setSessionId(id); history.pushState(null, '', `?s=${id}`) }} />}
  </div>
}
