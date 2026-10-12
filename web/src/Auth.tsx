import { useEffect, useState, type FormEvent } from 'react'
import {
  forgotPassword,
  login,
  register,
  resendVerification,
  resetPassword,
  verifyEmail,
} from './api'

type Mode = 'login' | 'register' | 'verify' | 'forgot' | 'reset'

export default function Auth({ onLogin }: { onLogin: () => Promise<void> }) {
  const params = new URLSearchParams(location.search)
  const verificationToken = params.get('verify')
  const resetToken = params.get('reset')
  const [mode, setMode] = useState<Mode>(verificationToken ? 'verify' : resetToken ? 'reset' : 'login')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  useEffect(() => {
    if (!verificationToken) return
    setBusy(true)
    void verifyEmail(verificationToken)
      .then(() => {
        setNotice('邮箱验证成功，请登录。')
        setMode('login')
        history.replaceState(null, '', '/')
      })
      .catch(() => setError('验证链接无效或已过期。请重新发送验证邮件。'))
      .finally(() => setBusy(false))
  }, [verificationToken])

  const navigate = (next: Mode) => {
    setMode(next)
    setError('')
    setNotice('')
    setPassword('')
    setConfirm('')
    if (next !== 'reset') history.replaceState(null, '', '/')
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy) return
    if ((mode === 'register' || mode === 'reset') && password !== confirm) {
      setError('两次输入的密码不一致。')
      return
    }
    setBusy(true)
    setError('')
    setNotice('')
    try {
      if (mode === 'login') {
        await login(email.trim(), password)
        await onLogin()
      } else if (mode === 'register') {
        await register(email.trim(), password)
        setMode('verify')
        setNotice('验证邮件请求已受理。请查看邮箱及垃圾邮件；未收到时可稍后重新发送。')
      } else if (mode === 'verify') {
        await resendVerification(email.trim())
        setNotice('验证邮件请求已受理。请查看邮箱及垃圾邮件；未收到时可稍后重新发送。')
      } else if (mode === 'forgot') {
        await forgotPassword(email.trim())
        setNotice('重置请求已受理。如邮箱可用，请查看收件箱及垃圾邮件；未收到时可稍后重新申请。')
      } else if (resetToken) {
        await resetPassword(resetToken, password)
        history.replaceState(null, '', '/')
        setMode('login')
        setPassword('')
        setConfirm('')
        setNotice('密码已重置。请重新登录。')
      } else {
        setError('重置链接缺少令牌，请重新申请。')
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '操作失败，请稍后重试。')
    } finally {
      setBusy(false)
    }
  }

  const titles: Record<Mode, string> = {
    login: '登录 Atoms',
    register: '创建账户',
    verify: '验证邮箱',
    forgot: '找回密码',
    reset: '重置密码',
  }

  return (
    <main className="auth-page">
      <div className="auth-mark">Atoms</div>
      <div className="auth-form-wrap">
        <h1>{titles[mode]}</h1>
        {mode === 'verify' && <p className="auth-description">验证后即可进入个人工作区。没有收到邮件时可重新发送。</p>}
        {mode === 'forgot' && <p className="auth-description">输入注册邮箱，我们将发送密码重置链接。</p>}
        {mode === 'reset' && <p className="auth-description">设置新密码后，所有设备的登录会话将失效，请重新登录。</p>}
        <form className="auth-form" onSubmit={(event) => void submit(event)}>
          {mode !== 'reset' && (
            <label>
              邮箱
              <input type="email" autoComplete="email" required value={email} onChange={(event) => setEmail(event.target.value)} />
            </label>
          )}
          {(mode === 'login' || mode === 'register' || mode === 'reset') && (
            <label>
              {mode === 'login' ? '密码' : '密码（至少 12 位）'}
              <input type="password" minLength={mode === 'login' ? undefined : 12} autoComplete={mode === 'login' ? 'current-password' : 'new-password'} required value={password} onChange={(event) => setPassword(event.target.value)} />
            </label>
          )}
          {(mode === 'register' || mode === 'reset') && (
            <label>
              确认密码
              <input type="password" minLength={12} autoComplete="new-password" required value={confirm} onChange={(event) => setConfirm(event.target.value)} />
            </label>
          )}
          {error && <div className="auth-alert" role="alert">{error}</div>}
          {notice && <div className="auth-notice" role="status">{notice}</div>}
          <button className="primary-action" type="submit" disabled={busy || (mode === 'reset' && !resetToken)}>
            {busy ? '请稍候…' : mode === 'login' ? '登录' : mode === 'register' ? '注册' : mode === 'verify' ? '重新发送验证邮件' : mode === 'forgot' ? '发送重置链接' : '重置密码'}
          </button>
        </form>
        <div className="auth-links">
          {mode === 'login' && <><button onClick={() => navigate('register')}>创建账户</button><button onClick={() => navigate('verify')}>验证邮箱</button><button onClick={() => navigate('forgot')}>忘记密码？</button></>}
          {mode !== 'login' && <button onClick={() => navigate('login')}>返回登录</button>}
          {mode === 'verify' && <button onClick={() => navigate('forgot')}>无法访问邮箱？</button>}
        </div>
        <p className="auth-footnote">旧 AuthSphere 账户、项目和公开链接不会迁移。</p>
      </div>
    </main>
  )
}
