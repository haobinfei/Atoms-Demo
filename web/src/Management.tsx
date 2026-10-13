import { useEffect, useState, type FormEvent } from 'react'
import {
  changePassword,
  createWorkspace,
  deleteAccount,
  deleteWorkspace,
  inviteMember,
  leaveWorkspace,
  listAdminAccounts,
  listAudit,
  listInvitations,
  listMembers,
  removeMember,
  rescueWorkspace,
  revokeInvitation,
  setAccountDisabled,
  updateMember,
  type Account,
  type AdminAccount,
  type AuditEntry,
  type Invitation,
  type Member,
  type WorkspaceInfo,
} from './api'

function message(error: unknown) {
  return error instanceof Error ? error.message : '操作失败，请稍后重试。'
}

export function Workspaces({ account, refresh, onWorkspaceDeleted }: {
  account: Account
  refresh: () => Promise<void>
  onWorkspaceDeleted: (id: string) => void
}) {
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  async function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!name.trim() || busy) return
    setBusy(true); setError(''); setNotice('')
    try {
      await createWorkspace(name.trim())
      setName('')
      await refresh()
      setNotice('团队工作区已创建。')
    } catch (cause) { setError(message(cause)) }
    finally { setBusy(false) }
  }

  return <main className="manage-page">
    <header className="manage-heading"><h1>工作区</h1><p>项目归当前工作区所有。切换工作区后显示其项目。</p></header>
    <div className="manage-list">
      {account.workspaces.map((workspace) => <div className="manage-row" key={workspace.id}>
        <div><strong>{workspace.name}</strong><small>{workspace.kind === 'personal' ? '个人工作区' : '团队工作区'} · {workspace.role === 'owner' ? '所有者' : '编辑者'}</small></div>
        {workspace.id === account.currentWorkspace && <span className="current-badge">当前</span>}
        {workspace.kind === 'team' && workspace.role === 'owner' && <button className="btn btn--sm btn--danger" onClick={async () => {
          if (!window.confirm(`删除「${workspace.name}」及其中所有项目？公开链接也将立即失效。此操作不可恢复。`)) return
          if (window.prompt(`再次输入工作区名称「${workspace.name}」以确认删除`) !== workspace.name) return
          setError(''); setNotice('')
          try { await deleteWorkspace(workspace.id, workspace.name); onWorkspaceDeleted(workspace.id); await refresh(); setNotice('工作区已删除。') }
          catch (cause) { setError(message(cause)) }
        }}>删除</button>}
      </div>)}
    </div>
    <form className="manage-form" onSubmit={(event) => void create(event)}>
      <h2>创建团队工作区</h2>
      <label className="field">名称<input value={name} maxLength={80} required onChange={(event) => setName(event.target.value)} placeholder="例如：产品团队" /></label>
      <button className="btn btn--primary" disabled={busy || !name.trim()}>{busy ? '创建中…' : '创建工作区'}</button>
    </form>
    {error && <p className="auth-alert" role="alert">{error}</p>}{notice && <p className="auth-notice" role="status">{notice}</p>}
  </main>
}

export function TeamSettings({ workspace, account, onLeft }: { workspace: WorkspaceInfo; account: Account; onLeft: () => Promise<void> }) {
  const [members, setMembers] = useState<Member[]>([])
  const [invitations, setInvitations] = useState<Invitation[]>([])
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<Member['role']>('editor')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const owner = workspace.role === 'owner'

  async function refresh() {
    const [membersResult, invitationsResult] = await Promise.all([
      listMembers(workspace.id),
      owner ? listInvitations(workspace.id) : Promise.resolve({ invitations: [] }),
    ])
    setMembers(membersResult.members ?? [])
    setInvitations(invitationsResult.invitations ?? [])
  }

  useEffect(() => {
    let active = true
    Promise.all([listMembers(workspace.id), owner ? listInvitations(workspace.id) : Promise.resolve({ invitations: [] })])
      .then(([memberResult, invitationResult]) => {
        if (!active) return
        setMembers(memberResult.members ?? [])
        setInvitations(invitationResult.invitations ?? [])
      })
      .catch((cause) => { if (active) setError(message(cause)) })
    return () => { active = false }
  }, [workspace.id, owner])

  async function run(action: () => Promise<unknown>, success: string) {
    setBusy(true); setError(''); setNotice('')
    try { await action(); await refresh(); setNotice(success) }
    catch (cause) { setError(message(cause)) }
    finally { setBusy(false) }
  }

  async function leave() {
    if (busy || !window.confirm(`退出「${workspace.name}」工作区？你将返回个人工作区。`)) return
    setBusy(true); setError(''); setNotice('')
    try { await leaveWorkspace(workspace.id); await onLeft() }
    catch (cause) { setError(message(cause)) }
    finally { setBusy(false) }
  }

  return <main className="manage-page">
    <header className="manage-heading"><h1>{workspace.name}</h1><p>团队成员与邀请</p></header>
    <section className="manage-section"><h2>成员</h2>
      <div className="manage-list">{members.map((member) => <div className="manage-row" key={member.accountId}>
        <div><strong>{member.email}</strong><small>{member.accountId === account.id ? '你 · ' : ''}{member.role === 'owner' ? '所有者' : '编辑者'}</small></div>
        {(owner || member.accountId === account.id) && <div className="row-actions">
          {owner && <select aria-label={`${member.email} 的角色`} value={member.role} disabled={busy} onChange={(event) => void run(() => updateMember(workspace.id, member.accountId, event.target.value as Member['role']), '成员角色已更新。')}>
            <option value="editor">编辑者</option><option value="owner">所有者</option>
          </select>}
          {member.accountId === account.id
            ? <button className="btn btn--sm btn--danger" disabled={busy} onClick={() => void leave()}>退出工作区</button>
            : <button className="btn btn--sm btn--danger" disabled={busy} onClick={() => {
              if (window.confirm(`移除 ${member.email}？`)) void run(() => removeMember(workspace.id, member.accountId), '成员已移除。')
            }}>移除</button>}
        </div>}
      </div>)}</div>
    </section>
    {owner && <><section className="manage-section">
      <h2>邀请成员</h2>
      <form className="invite-form" onSubmit={(event) => { event.preventDefault(); void run(async () => { await inviteMember(workspace.id, email.trim(), role); setEmail('') }, '邀请已发送。') }}>
        <label className="field">邮箱<input type="email" required value={email} onChange={(event) => setEmail(event.target.value)} placeholder="name@example.com" /></label>
        <label className="field">角色<select value={role} onChange={(event) => setRole(event.target.value as Member['role'])}><option value="editor">编辑者</option><option value="owner">所有者</option></select></label>
        <button className="btn btn--primary" disabled={busy || !email.trim()}>发送邀请</button>
      </form>
      <p className="manage-hint">邀请仅限指定邮箱的已验证账户接受，七天内有效。</p>
    </section><section className="manage-section"><h2>待接受邀请</h2>
      <div className="manage-list">{invitations.length === 0 && <p className="empty-state">暂无待接受邀请</p>}
        {invitations.map((invitation) => <div className="manage-row" key={invitation.id}>
          <div><strong>{invitation.email}</strong><small>{invitation.role === 'owner' ? '所有者' : '编辑者'} · {new Date(invitation.expiresAt).toLocaleString()} 到期</small></div>
          <button className="btn btn--sm btn--danger" disabled={busy} onClick={() => void run(() => revokeInvitation(workspace.id, invitation.id), '邀请已撤销。')}>撤销</button>
        </div>)}
      </div>
    </section></>}
    {error && <p className="auth-alert" role="alert">{error}</p>}{notice && <p className="auth-notice" role="status">{notice}</p>}
  </main>
}

export function AccountSettings({ account, onDeleted }: { account: Account; onDeleted: () => void }) {
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (newPassword !== confirm) { setError('两次输入的新密码不一致。'); return }
    setBusy(true); setError(''); setNotice('')
    try { await changePassword(currentPassword, newPassword); setCurrentPassword(''); setNewPassword(''); setConfirm(''); setNotice('密码已更新，其他设备已退出。') }
    catch (cause) { setError(message(cause)) }
    finally { setBusy(false) }
  }

  return <main className="manage-page">
    <header className="manage-heading"><h1>账户设置</h1><p>{account.email}</p></header>
    <section className="manage-section"><h2>修改密码</h2>
      <form className="manage-form" onSubmit={(event) => void submit(event)}>
        <label className="field">当前密码<input type="password" autoComplete="current-password" required value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} /></label>
        <label className="field">新密码（至少 12 位）<input type="password" autoComplete="new-password" minLength={12} required value={newPassword} onChange={(event) => setNewPassword(event.target.value)} /></label>
        <label className="field">确认新密码<input type="password" autoComplete="new-password" minLength={12} required value={confirm} onChange={(event) => setConfirm(event.target.value)} /></label>
        <button className="btn btn--primary" disabled={busy}>保存密码</button>
      </form>
    </section>
    <section className="manage-section danger-zone"><h2>删除账户</h2><p>个人工作区及其中的公开应用将立即下线。请先交接你独占所有权的团队工作区。</p>
      <button className="btn btn--danger" disabled={busy} onClick={async () => {
        if (!window.confirm('确定删除账户及个人工作区？此操作不可恢复。')) return
        if (window.prompt(`输入邮箱 ${account.email} 确认删除`) !== account.email) return
        setBusy(true); setError('')
        try { await deleteAccount(account.email); onDeleted() }
        catch (cause) { setError(message(cause)) }
        finally { setBusy(false) }
      }}>删除我的账户</button>
    </section>
    {error && <p className="auth-alert" role="alert">{error}</p>}{notice && <p className="auth-notice" role="status">{notice}</p>}
  </main>
}

export function AdminSettings({ currentAccountId }: { currentAccountId: string }) {
  const [accounts, setAccounts] = useState<AdminAccount[]>([])
  const [audit, setAudit] = useState<AuditEntry[]>([])
  const [workspaceId, setWorkspaceId] = useState('')
  const [accountId, setAccountId] = useState('')
  const [reason, setReason] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)

  async function refresh() {
    const [accountResult, auditResult] = await Promise.all([listAdminAccounts(), listAudit()])
    setAccounts(accountResult.accounts ?? [])
    setAudit(auditResult.entries ?? [])
  }
  useEffect(() => { void refresh().catch((cause) => setError(message(cause))) }, [])

  async function run(action: () => Promise<unknown>, success: string) {
    setBusy(true); setError(''); setNotice('')
    try { await action(); await refresh(); setNotice(success) }
    catch (cause) { setError(message(cause)) }
    finally { setBusy(false) }
  }

  return <main className="manage-page admin-page">
    <header className="manage-heading"><h1>平台管理</h1><p>账户与工作区归属处置。管理员身份不授予项目内容访问权。</p></header>
    <section className="manage-section"><h2>账户</h2><div className="manage-list">
      {accounts.map((account) => <div className="manage-row" key={account.id}>
        <div><strong>{account.email}</strong><small>{(account.memberships ?? []).map((m) => `${m.workspaceName}（${m.role === 'owner' ? '所有者' : '编辑者'}）`).join('、') || '无工作区'}</small></div>
        <span className={account.disabled ? 'badge badge--danger' : account.verified ? 'badge badge--success' : 'badge badge--muted'}>
          {account.disabled ? '已停用' : account.verified ? '正常' : '未验证'}
        </span>
        <button className="btn btn--sm" disabled={busy || account.id === currentAccountId} title={account.id === currentAccountId ? '不能停用当前管理员账户' : undefined} onClick={() => {
          const reason = window.prompt(`${account.disabled ? '恢复' : '停用'} ${account.email} 的原因（必填）`)
          if (reason?.trim()) void run(() => setAccountDisabled(account.id, !account.disabled, reason.trim()), '账户状态已更新。')
        }}>{account.disabled ? '恢复' : '停用'}</button>
      </div>)}
    </div></section>
    <section className="manage-section"><h2>所有者救援</h2>
      <form className="manage-form" onSubmit={(event) => { event.preventDefault(); void run(() => rescueWorkspace(workspaceId.trim(), accountId.trim(), reason.trim()), '所有者归属已更新。') }}>
        <label className="field">工作区 ID<input required value={workspaceId} onChange={(event) => setWorkspaceId(event.target.value)} /></label>
        <label className="field">新所有者账户 ID<input required value={accountId} onChange={(event) => setAccountId(event.target.value)} /></label>
        <label className="field">处置原因<textarea required minLength={5} value={reason} onChange={(event) => setReason(event.target.value)} /></label>
        <button className="btn btn--primary" disabled={busy}>提交救援</button>
      </form>
    </section>
    <section className="manage-section"><h2>管理审计</h2><div className="manage-list">
      {audit.length === 0 && <p className="empty-state">暂无管理记录</p>}
      {audit.map((entry) => <div className="manage-row" key={entry.id}><div><strong>{entry.action} · {entry.target}</strong><small>操作者 ID：{entry.actorId} · {new Date(entry.at).toLocaleString()} · {entry.reason}</small></div></div>)}
    </div></section>
    {error && <p className="auth-alert" role="alert">{error}</p>}{notice && <p className="auth-notice" role="status">{notice}</p>}
  </main>
}
