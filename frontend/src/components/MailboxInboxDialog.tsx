import { AlertCircle, Check, Clock3, Copy, Inbox, KeyRound, LoaderCircle, Mail, RefreshCw, Search, ShieldAlert, X } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'

import { apiClient } from '../api/client'
import type { CachedMessage, CachedMessagesResult, MailboxRecord, MessageFolder } from '../api/types'
import { ProviderMark } from './ProviderMark'

interface MailboxInboxDialogProps {
  mailbox: MailboxRecord
  onClose: () => void
}

interface CopyableMetaProps {
  label: string
  value: string
  copyValue?: string
  copyKey: string
  copied?: string
  onCopy: (value: string, key: string) => Promise<void>
}

function formatDate(value?: string, full = false): string {
  if (!value) return '尚未同步'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '尚未同步'
  return new Intl.DateTimeFormat('zh-CN', full
    ? { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }
    : { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(date)
}

function plainBody(message: CachedMessage): string {
  if (message.text?.trim()) return message.text.trim()
  if (!message.html) return '此邮件没有可显示的正文。'
  const parsed = new DOMParser().parseFromString(message.html, 'text/html')
  return parsed.body.textContent?.trim() || '此邮件没有可显示的正文。'
}

function emailDocument(html: string): string {
  const parsed = new DOMParser().parseFromString(html, 'text/html')
  parsed.querySelectorAll('script, object, embed, form, input, button').forEach((node) => node.remove())
  parsed.querySelectorAll('*').forEach((node) => {
    for (const attribute of [...node.attributes]) if (attribute.name.toLowerCase().startsWith('on')) node.removeAttribute(attribute.name)
  })
  parsed.querySelectorAll('a').forEach((node) => { node.target = '_blank'; node.rel = 'noreferrer noopener' })
  parsed.querySelectorAll('img').forEach((node) => { node.loading = 'lazy'; node.style.maxWidth = '100%'; node.style.height = 'auto' })
  const style = parsed.createElement('style')
  style.textContent = `:root{color-scheme:light}html,body{margin:0;padding:0;background:#fff;color:#302a31;font:15px/1.65 system-ui,sans-serif}body{padding:24px;overflow-wrap:anywhere}table{max-width:100%!important}a{color:#2563a8}pre{white-space:pre-wrap}img{max-width:100%;height:auto}`
  parsed.head.append(style)
  return '<!doctype html>' + parsed.documentElement.outerHTML
}

function verificationCodes(message: CachedMessage): string[] {
  const source = `${message.subject}\n${plainBody(message)}`
    .replace(/https?:\/\/\S+/gi, ' ')
    .replace(/\b[0-9a-f]{8}-[0-9a-f-]{27,}\b/gi, ' ')
    .replace(/\b(\d{3})[ -](\d{3})\b/g, '$1$2')
  const keyword = '(?:验证码|校验码|动态码|安全码|登录码|一次性密码|検証コード|認証コード|確認コード|ログインコード|verification\\s*code|security\\s*code|login\\s*code|one[- ]?time\\s*(?:code|password)|otp|passcode)'
  const code = '\\b((?=[A-Z0-9]{4,8}\\b)(?=[A-Z0-9]*\\d)[A-Z0-9]{4,8})\\b'
  const patterns = [
    new RegExp(`${keyword}[\\s\\S]{0,80}?${code}`, 'gi'),
    new RegExp(`${code}[\\s\\S]{0,48}?${keyword}`, 'gi'),
  ]
  const codes: string[] = []
  const append = (raw: string) => {
    const value = raw.toUpperCase()
    if (/^(?:19|20)\d{2}$/.test(value) || codes.includes(value)) return
    codes.push(value)
  }
  for (const pattern of patterns) {
    for (const match of source.matchAll(pattern)) {
      append(match[1])
      if (codes.length === 3) return codes
    }
  }
  if (new RegExp(keyword, 'i').test(source)) {
    for (const match of source.matchAll(/\b(\d{6})\b/g)) {
      append(match[1])
      if (codes.length === 3) break
    }
  }
  return codes
}

function methodLabel(method?: string): string {
  if (method === 'microsoft_graph') return 'Microsoft Graph'
  if (method === 'imap_oauth') return 'IMAP OAuth2'
  if (method === 'gmail_api') return 'Gmail API'
  if (method === 'imap_password') return 'IMAP'
  return '未知协议'
}

function addressForCopy(value: string): string {
  return value.match(/[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}/i)?.[0] ?? value.trim()
}

function CopyableMeta({ label, value, copyValue, copyKey, copied, onCopy }: CopyableMetaProps) {
  return <div className="message-meta-row">
    <dt>{label}</dt>
    <dd>
      <button type="button" className="message-copy-value" onClick={() => void onCopy(copyValue ?? value, copyKey)} title={`复制${label}`}>
        <span>{value}</span>{copied === copyKey ? <Check /> : <Copy />}
      </button>
    </dd>
  </div>
}

export function MailboxInboxDialog({ mailbox, onClose }: MailboxInboxDialogProps) {
  const [folder, setFolder] = useState<MessageFolder>('INBOX')
  const [result, setResult] = useState<CachedMessagesResult>({ messages: [], count: 0, newCount: 0, complete: true })
  const [selectedId, setSelectedId] = useState<string>()
  const [query, setQuery] = useState('')
  const [loading, setLoading] = useState(true)
  const [syncing, setSyncing] = useState(false)
  const [error, setError] = useState<string>()
  const [copied, setCopied] = useState<string>()

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true); setError(undefined)
    void apiClient.getCachedMessages(mailbox, folder, controller.signal)
      .then((value) => {
        setResult(value)
        setSelectedId((current) => value.messages.some((message) => message.id === current) ? current : undefined)
      })
      .catch((reason) => { if (!(reason instanceof DOMException && reason.name === 'AbortError')) setError(reason instanceof Error ? reason.message : '邮件缓存读取失败') })
      .finally(() => setLoading(false))
    return () => controller.abort()
  }, [folder, mailbox])

  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => { if (event.key === 'Escape') onClose() }
    window.addEventListener('keydown', closeOnEscape)
    return () => window.removeEventListener('keydown', closeOnEscape)
  }, [onClose])

  const visibleMessages = useMemo(() => {
    const normalized = query.trim().toLowerCase()
    if (!normalized) return result.messages
    return result.messages.filter((message) => [message.from, message.subject, message.to.join(' '), message.cc.join(' '), plainBody(message)].some((value) => value.toLowerCase().includes(normalized)))
  }, [query, result.messages])
  const selected = useMemo(() => visibleMessages.find((message) => message.id === selectedId), [selectedId, visibleMessages])
  const codes = useMemo(() => selected ? verificationCodes(selected) : [], [selected])

  const copy = async (value: string, key: string) => {
    await navigator.clipboard.writeText(value)
    setCopied(key)
    window.setTimeout(() => setCopied((current) => current === key ? undefined : current), 1500)
  }

  const openMessage = (message: CachedMessage) => {
    setSelectedId(message.id)
    if (!message.unread || message.viewedAt) return
    const viewedAt = new Date().toISOString()
    setResult((current) => ({ ...current, messages: current.messages.map((item) => item.id === message.id ? { ...item, viewedAt } : item) }))
    const mailboxId = mailbox.parentId ?? mailbox.id
    void apiClient.markCachedMessageViewed(mailboxId, message.id).catch((reason) => {
      setResult((current) => ({ ...current, messages: current.messages.map((item) => item.id === message.id && item.viewedAt === viewedAt ? { ...item, viewedAt: undefined } : item) }))
      setError(reason instanceof Error ? reason.message : '邮件已读状态保存失败')
    })
  }

  const sync = async () => {
    setSyncing(true); setError(undefined)
    try {
      const value = await apiClient.syncCachedMessages(mailbox, folder)
      setResult(value)
      setSelectedId((current) => value.messages.some((message) => message.id === current) ? current : undefined)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '邮件同步失败')
    } finally {
      setSyncing(false)
    }
  }

  return <div className="modal-layer inbox-layer" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
    <section className="inbox-dialog" role="dialog" aria-modal="true" aria-labelledby="mailbox-inbox-title">
      <header className="inbox-dialog__header">
        <div className="inbox-dialog__identity">
          <ProviderMark provider={mailbox.provider} size="medium" />
          <span><small>MAILBOX</small><button type="button" className="inbox-address-copy" onClick={() => void copy(mailbox.address, 'mailbox')}><h2 id="mailbox-inbox-title">{mailbox.address}</h2>{copied === 'mailbox' ? <Check /> : <Copy />}</button></span>
        </div>
        <div className="inbox-dialog__tools">
          <div className="inbox-folder-switch" role="tablist" aria-label="邮件文件夹">
            <button type="button" role="tab" aria-selected={folder === 'INBOX'} className={folder === 'INBOX' ? 'inbox-folder-switch__item inbox-folder-switch__item--active' : 'inbox-folder-switch__item'} onClick={() => setFolder('INBOX')}><Inbox />收件箱</button>
            <button type="button" role="tab" aria-selected={folder === 'Junk'} className={folder === 'Junk' ? 'inbox-folder-switch__item inbox-folder-switch__item--active' : 'inbox-folder-switch__item'} onClick={() => setFolder('Junk')}><ShieldAlert />垃圾箱</button>
          </div>
          <button className="icon-button" type="button" title="拉取新邮件" aria-label="拉取新邮件" onClick={() => void sync()} disabled={syncing}><RefreshCw className={syncing ? 'spin' : ''} /></button>
          <button className="icon-button" type="button" title="关闭收件箱" aria-label="关闭收件箱" onClick={onClose}><X /></button>
        </div>
      </header>

      <div className="inbox-dialog__content">
        <aside className="inbox-sidebar" aria-label={folder === 'INBOX' ? '收件箱邮件' : '垃圾箱邮件'}>
          <div className="inbox-list-toolbar">
            <label className="inbox-search"><Search aria-hidden="true" /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索发件人、主题或正文" aria-label="搜索缓存邮件" />{query && <button type="button" title="清除搜索" aria-label="清除搜索" onClick={() => setQuery('')}><X /></button>}</label>
            <div className="inbox-list-status">
              <span>{query ? `${visibleMessages.length}/${result.count} 封匹配` : `${result.count} 封邮件`}</span>
              <span>{result.sync ? `同步于 ${formatDate(result.sync.lastSyncedAt)}` : '本地缓存'}</span>
              {result.newCount > 0 && <strong>新增 {result.newCount}</strong>}
            </div>
            {(error || result.sync?.lastError) && <div className="inbox-list-error"><AlertCircle />{error || result.sync?.lastError}</div>}
          </div>
          <div className="inbox-message-list">
            {loading ? <div className="inbox-empty"><LoaderCircle className="spin" />读取本地缓存</div> : visibleMessages.length === 0 ? <div className="inbox-empty"><Mail />{query ? '没有匹配邮件' : '暂无缓存邮件'}</div> : visibleMessages.map((message) => {
              const messageCodes = verificationCodes(message)
              const isUnread = message.unread && !message.viewedAt
              return <article className={selected?.id === message.id ? 'inbox-message inbox-message--active' : 'inbox-message'} key={message.id}>
                <button className="inbox-message__open" type="button" onClick={() => openMessage(message)} aria-label={`查看邮件：${message.subject || '无主题'}`}>
                  <span className="inbox-message__top"><strong>{message.from || '未知发件人'}</strong><time>{formatDate(message.receivedAt)}</time></span>
                  <span className="inbox-message__subject">{message.subject || '(无主题)'}</span>
                  <span className="inbox-message__preview">{plainBody(message).slice(0, 110)}</span>
                </button>
                <footer className="inbox-message__footer">
                  <span className="protocol-badge protocol-badge--compact">{methodLabel(message.retrievalMethod)}</span>
                  {messageCodes[0] && <button type="button" className="inbox-message__code" onClick={() => void copy(messageCodes[0], `list-code:${message.id}`)} title="复制验证码"><KeyRound /><span>{messageCodes[0]}</span>{copied === `list-code:${message.id}` ? <Check /> : <Copy />}</button>}
                </footer>
                {isUnread && <span className="inbox-message__unread" aria-label="未读" />}
              </article>
            })}
          </div>
        </aside>

        <article className="inbox-message-view">
          {selected ? <>
            <header className="message-reading-head">
              <h3>{selected.subject || '(无主题)'}</h3>
              <div className="message-reading-head__meta">
                <time className="message-reading-time" dateTime={selected.receivedAt}><Clock3 aria-hidden="true" /><span>收件时间</span><strong>{formatDate(selected.receivedAt, true)}</strong></time>
                <span className="protocol-badge">{methodLabel(selected.retrievalMethod)}</span>
              </div>
              {codes.length > 0 && <div className="verification-codes" aria-label="邮件验证码">{codes.map((code) => <button type="button" key={code} onClick={() => void copy(code, `code:${code}`)} title="复制验证码"><KeyRound /><span>验证码</span><strong>{code}</strong>{copied === `code:${code}` ? <Check /> : <Copy />}</button>)}</div>}
              <dl>
                <CopyableMeta label="发件人" value={selected.from || '未知发件人'} copyValue={addressForCopy(selected.from || '')} copyKey="sender" copied={copied} onCopy={copy} />
                <CopyableMeta label="收件人" value={selected.to.join(', ') || mailbox.address} copyValue={(selected.to.length ? selected.to : [mailbox.address]).join(', ')} copyKey="recipients" copied={copied} onCopy={copy} />
                {selected.cc.length > 0 && <CopyableMeta label="抄送" value={selected.cc.join(', ')} copyKey="cc" copied={copied} onCopy={copy} />}
              </dl>
            </header>
            <div className="message-reading-body">{selected.html ? <iframe title={`邮件正文：${selected.subject}`} sandbox="allow-popups allow-popups-to-escape-sandbox" srcDoc={emailDocument(selected.html)} /> : <pre>{plainBody(selected)}</pre>}</div>
          </> : <div className="inbox-empty inbox-empty--viewer"><Mail /><strong>选择一封邮件</strong><span>查看完整标题、验证码和邮件正文</span></div>}
        </article>
      </div>
    </section>
  </div>
}
