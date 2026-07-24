import {
  Check,
  ChevronRight,
  CircleAlert,
  CircleX,
  Copy,
  Download,
  GitBranch,
  KeyRound,
  LoaderCircle,
  Mail,
  PanelRightOpen,
  RefreshCw,
  ShieldCheck,
} from 'lucide-react'

import type { MailAccessMode, MailboxRecord, RetrievalKeyStatus } from '../api/types'
import { ProviderMark, providerMeta } from './ProviderMark'

interface MailboxTableProps {
  mailboxes: MailboxRecord[]
  expandedIds: Set<string>
  selectedIds: Set<string>
  onToggleExpanded: (mailboxId: string) => void
  onToggleSelected: (mailboxId: string) => void
  onToggleAll: () => void
  onCopyAddress: (address: string) => void
  onOpenInbox: (mailbox: MailboxRecord) => void
  onExportMailbox: (mailbox: MailboxRecord) => void
  onOpenDetails: (mailbox: MailboxRecord) => void
}

const retrievalLabels: Record<RetrievalKeyStatus, string> = {
  ready: '可用',
  expiring: '即将到期',
  expired: '已过期',
  missing: '待签发',
  not_applicable: '无需密钥',
}

const accessLabels: Record<MailAccessMode, string> = {
  graph: 'Graph',
  imap: 'IMAP',
  oauth: 'OAuth',
  forward: '转发',
}

function formatDate(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(date)
}

function RetrievalBadge({ status }: { status: RetrievalKeyStatus }) {
  const Icon = status === 'ready' ? ShieldCheck : status === 'expired' ? CircleX : CircleAlert
  return (
    <span className={`status-badge status-badge--${status}`}>
      <Icon aria-hidden="true" />
      {retrievalLabels[status]}
    </span>
  )
}

function MailboxCheckbox({ checked, label, onChange }: { checked: boolean; label: string; onChange: () => void }) {
  return (
    <label className="check-control" title={label}>
      <input type="checkbox" checked={checked} onChange={onChange} aria-label={label} />
      <span className="check-control__box" aria-hidden="true"><Check /></span>
    </label>
  )
}

function MailboxIdentity({ mailbox, isChild, onCopyAddress }: { mailbox: MailboxRecord; isChild: boolean; onCopyAddress: (address: string) => void }) {
  return (
    <div className={`mailbox-identity ${isChild ? 'mailbox-identity--child' : ''}`}>
      {isChild && <GitBranch className="branch-icon" aria-hidden="true" />}
      <ProviderMark provider={mailbox.provider} size={isChild ? 'small' : 'medium'} />
      <span className="mailbox-identity__copy">
        <button className="mailbox-address-button" type="button" title="复制邮箱地址" onClick={() => onCopyAddress(mailbox.address)}>{mailbox.address}</button>
        <small>
          {mailbox.displayName || providerMeta[mailbox.provider].label}
          {!isChild && mailbox.children.length > 0 && <span className="alias-count-badge"><GitBranch />分裂 {mailbox.children.length}</span>}
          {isChild && <span className="split-label">分裂邮箱</span>}
        </small>
      </span>
    </div>
  )
}

function TokenDetails({ mailbox }: { mailbox: MailboxRecord }) {
  return (
    <div className="token-cell">
      <RetrievalBadge status={mailbox.retrievalKey.status} />
      <span className="token-mask">
        <KeyRound aria-hidden="true" />
        {mailbox.retrievalKey.maskedKey || '尚未签发'}
      </span>
    </div>
  )
}

function AuthDetails({ mailbox }: { mailbox: MailboxRecord }) {
  return (
    <div className="auth-cell">
      <span className="auth-modes">
        {mailbox.auth.modes.length > 0 ? mailbox.auth.modes.map((mode) => <span key={mode}>{accessLabels[mode]}</span>) : <span>待配置</span>}
      </span>
      {mailbox.auth.autoRefresh && (
        <span className="auto-refresh"><RefreshCw aria-hidden="true" /> 自动刷新</span>
      )}
    </div>
  )
}

function ExpiryDetails({ mailbox }: { mailbox: MailboxRecord }) {
  const forwarded = mailbox.auth.modes.length === 1 && mailbox.auth.modes[0] === 'forward'
  if (forwarded || mailbox.auth.refreshTokenValidity === 'not_applicable') return <div className="expiry-cell"><strong>不适用</strong><small>转发取件</small></div>
  const status = mailbox.auth.refreshStatus
  if (mailbox.auth.refreshTokenValidity === 'no_fixed_expiry') {
    return <div className="expiry-cell"><strong>无固定到期日</strong><small>{status === 'active' ? 'RT 已配置' : status === 'due' ? 'AT 待维护' : 'RT 状态已同步'}</small></div>
  }
  if (mailbox.auth.refreshTokenValidity === 'error') return <div className="expiry-cell"><strong className="text-danger">状态异常</strong><small>查看详情</small></div>
  if (mailbox.auth.refreshTokenValidity === 'missing') return <div className="expiry-cell"><strong className="text-danger">未配置 RT</strong><small>查看详情</small></div>
  return <div className="expiry-cell"><strong>尚未验证</strong><small>未提供固定到期日</small></div>
}

function ForwardingDetails({ mailbox }: { mailbox: MailboxRecord }) {
  if (mailbox.provider !== 'cloudflare') return <span className="muted-dash">不适用</span>
  if (!mailbox.forwarding) return <span className="muted-dash">待配置</span>
  return (
    <div className="forward-cell">
      <span>{mailbox.forwarding.target}</span>
      <small className={mailbox.forwarding.verified ? 'verified-text' : 'warning-text'}>
        {mailbox.forwarding.verified ? '已验证' : '待验证'}
      </small>
    </div>
  )
}

function RowActions({ mailbox, onCopyAddress, onOpenInbox, onExportMailbox, onOpenDetails }: { mailbox: MailboxRecord; onCopyAddress: (address: string) => void; onOpenInbox: (mailbox: MailboxRecord) => void; onExportMailbox: (mailbox: MailboxRecord) => void; onOpenDetails: (mailbox: MailboxRecord) => void }) {
  return (
    <div className="row-actions">
      <button className="icon-button icon-button--small tooltip-button" data-tooltip="收件箱" type="button" title="查看收件箱" aria-label={`查看 ${mailbox.address} 的收件箱`} onClick={() => onOpenInbox(mailbox)}><Mail /></button>
      {!mailbox.parentId && <button className="icon-button icon-button--small tooltip-button" data-tooltip="邮箱详情" type="button" title="邮箱详情" aria-label={`查看 ${mailbox.address} 的详情`} onClick={() => onOpenDetails(mailbox)}>
        <PanelRightOpen />
      </button>}
      <button className="icon-button icon-button--small tooltip-button" data-tooltip="导出邮箱" type="button" title="选择格式导出" aria-label={`导出 ${mailbox.address}`} onClick={() => onExportMailbox(mailbox)}><Download /></button>
      <button className="icon-button icon-button--small tooltip-button" data-tooltip="复制邮箱" type="button" title="复制邮箱地址" aria-label={`复制 ${mailbox.address}`} onClick={() => onCopyAddress(mailbox.address)}>
        <Copy />
      </button>
    </div>
  )
}

export function MailboxTable({
  mailboxes,
  expandedIds,
  selectedIds,
  onToggleExpanded,
  onToggleSelected,
  onToggleAll,
  onCopyAddress,
  onOpenInbox,
  onExportMailbox,
  onOpenDetails,
}: MailboxTableProps) {
  const visibleRows = mailboxes.flatMap((mailbox) => [
    { mailbox, isChild: false },
    ...(expandedIds.has(mailbox.id) ? mailbox.children.map((child) => ({ mailbox: child, isChild: true })) : []),
  ])
  const visibleIds = visibleRows.map(({ mailbox }) => mailbox.id)
  const allSelected = visibleIds.length > 0 && visibleIds.every((id) => selectedIds.has(id))

  if (mailboxes.length === 0) {
    return (
      <div className="empty-state">
        <span className="empty-state__icon"><KeyRound aria-hidden="true" /></span>
        <strong>没有匹配的邮箱</strong>
        <span>调整平台或搜索条件</span>
      </div>
    )
  }

  return (
    <>
      <div className="table-scroller">
        <table className="mailbox-table">
          <thead>
            <tr>
              <th className="check-column"><MailboxCheckbox checked={allSelected} label="选择当前全部邮箱" onChange={onToggleAll} /></th>
              <th>邮箱</th>
              <th>取件密钥</th>
              <th>取件方式</th>
              <th>RT 状态</th>
              <th>域名转发</th>
              <th>最近收件</th>
              <th className="actions-column"><span className="sr-only">操作</span></th>
            </tr>
          </thead>
          <tbody>
            {visibleRows.map(({ mailbox, isChild }) => {
              const hasChildren = mailbox.children.length > 0
              return (
                <tr key={mailbox.id} className={isChild ? 'mailbox-row mailbox-row--child' : 'mailbox-row'}>
                  <td className="check-column">
                    <MailboxCheckbox
                      checked={selectedIds.has(mailbox.id)}
                      label={`选择 ${mailbox.address}`}
                      onChange={() => onToggleSelected(mailbox.id)}
                    />
                  </td>
                  <td>
                    <div className="identity-with-toggle">
                      <button
                        className={`expand-button ${hasChildren ? '' : 'expand-button--empty'}`}
                        type="button"
                        onClick={() => hasChildren && onToggleExpanded(mailbox.id)}
                        aria-label={expandedIds.has(mailbox.id) ? `收起 ${mailbox.address}` : `展开 ${mailbox.address}`}
                        aria-expanded={hasChildren ? expandedIds.has(mailbox.id) : undefined}
                        tabIndex={hasChildren ? 0 : -1}
                      >
                        <ChevronRight aria-hidden="true" />
                      </button>
                      <MailboxIdentity mailbox={mailbox} isChild={isChild} onCopyAddress={onCopyAddress} />
                    </div>
                  </td>
                  <td><TokenDetails mailbox={mailbox} /></td>
                  <td><AuthDetails mailbox={mailbox} /></td>
                  <td><ExpiryDetails mailbox={mailbox} /></td>
                  <td><ForwardingDetails mailbox={mailbox} /></td>
                  <td><span className="last-mail-time">{formatDate(mailbox.lastMessageAt)}</span></td>
                  <td className="actions-column"><RowActions mailbox={mailbox} onCopyAddress={onCopyAddress} onOpenInbox={onOpenInbox} onExportMailbox={onExportMailbox} onOpenDetails={onOpenDetails} /></td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>

      <div className="mailbox-card-list">
        {visibleRows.map(({ mailbox, isChild }) => {
          const hasChildren = mailbox.children.length > 0
          return (
            <article className={`mailbox-card ${isChild ? 'mailbox-card--child' : ''}`} key={mailbox.id}>
              <div className="mailbox-card__head">
                <div className="identity-with-toggle">
                  <button
                    className={`expand-button ${hasChildren ? '' : 'expand-button--empty'}`}
                    type="button"
                    onClick={() => hasChildren && onToggleExpanded(mailbox.id)}
                    aria-label={expandedIds.has(mailbox.id) ? `收起 ${mailbox.address}` : `展开 ${mailbox.address}`}
                  >
                    <ChevronRight aria-hidden="true" />
                  </button>
                  <MailboxIdentity mailbox={mailbox} isChild={isChild} onCopyAddress={onCopyAddress} />
                </div>
                <MailboxCheckbox checked={selectedIds.has(mailbox.id)} label={`选择 ${mailbox.address}`} onChange={() => onToggleSelected(mailbox.id)} />
              </div>
              <div className="mailbox-card__status">
                <TokenDetails mailbox={mailbox} />
                <ExpiryDetails mailbox={mailbox} />
              </div>
              <dl className="mailbox-card__details">
                <div><dt>取件方式</dt><dd><AuthDetails mailbox={mailbox} /></dd></div>
                {mailbox.provider === 'cloudflare' && <div><dt>转发至</dt><dd><ForwardingDetails mailbox={mailbox} /></dd></div>}
                <div><dt>最近收件</dt><dd>{formatDate(mailbox.lastMessageAt)}</dd></div>
              </dl>
              <div className="mailbox-card__actions"><RowActions mailbox={mailbox} onCopyAddress={onCopyAddress} onOpenInbox={onOpenInbox} onExportMailbox={onExportMailbox} onOpenDetails={onOpenDetails} /></div>
            </article>
          )
        })}
      </div>
    </>
  )
}
