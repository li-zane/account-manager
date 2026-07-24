import { AlertCircle, Inbox, LoaderCircle, Mail, RefreshCw, Search, ShieldAlert, X } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'

import { apiClient } from '../api/client'
import type { CachedMessage, CachedMessagesResult, MailboxRecord, MessageFolder } from '../api/types'
import { ProviderMark } from './ProviderMark'

interface MailboxInboxDialogProps {
  mailbox: MailboxRecord
  onClose: () => void
}

function formatDate(value?: string): string {
  if (!value) return '尚未同步'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '尚未同步'
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false,
  }).format(date)
}

function messageBody(message: CachedMessage): string {
  if (message.text?.trim()) return message.text
  if (!message.html) return '此邮件没有可显示的正文。'
  const documentValue = new DOMParser().parseFromString(message.html, 'text/html')
  return documentValue.body.textContent?.trim() || '此邮件没有可显示的正文。'
}

export function MailboxInboxDialog({ mailbox, onClose }: MailboxInboxDialogProps) {
  const [folder, setFolder] = useState<MessageFolder>('INBOX')
  const [result, setResult] = useState<CachedMessagesResult>({ messages: [], count: 0, newCount: 0 })
  const [selectedId, setSelectedId] = useState<string>()
  const [query, setQuery] = useState('')
  const [retrievalMethod, setRetrievalMethod] = useState<'auto' | 'microsoft_graph' | 'imap_oauth'>('auto')
  const [loading, setLoading] = useState(true)
  const [syncing, setSyncing] = useState(false)
  const [error, setError] = useState<string>()

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError(undefined)
    void apiClient.getCachedMessages(mailbox, folder, controller.signal)
      .then((value) => {
        setResult(value)
        setSelectedId((current) => value.messages.some((message) => message.id === current) ? current : value.messages[0]?.id)
      })
      .catch((reason) => {
        if (!(reason instanceof DOMException && reason.name === 'AbortError')) setError(reason instanceof Error ? reason.message : '邮件缓存读取失败')
      })
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
    return result.messages.filter((message) => [
      message.from,
      message.subject,
      message.to.join(' '),
      message.cc.join(' '),
      messageBody(message),
    ].some((value) => value.toLowerCase().includes(normalized)))
  }, [query, result.messages])
  const selected = useMemo(() => visibleMessages.find((message) => message.id === selectedId) ?? visibleMessages[0], [selectedId, visibleMessages])

  const sync = async () => {
    setSyncing(true)
    setError(undefined)
    try {
      const value = await apiClient.syncCachedMessages(mailbox, folder, retrievalMethod === 'auto' ? undefined : retrievalMethod)
      setResult(value)
      setSelectedId((current) => value.messages.some((message) => message.id === current) ? current : value.messages[0]?.id)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '邮件同步失败')
    } finally {
      setSyncing(false)
    }
  }

  return (
    <div className="drawer-layer inbox-layer" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <section className="inbox-dialog" role="dialog" aria-modal="true" aria-labelledby="mailbox-inbox-title">
        <header className="inbox-dialog__header">
          <div className="inbox-dialog__identity"><ProviderMark provider={mailbox.provider} size="medium" /><span><small>MAILBOX</small><h2 id="mailbox-inbox-title">{mailbox.address}</h2></span></div>
          <div className="inbox-dialog__tools">
            {mailbox.provider === 'microsoft' && <div className="inbox-method-switch" role="group" aria-label="Outlook 探测通道">
              <button type="button" className={retrievalMethod === 'auto' ? 'inbox-method-switch__item inbox-method-switch__item--active' : 'inbox-method-switch__item'} onClick={() => setRetrievalMethod('auto')}>自动</button>
              <button type="button" className={retrievalMethod === 'microsoft_graph' ? 'inbox-method-switch__item inbox-method-switch__item--active' : 'inbox-method-switch__item'} onClick={() => setRetrievalMethod('microsoft_graph')}>Graph</button>
              <button type="button" className={retrievalMethod === 'imap_oauth' ? 'inbox-method-switch__item inbox-method-switch__item--active' : 'inbox-method-switch__item'} onClick={() => setRetrievalMethod('imap_oauth')}>IMAP</button>
            </div>}
            <div className="inbox-folder-switch" role="tablist" aria-label="邮件文件夹">
              <button type="button" role="tab" aria-selected={folder === 'INBOX'} className={folder === 'INBOX' ? 'inbox-folder-switch__item inbox-folder-switch__item--active' : 'inbox-folder-switch__item'} onClick={() => setFolder('INBOX')}><Inbox />收件箱</button>
              <button type="button" role="tab" aria-selected={folder === 'Junk'} className={folder === 'Junk' ? 'inbox-folder-switch__item inbox-folder-switch__item--active' : 'inbox-folder-switch__item'} onClick={() => setFolder('Junk')}><ShieldAlert />垃圾箱</button>
            </div>
            <button className="icon-button" type="button" title="拉取新邮件" aria-label="拉取新邮件" onClick={() => void sync()} disabled={syncing}><RefreshCw className={syncing ? 'spin' : ''} /></button>
            <button className="icon-button" type="button" title="关闭收件箱" aria-label="关闭收件箱" onClick={onClose}><X /></button>
          </div>
        </header>

        <div className="inbox-sync-strip">
          <span>{result.sync ? `上次探测 ${formatDate(result.sync.lastSyncedAt)}` : '当前显示本地缓存'}</span>
          <span>{query ? `${visibleMessages.length}/${result.count} 封匹配` : `${result.count} 封缓存`}</span>
          {result.newCount > 0 && <strong>新增 {result.newCount} 封</strong>}
          {result.sync?.lastError && <span className="inbox-sync-strip__error"><AlertCircle />{result.sync.lastError}</span>}
        </div>
        <div className="inbox-search-area">
          {error && <div className="inline-alert inline-alert--error"><AlertCircle />{error}</div>}
          <label className="inbox-search"><Search aria-hidden="true" /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索发件人、主题、收件人或正文" aria-label="搜索缓存邮件" />{query && <button type="button" title="清除搜索" aria-label="清除搜索" onClick={() => setQuery('')}><X /></button>}</label>
        </div>

        <div className="inbox-dialog__content">
          <aside className="inbox-message-list" aria-label={folder === 'INBOX' ? '收件箱邮件' : '垃圾箱邮件'}>
            {loading ? <div className="inbox-empty"><LoaderCircle className="spin" />读取本地缓存</div> : visibleMessages.length === 0 ? <div className="inbox-empty"><Mail />{query ? '没有匹配邮件' : '暂无缓存邮件'}</div> : visibleMessages.map((message) => (
              <button className={selected?.id === message.id ? 'inbox-message inbox-message--active' : 'inbox-message'} type="button" key={message.id} onClick={() => setSelectedId(message.id)}>
                <span className="inbox-message__top"><strong>{message.from || '未知发件人'}</strong><time>{formatDate(message.receivedAt)}</time></span>
                <span className="inbox-message__subject">{message.subject || '(无主题)'}</span>
                <span className="inbox-message__preview">{messageBody(message).slice(0, 90)}</span>
                {message.unread && <span className="inbox-message__unread" aria-label="未读" />}
              </button>
            ))}
          </aside>

          <article className="inbox-message-view">
            {selected ? <><header><p>{selected.from || '未知发件人'}</p><h3>{selected.subject || '(无主题)'}</h3><dl><div><dt>收件人</dt><dd>{selected.to.join(', ') || mailbox.address}</dd></div>{selected.cc.length > 0 && <div><dt>抄送</dt><dd>{selected.cc.join(', ')}</dd></div>}<div><dt>时间</dt><dd>{formatDate(selected.receivedAt)}</dd></div></dl></header><pre>{messageBody(selected)}</pre></> : <div className="inbox-empty inbox-empty--viewer"><Mail />选择一封邮件查看正文</div>}
          </article>
        </div>
      </section>
    </div>
  )
}
