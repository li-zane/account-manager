import {
  AtSign,
  CheckCircle2,
  CircleAlert,
  Clock3,
  Copy,
  Eye,
  EyeOff,
  GitBranch,
  KeyRound,
  Link2,
  LoaderCircle,
  RefreshCw,
  ShieldCheck,
  X,
} from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'

import { apiClient } from '../api/client'
import type { MailboxCredentialSummary, MailboxDetail, MailboxRecord, RetrievalCapabilitySummary, RevealedCredential } from '../api/types'
import { ProviderMark, providerMeta } from './ProviderMark'

interface MailboxDetailDrawerProps {
  mailbox: MailboxRecord
  onClose: () => void
}

const credentialLabels: Record<string, string> = {
  microsoft_graph_oauth: 'Microsoft Graph OAuth',
  microsoft_imap_oauth: 'Microsoft IMAP OAuth',
  microsoft_dual_token: 'Microsoft 共享 RT',
  gmail_oauth: 'Google OAuth',
  imap_password: 'IMAP 密码',
}

const refreshStatusLabels: Record<string, string> = {
  active: '可刷新',
  due: '等待刷新',
  expired: '访问令牌已过期',
  error: '刷新异常',
  unreadable: '凭据不可读',
  missing: '未配置 RT',
  unknown: '尚未验证',
}

function formatDate(value?: string): string {
  if (!value) return '未设置'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '未设置'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(date)
}

function inferCredential(mailbox: MailboxRecord): MailboxCredentialSummary {
  const credentialType = mailbox.provider === 'google'
    ? 'gmail_oauth'
    : mailbox.auth.modes.includes('graph') && mailbox.auth.modes.includes('imap')
      ? 'microsoft_dual_token'
      : mailbox.auth.modes.includes('imap')
        ? 'microsoft_imap_oauth'
        : mailbox.auth.modes.includes('graph')
          ? 'microsoft_graph_oauth'
          : 'imap_password'
  const methods = credentialType === 'microsoft_dual_token'
    ? ['microsoft_graph', 'imap_oauth']
    : credentialType === 'microsoft_graph_oauth'
      ? ['microsoft_graph']
      : credentialType === 'microsoft_imap_oauth'
        ? ['imap_oauth']
        : []
  return {
    credentialType,
    retrievalMethods: methods,
    retrievalCapabilities: methods.map((method) => ({
      method,
      status: 'unknown',
      accessTokenExpiresAt: method === 'imap_oauth' ? mailbox.auth.imapAccessTokenExpiresAt : mailbox.auth.graphAccessTokenExpiresAt,
    })),
    hasRefreshToken: false,
    refreshTokenValidity: mailbox.auth.refreshTokenValidity,
    expiresAt: mailbox.auth.refreshTokenExpiresAt,
    refreshStatus: 'unknown',
    autoRefresh: mailbox.auth.autoRefresh,
  }
}

function fallbackDetail(mailbox: MailboxRecord): MailboxDetail {
  return {
    mailbox,
    credentials: mailbox.auth.modes.includes('forward') ? [] : [inferCredential(mailbox)],
    aliases: mailbox.children.map((child) => ({
      id: child.id,
      address: child.address,
      provider: child.provider,
      kind: child.forwarding ? 'forward' : 'split',
      enabled: child.health !== 'offline',
    })),
    accounts: [],
  }
}

function MaskedValue({ value, fallback = '未同步' }: { value?: string; fallback?: string }) {
  return <span className={value ? 'detail-value detail-value--mono' : 'detail-value detail-value--muted'}>{value || fallback}</span>
}

const retrievalMethodLabels: Record<string, string> = {
  microsoft_graph: 'Graph',
  imap_oauth: 'IMAP',
  gmail_api: 'Gmail API',
  imap_password: 'IMAP',
}

const capabilityStatusLabels: Record<RetrievalCapabilitySummary['status'], string> = {
  verified: '取件已验证',
  configured: '通道已配置',
  failed: '最近验证失败',
  unknown: '尚未验证',
}

function credentialCapabilities(credential: MailboxCredentialSummary, revealed?: RevealedCredential): RetrievalCapabilitySummary[] {
  const supplied = revealed?.retrievalCapabilities.length ? revealed.retrievalCapabilities : credential.retrievalCapabilities
  if (supplied.length > 0) return supplied
  return credential.retrievalMethods.map((method) => ({
    method,
    status: 'unknown',
    accessTokenExpiresAt: method === 'imap_oauth'
      ? revealed?.imapTokenExpiresAt || credential.imapTokenExpiresAt
      : method === 'microsoft_graph'
        ? revealed?.graphTokenExpiresAt || credential.graphTokenExpiresAt
        : revealed?.expiresAt || credential.expiresAt,
  }))
}

function rtValidityLabel(mailbox: MailboxRecord, credential: MailboxCredentialSummary): string {
  if (credential.credentialType === 'imap_password') return '不适用'
  if (!credential.hasRefreshToken || credential.refreshTokenValidity === 'missing') return '未配置'
  if (credential.refreshTokenValidity === 'error') return '最近兑换异常，未返回精确到期时间'
  return mailbox.provider === 'microsoft' ? '微软未返回精确到期时间' : '上游未返回精确到期时间'
}

export function MailboxDetailDrawer({ mailbox, onClose }: MailboxDetailDrawerProps) {
  const [detail, setDetail] = useState<MailboxDetail>(() => fallbackDetail(mailbox))
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string>()
  const [selectedType, setSelectedType] = useState<string>()
  const [revealed, setRevealed] = useState<RevealedCredential>()
  const [revealError, setRevealError] = useState<string>()
  const [revealing, setRevealing] = useState(false)
  const [copied, setCopied] = useState<string>()

  useEffect(() => {
    const controller = new AbortController()
    setDetail(fallbackDetail(mailbox))
    setLoading(true)
    setLoadError(undefined)
    setRevealed(undefined)
    void apiClient.getMailboxDetail(mailbox.id, controller.signal)
      .then((value) => {
        setDetail(value)
        setSelectedType(value.credentials[0]?.credentialType)
      })
      .catch((error) => {
        if (!(error instanceof DOMException && error.name === 'AbortError')) {
          setLoadError(error instanceof Error ? error.message : '详情加载失败')
        }
      })
      .finally(() => setLoading(false))
    return () => controller.abort()
  }, [mailbox])

  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', closeOnEscape)
    return () => window.removeEventListener('keydown', closeOnEscape)
  }, [onClose])

  const activeCredential = useMemo(() => {
    return detail.credentials.find((credential) => credential.credentialType === selectedType) ?? detail.credentials[0]
  }, [detail.credentials, selectedType])
  const hasRevealableToken = Boolean(activeCredential?.hasRefreshToken)
  const capabilities = useMemo(() => activeCredential ? credentialCapabilities(activeCredential, revealed) : [], [activeCredential, revealed])

  const reveal = async () => {
    setRevealing(true)
    setRevealError(undefined)
    try {
      const value = await apiClient.revealMailboxCredential(mailbox.id, activeCredential?.credentialType)
      setRevealed(value)
    } catch (error) {
      setRevealError(error instanceof Error ? error.message : '凭据读取失败')
    } finally {
      setRevealing(false)
    }
  }

  const copy = async (label: string, value: string) => {
    try {
      await navigator.clipboard?.writeText(value)
      setCopied(label)
      window.setTimeout(() => setCopied(undefined), 1_800)
    } catch {
      setRevealError('浏览器未授予剪贴板权限')
    }
  }

  return (
    <div className="drawer-layer" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <aside className="detail-drawer" role="dialog" aria-modal="true" aria-labelledby="mailbox-detail-title">
        <header className="detail-drawer__header">
          <div className="detail-mailbox-heading">
            <ProviderMark provider={mailbox.provider} />
            <div>
              <p className="eyebrow">MAILBOX DETAIL</p>
              <h2 id="mailbox-detail-title">{mailbox.address}</h2>
              <span>{providerMeta[mailbox.provider].label} · {mailbox.id}</span>
            </div>
          </div>
          <button className="icon-button" type="button" title="关闭详情" aria-label="关闭邮箱详情" onClick={onClose}><X /></button>
        </header>

        <div className="detail-drawer__body">
          {loadError && <div className="inline-alert inline-alert--warning"><RefreshCw />遮罩摘要已显示，完整详情加载失败：{loadError}</div>}

          <section className="detail-section" aria-labelledby="credential-detail-title">
            <div className="detail-section__heading">
              <div><KeyRound /><h3 id="credential-detail-title">取件凭据</h3></div>
              {loading && <LoaderCircle className="spin" />}
            </div>

            {detail.credentials.length > 1 && (
              <div className="credential-tabs" role="tablist" aria-label="凭据类型">
                {detail.credentials.map((credential) => (
                  <button key={credential.id || credential.credentialType} className={activeCredential === credential ? 'credential-tab credential-tab--active' : 'credential-tab'} type="button" role="tab" aria-selected={activeCredential === credential} onClick={() => { setSelectedType(credential.credentialType); setRevealed(undefined) }}>
                    {credentialLabels[credential.credentialType] || credential.credentialType}
                  </button>
                ))}
              </div>
            )}

            {activeCredential ? (
              <>
                <dl className="credential-grid">
                  <div><dt>Client ID</dt><dd><MaskedValue value={revealed?.clientId || activeCredential.clientId} /></dd></div>
                  <div><dt>凭据类型</dt><dd><span className="detail-value">{credentialLabels[activeCredential.credentialType] || activeCredential.credentialType}</span></dd></div>
                  <div className="credential-grid__wide"><dt>共享 Refresh Token</dt><dd><MaskedValue value={revealed?.refreshToken || activeCredential.maskedRefreshToken} fallback={activeCredential.hasRefreshToken ? '已配置' : '未配置'} /></dd></div>
                  <div><dt>RT 状态</dt><dd><span className={activeCredential.refreshStatus === 'error' || activeCredential.refreshStatus === 'unreadable' ? 'detail-state detail-state--error' : hasRevealableToken ? 'detail-state detail-state--on' : 'detail-state'}><RefreshCw />{activeCredential.credentialType === 'imap_password' ? '不适用' : refreshStatusLabels[activeCredential.refreshStatus] || activeCredential.refreshStatus}</span></dd></div>
                  <div><dt>RT 有效期</dt><dd><span className="detail-value"><Clock3 />{rtValidityLabel(mailbox, activeCredential)}</span></dd></div>
                  <div><dt>自动刷新</dt><dd><span className={activeCredential.autoRefresh ? 'detail-state detail-state--on' : 'detail-state'}><RefreshCw />{activeCredential.autoRefresh ? '已开启' : '未开启'}</span></dd></div>
                  <div><dt>计划刷新</dt><dd><span className="detail-value"><Clock3 />{formatDate(activeCredential.refreshAfter)}</span></dd></div>
                  <div><dt>上次刷新</dt><dd><span className="detail-value"><Clock3 />{formatDate(activeCredential.lastRefreshedAt)}</span></dd></div>
                  {activeCredential.lastRefreshError && <div className="credential-grid__wide"><dt>最近错误</dt><dd><span className="detail-value detail-value--error">{activeCredential.lastRefreshError}</span></dd></div>}
                </dl>
                <div className="credential-methods" aria-label="取件能力">
                  {capabilities.map((capability) => (
                    <div className="credential-method-row" key={capability.method}>
                      <div className="credential-method-row__heading"><span><strong>{retrievalMethodLabels[capability.method] || capability.method}</strong><small>{capability.method}</small></span></div>
                      <span className={capability.status === 'failed' ? 'detail-state detail-state--error' : capability.status === 'verified' ? 'detail-state detail-state--on' : 'detail-state'}>{capability.status === 'failed' ? <CircleAlert /> : <CheckCircle2 />}{capabilityStatusLabels[capability.status]}</span>
                      <dl className="credential-method-row__details"><div><dt>短期 AT 有效期</dt><dd><span className="detail-value"><Clock3 />{formatDate(capability.accessTokenExpiresAt)}</span></dd></div>{capability.checkedAt && <div><dt>上次验证</dt><dd><span className="detail-value"><Clock3 />{formatDate(capability.checkedAt)}</span></dd></div>}</dl>
                    </div>
                  ))}
                </div>
              </>
            ) : <div className="detail-empty"><KeyRound />此邮箱通过转发取件，不使用上游 RT 凭据</div>}

            {hasRevealableToken && (
              <div className="reveal-actions">
                {revealed ? (
                  <>
                    <span className="reveal-expiry"><ShieldCheck />临时显示至 {formatDate(revealed.revealedUntil)}</span>
                    <button className="secondary-button" type="button" onClick={() => copy('Refresh Token', revealed.refreshToken)}><Copy />{copied === 'Refresh Token' ? '已复制' : '复制 RT'}</button>
                    <button className="secondary-button" type="button" onClick={() => setRevealed(undefined)}><EyeOff />隐藏</button>
                  </>
                ) : (
                  <button className="primary-button" type="button" onClick={reveal} disabled={revealing}>{revealing ? <LoaderCircle className="spin" /> : <Eye />}{revealing ? '正在读取' : '显示原始凭据'}</button>
                )}
              </div>
            )}
            {revealError && <div className="inline-alert inline-alert--error">{revealError}</div>}
          </section>

          <section className="detail-section" aria-labelledby="alias-detail-title">
            <div className="detail-section__heading"><div><GitBranch /><h3 id="alias-detail-title">Aliases</h3><span className="detail-count">{detail.aliases.length}</span></div></div>
            {detail.aliases.length > 0 ? <div className="detail-list">{detail.aliases.map((alias) => (
              <div className="detail-list__row" key={alias.id}>
                <ProviderMark provider={alias.provider} size="small" />
                <span><strong>{alias.address}</strong><small>{alias.kind === 'forward' ? '转发地址' : '分裂邮箱'} · {alias.id}</small></span>
                <span className={alias.enabled ? 'detail-state detail-state--on' : 'detail-state'}>{alias.enabled ? '启用' : '停用'}</span>
              </div>
            ))}</div> : <div className="detail-empty"><GitBranch />暂无分裂或转发地址</div>}
          </section>

          <section className="detail-section" aria-labelledby="account-detail-title">
            <div className="detail-section__heading"><div><Link2 /><h3 id="account-detail-title">关联平台账号</h3><span className="detail-count">{detail.accounts.length}</span></div></div>
            {detail.accounts.length > 0 ? <div className="detail-list">{detail.accounts.map((account) => (
              <div className="detail-list__row" key={account.id}>
                <span className="linked-account-icon"><AtSign /></span>
                <span><strong>{account.platform}</strong><small>{account.loginAddress || account.id}</small></span>
                <span className="detail-state detail-state--on"><CheckCircle2 />{account.status}</span>
              </div>
            ))}</div> : <div className="detail-empty"><Link2 />暂无关联平台账号</div>}
          </section>
        </div>
      </aside>
    </div>
  )
}
