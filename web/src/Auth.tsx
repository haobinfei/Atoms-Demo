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
// Field-level messages, so a mistake is stated next to the field that caused
// it instead of only as one form-wide banner.
type FieldErrors = { email?: string; password?: string; confirm?: string }

const PASSWORD_MIN = 12
// The server refuses a resend storm for the same reason it rate-limits login;
// the button says how long rather than silently doing nothing.
const RESEND_COOLDOWN = 60

// Mirrors the server's rule (internal/identity/identity.go: validEmail) so the
// client never rejects an address the server would accept.
function emailLooksValid(value: string): boolean {
  const at = value.indexOf('@')
  return at > 0 && at < value.length - 3 && !/\s/.test(value)
}

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
  const [fieldErrors, setFieldErrors] = useState<FieldErrors>({})
  const [reveal, setReveal] = useState(false)
  const [cooldown, setCooldown] = useState(0)

  useEffect(() => {
    if (cooldown <= 0) return
    const timer = setTimeout(() => setCooldown(cooldown - 1), 1000)
    return () => clearTimeout(timer)
  }, [cooldown])

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
    setFieldErrors({})
    setPassword('')
    setConfirm('')
    if (next !== 'reset') history.replaceState(null, '', '/')
  }

  // Client-side only for the two rules the server actually enforces: a
  // well-formed address and a long enough password. A length rule measured in
  // characters is never looser than the server's byte count.
  function validate(): FieldErrors {
    const next: FieldErrors = {}
    if (mode !== 'reset') {
      const mail = email.trim()
      if (!mail) next.email = '请填写邮箱。'
      else if (!emailLooksValid(mail)) next.email = '这个邮箱地址看起来不完整。'
    }
    if (mode === 'login' || mode === 'register' || mode === 'reset') {
      if (!password) next.password = '请填写密码。'
      else if (mode !== 'login' && [...password].length < PASSWORD_MIN) {
        next.password = `密码至少需要 ${PASSWORD_MIN} 个字符，当前 ${[...password].length} 个。`
      }
    }
    if (mode === 'register' || mode === 'reset') {
      if (!confirm) next.confirm = '请再输入一次密码。'
      else if (confirm !== password) next.confirm = '两次输入的密码不一致。'
    }
    return next
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy) return
    const problems = validate()
    setFieldErrors(problems)
    if (Object.keys(problems).length > 0) {
      setError('')
      setNotice('')
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
        setCooldown(RESEND_COOLDOWN)
        setNotice('验证邮件请求已受理。请查看邮箱及垃圾邮件；未收到时可稍后重新发送。')
      } else if (mode === 'verify') {
        await resendVerification(email.trim())
        setCooldown(RESEND_COOLDOWN)
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

  const passwordLongEnough = [...password].length >= PASSWORD_MIN
  const passwordsMatch = confirm.length > 0 && confirm === password
  const showRules = mode === 'register' || mode === 'reset'

  return (
    <main className="auth-page">
      <aside className="auth-brand">
        <div className="auth-brand-logo">Atoms</div>
        <h2 className="auth-brand-title">把想法变成可运行的应用</h2>
        <p className="auth-brand-sub">描述你想要的，一支 AI 团队会为你生成、构建并发布。</p>
        <ul className="auth-brand-points">
          <li>生成：从一句话需求产出全栈代码</li>
          <li>预览：桌面与移动视口即时查看</li>
          <li>发布：一键分享可访问的公开链接</li>
        </ul>
      </aside>
      <div className="auth-form-wrap">
        <h1>{titles[mode]}</h1>
        {mode === 'verify' && <p className="auth-description">验证后即可进入个人工作区。没有收到邮件时可重新发送。</p>}
        {mode === 'forgot' && <p className="auth-description">输入注册邮箱，我们将发送密码重置链接。</p>}
        {mode === 'reset' && <p className="auth-description">设置新密码后，所有设备的登录会话将失效，请重新登录。</p>}
        <form className="auth-form" onSubmit={(event) => void submit(event)} noValidate>
          {mode !== 'reset' && (
            <div className="auth-field">
              <label htmlFor="auth-email">邮箱</label>
              <input
                id="auth-email"
                type="email"
                autoComplete="email"
                aria-invalid={fieldErrors.email ? true : undefined}
                aria-describedby={fieldErrors.email ? 'auth-email-error' : undefined}
                value={email}
                onChange={(event) => {
                  setEmail(event.target.value)
                  if (fieldErrors.email) setFieldErrors((prev) => ({ ...prev, email: undefined }))
                }}
              />
              {fieldErrors.email && (
                <p className="auth-field-error" id="auth-email-error">
                  {fieldErrors.email}
                </p>
              )}
            </div>
          )}
          {(mode === 'login' || mode === 'register' || mode === 'reset') && (
            <div className="auth-field">
              <label htmlFor="auth-password">{mode === 'login' ? '密码' : `密码（至少 ${PASSWORD_MIN} 位）`}</label>
              <div className="auth-input">
                <input
                  id="auth-password"
                  type={reveal ? 'text' : 'password'}
                  autoComplete={mode === 'login' ? 'current-password' : 'new-password'}
                  aria-invalid={fieldErrors.password ? true : undefined}
                  aria-describedby={fieldErrors.password ? 'auth-password-error' : undefined}
                  value={password}
                  onChange={(event) => {
                    setPassword(event.target.value)
                    if (fieldErrors.password) setFieldErrors((prev) => ({ ...prev, password: undefined }))
                  }}
                />
                <button
                  type="button"
                  className="auth-reveal"
                  aria-label={reveal ? '隐藏密码' : '显示密码'}
                  aria-pressed={reveal}
                  onClick={() => setReveal((on) => !on)}
                >
                  {reveal ? '隐藏' : '显示'}
                </button>
              </div>
              {fieldErrors.password && (
                <p className="auth-field-error" id="auth-password-error">
                  {fieldErrors.password}
                </p>
              )}
            </div>
          )}
          {(mode === 'register' || mode === 'reset') && (
            <div className="auth-field">
              <label htmlFor="auth-confirm">确认密码</label>
              <input
                id="auth-confirm"
                type={reveal ? 'text' : 'password'}
                autoComplete="new-password"
                aria-invalid={fieldErrors.confirm ? true : undefined}
                aria-describedby={fieldErrors.confirm ? 'auth-confirm-error' : undefined}
                value={confirm}
                onChange={(event) => {
                  setConfirm(event.target.value)
                  if (fieldErrors.confirm) setFieldErrors((prev) => ({ ...prev, confirm: undefined }))
                }}
              />
              {fieldErrors.confirm && (
                <p className="auth-field-error" id="auth-confirm-error">
                  {fieldErrors.confirm}
                </p>
              )}
            </div>
          )}
          {showRules && (
            <ul className="auth-rules" aria-live="polite">
              <li className={passwordLongEnough ? 'is-met' : ''}>至少 {PASSWORD_MIN} 个字符</li>
              <li className={passwordsMatch ? 'is-met' : ''}>两次输入一致</li>
            </ul>
          )}
          {error && <div className="auth-alert" role="alert">{error}</div>}
          {notice && <div className="auth-notice" role="status">{notice}</div>}
          <button
            className="primary-action"
            type="submit"
            disabled={busy || (mode === 'reset' && !resetToken) || (mode === 'verify' && cooldown > 0)}
          >
            {busy
              ? '请稍候…'
              : mode === 'login'
                ? '登录'
                : mode === 'register'
                  ? '注册'
                  : mode === 'verify'
                    ? cooldown > 0
                      ? `${cooldown} 秒后可重发`
                      : '重新发送验证邮件'
                    : mode === 'forgot'
                      ? '发送重置链接'
                      : '重置密码'}
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
