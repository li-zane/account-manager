import { Download, LoaderCircle, RefreshCw, Search, Trash2 } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'

import { apiClient } from '../api/client'
import type { MailboxRecord, ManagedCacheQuery, ManagedCacheResult, MessageFolder } from '../api/types'

interface MessageCacheSettingsProps {
  mailboxes: MailboxRecord[]
}

function iso(value: string): string | undefined {
  if (!value) return undefined
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? undefined : parsed.toISOString()
}

function localDate(value: string): string {
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short', hour12: false }).format(new Date(value))
}

export function MessageCacheSettings({ mailboxes }: MessageCacheSettingsProps) {
  const [mailboxId, setMailboxId] = useState('')
  const [folder, setFolder] = useState<'' | MessageFolder>('')
  const [after, setAfter] = useState('')
  const [before, setBefore] = useState('')
  const [query, setQuery] = useState('')
  const [result, setResult] = useState<ManagedCacheResult>({ messages: [], count: 0 })
  const [loading, setLoading] = useState(true)
  const [working, setWorking] = useState(false)
  const [error, setError] = useState<string>()
  const addresses = useMemo(() => new Map(mailboxes.map((mailbox) => [mailbox.id, mailbox.address])), [mailboxes])
  const filter = (): ManagedCacheQuery => ({ mailboxId: mailboxId || undefined, folder: folder || undefined, after: iso(after), before: iso(before), query: query.trim() || undefined, limit: 100 })
  const load = async () => {
    setLoading(true)
    setError(undefined)
    try { setResult(await apiClient.queryManagedCache(filter())) } catch (reason) { setError(reason instanceof Error ? reason.message : '缓存查询失败') } finally { setLoading(false) }
  }
  useEffect(() => { void load() }, [])
  const exportCache = async () => {
    setWorking(true); setError(undefined)
    try { await apiClient.exportManagedCache(filter()) } catch (reason) { setError(reason instanceof Error ? reason.message : '缓存导出失败') } finally { setWorking(false) }
  }
  const deleteCache = async () => {
    if (!after || !before) { setError('删除缓存需要明确选择开始和结束时间'); return }
    if (!window.confirm(`将删除当前时间范围内的 ${result.count} 封缓存邮件，微软或 Google 上游邮件不受影响。`)) return
    setWorking(true); setError(undefined)
    try { await apiClient.deleteManagedCache({ ...filter(), query: undefined, limit: 100000 }); await load() } catch (reason) { setError(reason instanceof Error ? reason.message : '缓存删除失败') } finally { setWorking(false) }
  }
	const restoreCache = async () => {
		if (!mailboxId || !after || !before) { setError('重新拉取需要选择一个邮箱以及开始、结束时间'); return }
		setWorking(true); setError(undefined)
		try {
			const folders: MessageFolder[] = folder ? [folder] : ['INBOX', 'Junk']
			for (const targetFolder of folders) await apiClient.restoreManagedCache(mailboxId, { after: iso(after), before: iso(before), folder: targetFolder })
			await load()
		} catch (reason) { setError(reason instanceof Error ? reason.message : '按时间范围重新拉取失败') } finally { setWorking(false) }
	}
  return <>
    <div className="section-heading"><div><p className="eyebrow">MESSAGE CACHE</p><h2>邮件缓存</h2></div><span className="cache-total">{result.count} 封</span></div>
    <div className="cache-manager__filters">
      <label><span>邮箱</span><select value={mailboxId} onChange={(event) => setMailboxId(event.target.value)}><option value="">全部邮箱</option>{mailboxes.map((mailbox) => <option key={mailbox.id} value={mailbox.id}>{mailbox.address}</option>)}</select></label>
      <label><span>文件夹</span><select value={folder} onChange={(event) => setFolder(event.target.value as '' | MessageFolder)}><option value="">全部文件夹</option><option value="INBOX">收件箱</option><option value="Junk">垃圾箱</option></select></label>
      <label><span>开始时间</span><input type="datetime-local" value={after} onChange={(event) => setAfter(event.target.value)} /></label>
      <label><span>结束时间</span><input type="datetime-local" value={before} onChange={(event) => setBefore(event.target.value)} /></label>
      <label className="cache-manager__search"><span>关键词</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="主题、发件人或正文" /></label>
      <button className="primary-button" type="button" onClick={() => void load()} disabled={loading || working}>{loading ? <LoaderCircle className="spin" /> : <Search />}查询</button>
    </div>
    <div className="cache-manager__actions"><button className="secondary-button" type="button" onClick={() => void restoreCache()} disabled={working}><RefreshCw />重新拉取时间范围</button><button className="secondary-button" type="button" onClick={() => void exportCache()} disabled={working || result.count === 0}><Download />导出当前结果</button><button className="danger-button" type="button" onClick={() => void deleteCache()} disabled={working || result.count === 0}><Trash2 />删除时间范围</button></div>
    {error && <div className="settings-sync-error" role="alert">{error}</div>}
    <div className="cache-manager__table" role="table" aria-label="缓存邮件查询结果">
      <div className="cache-manager__row cache-manager__row--head" role="row"><span>邮箱</span><span>主题</span><span>文件夹 / 协议</span><span>时间</span></div>
      {loading ? <div className="cache-manager__empty"><LoaderCircle className="spin" />正在查询缓存</div> : result.messages.length === 0 ? <div className="cache-manager__empty">当前条件下没有缓存邮件</div> : result.messages.map((message) => <div className="cache-manager__row" role="row" key={message.id}><span>{addresses.get(message.mailboxId) || message.mailboxId}</span><strong>{message.subject || '(无主题)'}</strong><span>{message.folder} · {message.retrievalMethod || '未知'}</span><time>{localDate(message.receivedAt)}</time></div>)}
    </div>
  </>
}
