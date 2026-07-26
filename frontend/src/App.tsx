import {
  ArchiveRestore,
  ArrowUpRight,
  Bell,
  Check,
  ChevronDown,
  CloudCog,
  Database,
  Download,
  HardDrive,
  KeyRound,
  LoaderCircle,
  Plus,
  RefreshCw,
  Search,
  Settings2,
  ShieldCheck,
  SlidersHorizontal,
  Sparkles,
  Upload,
  UserRound,
  X,
} from 'lucide-react'
import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react'

import { ApiClientError, apiClient } from './api/client'
import type { MailboxDashboard, MailboxImportResult, MailboxRecord, MailProvider, MessageProbeSettings, TokenRefreshSettings } from './api/types'
import { AppShell, type ViewKey, type WorkspacePlatform } from './components/AppShell'
import { BackupSettings } from './components/BackupSettings'
import { MailboxTable } from './components/MailboxTable'
import { MailboxDetailDrawer } from './components/MailboxDetailDrawer'
import { MailboxInboxDialog } from './components/MailboxInboxDialog'
import { MessageCacheSettings } from './components/MessageCacheSettings'
import { MailboxTransferDialog, type TransferMode } from './components/MailboxTransferDialog'
import { ProviderMark, providerMeta } from './components/ProviderMark'
import { ProviderConnectionsSettings } from './components/ProviderConnectionsSettings'

const providerFilters: Array<{ key: 'all' | MailProvider; label: string }> = [
  { key: 'all', label: '全部邮箱' },
  { key: 'microsoft', label: 'Microsoft' },
  { key: 'cloudflare', label: 'Cloudflare 域名' },
  { key: 'google', label: 'Google' },
]

const workspaceAccounts = [
  { id: 'gpt-112', platform: 'chatgpt' as const, email: 'studio.ops@outlook.com', plan: 'Team Owner', state: '订阅中', mailbox: 'microsoft_8f3c7a91' },
  { id: 'gpt-148', platform: 'chatgpt' as const, email: 'team.archive@gmail.com', plan: 'Plus', state: '可用', mailbox: 'google_c2879fd0' },
  { id: 'grok-038', platform: 'grok' as const, email: 'studio.ops+grok@outlook.com', plan: 'SuperGrok', state: '可用', mailbox: 'microsoft_704be123' },
  { id: 'grok-041', platform: 'grok' as const, email: 'verify@paperplane.dev', plan: 'Basic', state: '待检查', mailbox: 'cloudflare_93f31e4a' },
]

function countAll(mailboxes: MailboxRecord[]): number {
  return mailboxes.reduce((count, mailbox) => count + 1 + mailbox.children.length, 0)
}

function flattenIds(mailboxes: MailboxRecord[], expandedIds: Set<string>): string[] {
  return mailboxes.flatMap((mailbox) => [mailbox.id, ...(expandedIds.has(mailbox.id) ? mailbox.children.map((child) => child.id) : [])])
}

function formatRelative(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  const minutes = Math.max(0, Math.round((Date.now() - date.getTime()) / 60_000))
  if (minutes < 1) return '刚刚'
  if (minutes < 60) return `${minutes} 分钟前`
  if (minutes < 1_440) return `${Math.round(minutes / 60)} 小时前`
  return `${Math.round(minutes / 1_440)} 天前`
}

function Toggle({ checked, onChange, label, disabled = false }: { checked: boolean; onChange: () => void; label: string; disabled?: boolean }) {
  return (
    <button className={`toggle ${checked ? 'toggle--checked' : ''}`} type="button" role="switch" aria-checked={checked} aria-label={label} onClick={onChange} disabled={disabled}>
      <span />
    </button>
  )
}

function StatItem({ label, value, detail, tone }: { label: string; value: string | number; detail: string; tone: 'pink' | 'green' | 'yellow' | 'blue' }) {
  return (
    <div className="stat-item">
      <span className={`stat-item__dot stat-item__dot--${tone}`} />
      <span className="stat-item__copy"><small>{label}</small><strong>{value}</strong></span>
      <span className="stat-item__detail">{detail}</span>
    </div>
  )
}

function MailboxesView({
  dashboard,
  source,
  warning,
  loading,
  refreshing,
  filter,
  query,
  expandedIds,
  selectedIds,
  onFilterChange,
  onQueryChange,
  onToggleExpanded,
  onToggleSelected,
  onToggleAll,
  onCopyAddress,
  onOpenInbox,
  onExportMailbox,
  onOpenDetails,
  onRefresh,
  onAdd,
  onImport,
  onExport,
}: {
  dashboard: MailboxDashboard | null
  source: 'api' | 'mock'
  warning?: string
  loading: boolean
  refreshing: boolean
  filter: 'all' | MailProvider
  query: string
  expandedIds: Set<string>
  selectedIds: Set<string>
  onFilterChange: (filter: 'all' | MailProvider) => void
  onQueryChange: (query: string) => void
  onToggleExpanded: (id: string) => void
  onToggleSelected: (id: string) => void
  onToggleAll: () => void
  onCopyAddress: (address: string) => void
  onOpenInbox: (mailbox: MailboxRecord) => void
  onExportMailbox: (mailbox: MailboxRecord) => void
  onOpenDetails: (mailbox: MailboxRecord) => void
  onRefresh: () => void
  onAdd: () => void
  onImport: () => void
  onExport: () => void
  onRunBackup?: () => void
  backupRunning?: boolean
}) {
  const allMailboxes = dashboard?.mailboxes ?? []
  const filteredMailboxes = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase()
    return allMailboxes
      .map((mailbox) => {
        const rootProviderMatches = filter === 'all' || mailbox.provider === filter
        const providerChildren = filter === 'all' ? mailbox.children : mailbox.children.filter((child) => child.provider === filter)
        if (!rootProviderMatches && providerChildren.length === 0) return null
        const candidate = { ...mailbox, children: providerChildren }
        if (!normalizedQuery) return candidate
        const rootMatches = `${candidate.address} ${candidate.displayName ?? ''}`.toLowerCase().includes(normalizedQuery)
        const matchingChildren = candidate.children.filter((child) => `${child.address} ${child.displayName ?? ''}`.toLowerCase().includes(normalizedQuery))
        if (!rootMatches && matchingChildren.length === 0) return null
        return rootMatches ? candidate : { ...candidate, children: matchingChildren }
      })
      .filter((mailbox): mailbox is MailboxRecord => mailbox !== null)
  }, [allMailboxes, filter, query])

  const rootCount = allMailboxes.length
  const addressCount = countAll(allMailboxes)
  const aliasCount = addressCount - rootCount
  const healthyCount = allMailboxes.filter((mailbox) => mailbox.health === 'healthy').length
  const attentionCount = allMailboxes.filter((mailbox) => mailbox.health !== 'healthy' || mailbox.retrievalKey.status === 'missing' || mailbox.retrievalKey.status === 'expired' || mailbox.retrievalKey.status === 'expiring').length
  const filterCounts = useMemo(() => {
    const counts = {
      all: { primary: rootCount, aliases: aliasCount },
      microsoft: { primary: 0, aliases: 0 },
      cloudflare: { primary: 0, aliases: 0 },
      google: { primary: 0, aliases: 0 },
    }
    allMailboxes.forEach((mailbox) => {
      counts[mailbox.provider].primary += 1
      mailbox.children.forEach((child) => { counts[child.provider].aliases += 1 })
    })
    return counts
  }, [aliasCount, allMailboxes, rootCount])

  return (
    <div className="view view--mailboxes">
      <div className="page-heading">
        <div>
          <div className="heading-kicker"><span className={`source-dot source-dot--${source}`} />资源中心 <span className="slash">/</span> 邮箱</div>
          <h1>邮箱管理</h1>
          <p className="heading-meta">{rootCount} 个主邮箱 · {aliasCount} 个分裂/转发地址 · {dashboard ? `同步于 ${formatRelative(dashboard.updatedAt)}` : '正在同步'}</p>
        </div>
        <div className="page-actions">
          <button className="icon-button desktop-only" type="button" title="通知" aria-label="通知"><Bell /><span className="notification-dot" /></button>
          <button className="secondary-button" type="button" onClick={onImport}><Upload />导入</button>
          <button className="secondary-button" type="button" onClick={onExport}><Download />导出</button>
          <button className="primary-button" type="button" onClick={onAdd}><Plus />添加邮箱</button>
        </div>
      </div>

      {warning && <div className="mock-banner" role="status"><span><ArchiveRestore aria-hidden="true" />{source === 'mock' ? '当前显示本地预览数据' : `邮箱服务连接异常：${warning}`}</span><button type="button" onClick={onRefresh}><RefreshCw aria-hidden="true" />重试连接</button></div>}

      <div className="overview-strip" aria-label="邮箱概览">
        <StatItem label="邮箱总数" value={rootCount} detail={`${aliasCount} 个分裂/转发地址`} tone="pink" />
        <StatItem label="运行正常" value={healthyCount} detail={rootCount ? `${Math.round((healthyCount / rootCount) * 100)}% 主邮箱健康率` : '—'} tone="green" />
        <StatItem label="需要处理" value={attentionCount} detail={attentionCount ? '密钥或连接状态' : '全部稳定'} tone="yellow" />
        <StatItem label="自动刷新" value={allMailboxes.filter((mailbox) => mailbox.auth.autoRefresh).length} detail="个主邮箱已开启" tone="blue" />
      </div>

      <div className="mailbox-layout">
        <section className="mailbox-section" aria-labelledby="mailbox-list-title">
          <div className="section-heading">
            <div><p className="eyebrow">MAILBOX DIRECTORY</p><h2 id="mailbox-list-title">邮箱目录</h2></div>
            <div className="section-heading__right"><span className="live-indicator"><span />实时状态</span><button className="icon-button icon-button--small" type="button" title="刷新邮箱状态" aria-label="刷新邮箱状态" onClick={onRefresh} disabled={loading || refreshing}><RefreshCw className={loading || refreshing ? 'spin' : ''} /></button></div>
          </div>

          <div className="filter-toolbar">
            <div className="platform-tabs" role="tablist" aria-label="邮箱平台筛选">
              {providerFilters.map(({ key, label }) => (
                <button className={filter === key ? 'platform-tab platform-tab--active' : 'platform-tab'} type="button" role="tab" aria-selected={filter === key} key={key} onClick={() => onFilterChange(key)}>
                  {key !== 'all' && <ProviderMark provider={key} size="small" />}
                  <span>{label}</span><small>{filterCounts[key].primary > 0 ? `${filterCounts[key].primary} 主` : `${filterCounts[key].aliases} 地址`}</small>
                </button>
              ))}
            </div>
            <label className="search-box"><Search aria-hidden="true" /><input value={query} onChange={(event) => onQueryChange(event.target.value)} placeholder="搜索邮箱、备注或 ID" aria-label="搜索邮箱、备注或 ID" /><kbd>/</kbd></label>
          </div>

          {selectedIds.size > 0 && <div className="bulk-bar"><span>已选择 <strong>{selectedIds.size}</strong> 个地址</span><button type="button" onClick={onExport}><Download />导出</button><button className="icon-button icon-button--tiny" type="button" title="清除选择" aria-label="清除选择" onClick={onToggleAll}><X /></button></div>}

          <div className={`mailbox-list ${loading ? 'mailbox-list--loading' : ''}`}>
            {loading && !dashboard ? <LoadingTable /> : <MailboxTable mailboxes={filteredMailboxes} expandedIds={expandedIds} selectedIds={selectedIds} onToggleExpanded={onToggleExpanded} onToggleSelected={onToggleSelected} onToggleAll={onToggleAll} onCopyAddress={onCopyAddress} onOpenInbox={onOpenInbox} onExportMailbox={onExportMailbox} onOpenDetails={onOpenDetails} />}
          </div>
          {!loading && filteredMailboxes.length > 0 && <div className="list-footer"><span>显示 {filteredMailboxes.length} 个主邮箱{query ? `，匹配“${query}”` : ''}</span><span>已展开的分裂邮箱会跟随主邮箱导出</span></div>}
        </section>

      </div>
    </div>
  )
}

function LoadingTable() {
  return <div className="loading-table" aria-label="正在加载邮箱"><LoaderCircle className="spin" /><span>正在同步邮箱目录…</span></div>
}

function WorkspaceView({ platform, onPlatformChange }: { platform: WorkspacePlatform; onPlatformChange: (platform: WorkspacePlatform) => void }) {
  const rows = workspaceAccounts.filter((account) => account.platform === platform)
  return (
    <div className="view view--workspace">
      <div className="page-heading">
        <div><div className="heading-kicker"><span className="source-dot source-dot--api" />业务空间 <span className="slash">/</span> 账号</div><h1>工作区</h1><p className="heading-meta">17 个账号 · 2 个平台</p></div>
        <div className="page-actions"><button className="secondary-button" type="button"><SlidersHorizontal />工作区设置</button><button className="primary-button" type="button"><Plus />添加账号</button></div>
      </div>
      <div className="workspace-layout">
        <section className="workspace-section">
          <div className="workspace-switcher" role="tablist" aria-label="工作区平台">
            <button className={platform === 'chatgpt' ? 'workspace-tab workspace-tab--active' : 'workspace-tab'} type="button" role="tab" aria-selected={platform === 'chatgpt'} onClick={() => onPlatformChange('chatgpt')}><span className="workspace-icon workspace-icon--chatgpt"><Sparkles /></span><span><strong>ChatGPT</strong><small>12 个账号</small></span><ChevronDown /></button>
            <button className={platform === 'grok' ? 'workspace-tab workspace-tab--active' : 'workspace-tab'} type="button" role="tab" aria-selected={platform === 'grok'} onClick={() => onPlatformChange('grok')}><span className="workspace-icon workspace-icon--grok"><UserRound /></span><span><strong>Grok</strong><small>5 个账号</small></span><ChevronDown /></button>
          </div>
          <div className="section-heading section-heading--workspace"><div><p className="eyebrow">{platform === 'chatgpt' ? 'CHATGPT WORKSPACE' : 'GROK WORKSPACE'}</p><h2>账号列表</h2></div><span className="live-indicator"><span />同步正常</span></div>
          <div className="account-table">
            <div className="account-table__head"><span>账号</span><span>订阅方案</span><span>状态</span><span>来源邮箱</span><span /></div>
            {rows.map((row) => <div className="account-table__row" key={row.id}><div className="account-table__identity"><span className={`account-platform-mark account-platform-mark--${row.platform}`}>{row.platform === 'chatgpt' ? <Sparkles /> : <UserRound />}</span><span><strong>{row.email}</strong><small>{row.id}</small></span></div><span className="plan-chip">{row.plan}</span><span className={`account-state account-state--${row.state === '订阅中' || row.state === '可用' ? 'good' : 'attention'}`}><span />{row.state}</span><button className="source-link" type="button" title="查看来源邮箱">{row.mailbox}<ArrowUpRight /></button><button className="icon-button icon-button--small" type="button" title="更多操作" aria-label={`${row.email} 更多操作`}><Settings2 /></button></div>)}
          </div>
        </section>
        <aside className="workspace-note"><span className="section-icon section-icon--lilac"><CloudCog /></span><p className="eyebrow">WORKSPACE POLICY</p><h2>邮箱与账号分开管理</h2><p className="workspace-note__copy">账号通过邮箱 ID 路由。一个邮箱可以承载多个平台账号，也可以继续挂载分裂邮箱。</p><div className="workspace-note__line"><ShieldCheck />售出状态独立传播</div><div className="workspace-note__line"><KeyRound />平台密钥不暴露原始令牌</div></aside>
      </div>
    </div>
  )
}

function SettingsView({ onServerChange, mailboxes }: { onServerChange: () => void; mailboxes: MailboxRecord[] }) {
  const [initialDraft] = useState(() => {
    const defaults = { compactTable: true, reduceMotion: false, retrievalMode: 'dual' }
    try {
      const saved = JSON.parse(window.localStorage.getItem('account-manager.settings-draft.v1') ?? '{}') as Partial<typeof defaults>
      return { ...defaults, ...saved }
    } catch {
      return defaults
    }
  })
  const [tokenRefreshSettings, setTokenRefreshSettings] = useState<TokenRefreshSettings>({ enabled: true, leadTimeMinutes: 5, version: 0 })
  const [messageProbeSettings, setMessageProbeSettings] = useState<MessageProbeSettings>({ enabled: false, intervalMinutes: 10, version: 0 })
  const [autoRefresh, setAutoRefresh] = useState(true)
  const [refreshLeadMinutes, setRefreshLeadMinutes] = useState(5)
  const [autoProbe, setAutoProbe] = useState(false)
  const [probeIntervalMinutes, setProbeIntervalMinutes] = useState(10)
  const [tokenRefreshLoading, setTokenRefreshLoading] = useState(true)
  const [tokenRefreshSaving, setTokenRefreshSaving] = useState(false)
  const [tokenRefreshDirty, setTokenRefreshDirty] = useState(false)
  const [messageProbeDirty, setMessageProbeDirty] = useState(false)
  const [tokenRefreshError, setTokenRefreshError] = useState<string>()
  const [compactTable, setCompactTable] = useState(initialDraft.compactTable)
  const [reduceMotion, setReduceMotion] = useState(initialDraft.reduceMotion)
  const [retrievalMode, setRetrievalMode] = useState(initialDraft.retrievalMode)
  const [activeSection, setActiveSection] = useState<'mail' | 'cache' | 'connections' | 'backup' | 'interface'>('mail')
  const [draftSaved, setDraftSaved] = useState(true)
  useEffect(() => {
    document.documentElement.dataset.reduceMotion = String(reduceMotion)
    document.documentElement.dataset.compactTable = String(compactTable)
  }, [compactTable, reduceMotion])
  useEffect(() => {
    const controller = new AbortController()
    setTokenRefreshLoading(true)
    void apiClient.getTokenRefreshSettings(controller.signal)
      .then((settings) => {
        setTokenRefreshSettings(settings)
        setAutoRefresh(settings.enabled)
        setRefreshLeadMinutes(settings.leadTimeMinutes)
        setTokenRefreshDirty(false)
        setTokenRefreshError(undefined)
      })
      .catch((error) => {
        if (!controller.signal.aborted) setTokenRefreshError(error instanceof Error ? error.message : '令牌刷新设置读取失败')
      })
      .finally(() => {
        if (!controller.signal.aborted) setTokenRefreshLoading(false)
      })
    return () => controller.abort()
  }, [])
  useEffect(() => {
    const controller = new AbortController()
    void apiClient.getMessageProbeSettings(controller.signal)
      .then((settings) => {
        setMessageProbeSettings(settings)
        setAutoProbe(settings.enabled)
        setProbeIntervalMinutes(settings.intervalMinutes)
        setMessageProbeDirty(false)
      })
      .catch((error) => {
        if (!controller.signal.aborted) setTokenRefreshError(error instanceof Error ? error.message : '邮件探测设置读取失败')
      })
    return () => controller.abort()
  }, [])
  const update = <T,>(setter: (value: T | ((previous: T) => T)) => void, value: T | ((previous: T) => T)) => {
    setter(value)
    setDraftSaved(false)
  }
  const settingsDraft = { compactTable, reduceMotion, retrievalMode }
  const saveDraft = async () => {
    window.localStorage.setItem('account-manager.settings-draft.v1', JSON.stringify(settingsDraft))
    setDraftSaved(true)
    if (!tokenRefreshDirty && !messageProbeDirty) return
    setTokenRefreshSaving(true)
    setTokenRefreshError(undefined)
    try {
      if (tokenRefreshDirty) {
        const saved = await apiClient.saveTokenRefreshSettings({ enabled: autoRefresh, leadTimeMinutes: refreshLeadMinutes, version: tokenRefreshSettings.version })
        setTokenRefreshSettings(saved)
        setAutoRefresh(saved.enabled)
        setRefreshLeadMinutes(saved.leadTimeMinutes)
        setTokenRefreshDirty(false)
      }
      if (messageProbeDirty) {
        const saved = await apiClient.saveMessageProbeSettings({ enabled: autoProbe, intervalMinutes: probeIntervalMinutes, version: messageProbeSettings.version })
        setMessageProbeSettings(saved)
        setAutoProbe(saved.enabled)
        setProbeIntervalMinutes(saved.intervalMinutes)
        setMessageProbeDirty(false)
      }
      onServerChange()
    } catch (error) {
      if (error instanceof ApiClientError && error.status === 409) {
        setTokenRefreshError('设置已被其他请求更新，请重新进入此页面后再保存')
      } else {
        setTokenRefreshError(error instanceof Error ? error.message : '令牌刷新设置保存失败')
      }
    } finally {
      setTokenRefreshSaving(false)
    }
  }
  const exportDraft = () => {
    const exportedSettings = { ...settingsDraft, tokenRefresh: { enabled: autoRefresh, leadTimeMinutes: refreshLeadMinutes }, messageProbe: { enabled: autoProbe, intervalMinutes: probeIntervalMinutes } }
    const url = URL.createObjectURL(new Blob([JSON.stringify(exportedSettings, null, 2)], { type: 'application/json' }))
    const link = document.createElement('a')
    link.href = url
    link.download = 'account-manager-settings-draft.json'
    link.click()
    URL.revokeObjectURL(url)
  }
  const updateTokenRefresh = (updateValue: () => void) => {
    updateValue()
    setTokenRefreshDirty(true)
    setTokenRefreshError(undefined)
  }
  const updateMessageProbe = (updateValue: () => void) => {
    updateValue()
    setMessageProbeDirty(true)
    setTokenRefreshError(undefined)
  }
  const settingsSaved = draftSaved && !tokenRefreshDirty && !messageProbeDirty
  const statusLabel = tokenRefreshLoading
    ? '正在读取服务器设置'
    : tokenRefreshSaving
      ? '正在保存设置'
      : settingsSaved
        ? '设置已保存'
        : '有未保存设置'
  const draftStatus = <span className="settings-saved">{tokenRefreshLoading || tokenRefreshSaving ? <LoaderCircle className="spin" /> : <Check />}{statusLabel}</span>
  return (
    <div className="view view--settings">
      <div className="page-heading"><div><div className="heading-kicker"><span className="source-dot source-dot--api" />系统设置</div><h1>设置</h1><p className="heading-meta">偏好与连接配置</p></div>{(activeSection === 'mail' || activeSection === 'interface') && <div className="page-actions"><button className="secondary-button" type="button" onClick={exportDraft}><ArchiveRestore />导出设置</button><button className="primary-button" type="button" onClick={() => void saveDraft()} disabled={tokenRefreshSaving}><Check />保存设置</button></div>}</div>
      <div className="settings-layout">
        <nav className="settings-nav" aria-label="设置分类">
          <button className={activeSection === 'mail' ? 'settings-nav__item settings-nav__item--active' : 'settings-nav__item'} type="button" onClick={() => setActiveSection('mail')}><KeyRound />邮箱取件<span>5</span></button>
          <button className={activeSection === 'cache' ? 'settings-nav__item settings-nav__item--active' : 'settings-nav__item'} type="button" onClick={() => setActiveSection('cache')}><HardDrive />邮件缓存</button>
          <button className={activeSection === 'connections' ? 'settings-nav__item settings-nav__item--active' : 'settings-nav__item'} type="button" onClick={() => setActiveSection('connections')}><CloudCog />服务连接</button>
          <button className={activeSection === 'backup' ? 'settings-nav__item settings-nav__item--active' : 'settings-nav__item'} type="button" onClick={() => setActiveSection('backup')}><Database />数据备份</button>
          <button className={activeSection === 'interface' ? 'settings-nav__item settings-nav__item--active' : 'settings-nav__item'} type="button" onClick={() => setActiveSection('interface')}><SlidersHorizontal />界面与导航</button>
        </nav>
        <section className="settings-section">
          {activeSection === 'mail' && <><div className="section-heading"><div><p className="eyebrow">MAIL ACCESS</p><h2>邮箱取件</h2></div>{draftStatus}</div><div className="settings-group"><div className="setting-row"><div><strong>自动维护 OAuth 令牌</strong><small>当前关闭时，手动与自动取件均不会刷新 RT</small></div><Toggle checked={autoRefresh} onChange={() => updateTokenRefresh(() => setAutoRefresh((value) => !value))} label="自动维护 OAuth 令牌" disabled={tokenRefreshLoading || tokenRefreshSaving} /></div><div className="setting-row"><div><strong>自动探测新邮件</strong><small>按间隔增量拉取收件箱与垃圾箱</small></div><Toggle checked={autoProbe} onChange={() => updateMessageProbe(() => setAutoProbe((value) => !value))} label="自动探测新邮件" disabled={tokenRefreshSaving} /></div><div className="setting-row"><div><strong>探测间隔</strong><small>后台缓存增量同步周期</small></div><label className="number-control"><input type="number" value={probeIntervalMinutes} min={1} max={1440} disabled={tokenRefreshSaving} onChange={(event) => updateMessageProbe(() => setProbeIntervalMinutes(Math.min(1440, Math.max(1, Number(event.target.value) || 1))))} /><span>分钟</span></label></div><div className="setting-row"><div><strong>默认取件能力</strong><small>Microsoft 共享 RT 的首选通道</small></div><label className="select-control"><select value={retrievalMode} aria-label="默认取件能力" onChange={(event) => update(setRetrievalMode, event.target.value)}><option value="dual">Graph + IMAP</option><option value="graph">Graph</option><option value="imap">IMAP</option></select><ChevronDown /></label></div><div className="setting-row"><div><strong>刷新提前时间</strong><small>仅用于短期访问令牌，不代表 RT 到期日</small></div><label className="number-control"><input type="number" value={refreshLeadMinutes} min={1} max={30} disabled={tokenRefreshLoading || tokenRefreshSaving} onChange={(event) => updateTokenRefresh(() => setRefreshLeadMinutes(Math.min(30, Math.max(1, Number(event.target.value) || 1))))} /><span>分钟</span></label></div></div>{tokenRefreshError && <div className="settings-sync-error" role="alert">{tokenRefreshError}</div>}</>}
          {activeSection === 'connections' && <ProviderConnectionsSettings />}
          {activeSection === 'cache' && <MessageCacheSettings mailboxes={mailboxes} />}
          {activeSection === 'backup' && <BackupSettings onServerChange={onServerChange} />}
          {activeSection === 'interface' && <><div className="section-heading"><div><p className="eyebrow">NAVIGATION</p><h2>界面与导航</h2></div>{draftStatus}</div><div className="settings-group"><div className="setting-row"><div><strong>固定工作区</strong><small>显示在左侧导航中</small></div><button className="select-control" type="button">ChatGPT、Grok <ChevronDown /></button></div><div className="setting-row"><div><strong>紧凑表格</strong><small>在宽屏中显示更多邮箱字段</small></div><Toggle checked={compactTable} onChange={() => update(setCompactTable, (value) => !value)} label="紧凑表格" /></div><div className="setting-row"><div><strong>减少动效</strong><small>适用于需要降低动态效果的环境</small></div><Toggle checked={reduceMotion} onChange={() => update(setReduceMotion, (value) => !value)} label="减少动效" /></div></div></>}
        </section>
      </div>
    </div>
  )
}

function AddMailboxDialog({ open, submitting, managedMailboxes, onClose, onSubmit }: { open: boolean; submitting: boolean; managedMailboxes: MailboxRecord[]; onClose: () => void; onSubmit: (provider: MailProvider, address: string, forwardingMailboxId: string) => Promise<void> }) {
  const [provider, setProvider] = useState<MailProvider>('microsoft')
  const [address, setAddress] = useState('')
  const [forwardingMailboxId, setForwardingMailboxId] = useState('')
  useEffect(() => { if (open) { setAddress(''); setForwardingMailboxId(managedMailboxes[0]?.id ?? ''); setProvider('microsoft') } }, [open])
  if (!open) return null
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!address.trim() || submitting) return
    await onSubmit(provider, address.trim(), forwardingMailboxId)
  }
  return (
    <div className="modal-layer" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && !submitting && onClose()}>
      <form className="modal" onSubmit={submit} role="dialog" aria-modal="true" aria-label="添加邮箱">
        <div className="modal__header"><div><p className="eyebrow">NEW MAILBOX</p><h2>添加邮箱</h2></div><button className="icon-button" type="button" title="关闭" aria-label="关闭" onClick={onClose} disabled={submitting}><X /></button></div>
        <div className="provider-picker" role="radiogroup" aria-label="选择邮箱平台">
          {(['microsoft', 'cloudflare', 'google'] as MailProvider[]).map((item) => <button className={provider === item ? `provider-option provider-option--${item} provider-option--active` : `provider-option provider-option--${item}`} type="button" role="radio" aria-checked={provider === item} key={item} onClick={() => setProvider(item)}><ProviderMark provider={item} size="medium" /><span>{providerMeta[item].label}</span>{provider === item && <Check />}</button>)}
        </div>
        <label className="field-label">邮箱地址<input autoFocus value={address} onChange={(event) => setAddress(event.target.value)} placeholder={provider === 'cloudflare' ? 'verify@example.com' : 'name@example.com'} type="email" required /></label>
        {provider === 'cloudflare' && <div className="field-label">转发到<label className="select-control select-control--wide"><select value={forwardingMailboxId} onChange={(event) => setForwardingMailboxId(event.target.value)} required disabled={managedMailboxes.length === 0} aria-label="Cloudflare 转发邮箱">{managedMailboxes.length === 0 && <option value="">暂无可用目标邮箱</option>}{managedMailboxes.map((mailbox) => <option value={mailbox.id} key={mailbox.id}>{mailbox.address}</option>)}</select><ChevronDown /></label></div>}
        <div className="modal-hint"><KeyRound />保存后自动签发本站取件密钥；上游令牌仅在明确选择敏感格式时导出。</div>
        <div className="modal__actions"><button className="secondary-button" type="button" onClick={onClose} disabled={submitting}>取消</button><button className="primary-button" type="submit" disabled={submitting || (provider === 'cloudflare' && !forwardingMailboxId)}>{submitting ? <LoaderCircle className="spin" /> : <Plus />}{submitting ? '正在添加' : '添加邮箱'}</button></div>
      </form>
    </div>
  )
}

export default function App() {
  const [dashboard, setDashboard] = useState<MailboxDashboard | null>(null)
  const [source, setSource] = useState<'api' | 'mock'>('api')
  const [warning, setWarning] = useState<string>()
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [filter, setFilter] = useState<'all' | MailProvider>('all')
  const [query, setQuery] = useState('')
  const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set())
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set())
  const [addingMailbox, setAddingMailbox] = useState(false)
  const [detailMailbox, setDetailMailbox] = useState<MailboxRecord>()
  const [inboxMailbox, setInboxMailbox] = useState<MailboxRecord>()
  const [transferMode, setTransferMode] = useState<TransferMode>()
  const [view, setView] = useState<ViewKey>(() => {
    const hash = window.location.hash.replace('#', '')
    return hash === 'workspace' || hash === 'settings' ? hash : 'mailboxes'
  })
  const [workspacePlatform, setWorkspacePlatform] = useState<WorkspacePlatform>('chatgpt')
  const [backupRunning, setBackupRunning] = useState(false)
  const [addOpen, setAddOpen] = useState(false)
  const [toast, setToast] = useState<string>()

  const loadDashboard = useCallback(async (signal?: AbortSignal) => {
    const result = await apiClient.getMailboxDashboard(signal)
    setDashboard(result.data)
    setSource(result.source)
    setWarning(result.warning)
    setExpandedIds((current) => current.size > 0 ? current : new Set(result.data.mailboxes.filter((mailbox) => mailbox.children.length > 0).slice(0, 2).map((mailbox) => mailbox.id)))
    setLoading(false)
    setRefreshing(false)
  }, [])

  const refreshDashboardFromSettings = useCallback(() => {
    void loadDashboard()
  }, [loadDashboard])

  useEffect(() => {
    const controller = new AbortController()
    void loadDashboard(controller.signal).catch((error) => {
      if (!(error instanceof DOMException && error.name === 'AbortError')) {
        setWarning(error instanceof Error ? error.message : '同步失败')
        setLoading(false)
        setRefreshing(false)
      }
    })
    return () => controller.abort()
  }, [loadDashboard])

  useEffect(() => {
    window.history.replaceState(null, '', `#${view}`)
    window.scrollTo({ top: 0, left: 0, behavior: 'auto' })
  }, [view])

  useEffect(() => {
    const handleHashChange = () => {
      const hash = window.location.hash.replace('#', '')
      setView(hash === 'workspace' || hash === 'settings' ? hash : 'mailboxes')
    }
    window.addEventListener('hashchange', handleHashChange)
    return () => window.removeEventListener('hashchange', handleHashChange)
  }, [])

  const overlayOpen = addOpen || Boolean(detailMailbox) || Boolean(inboxMailbox) || Boolean(transferMode)
  useEffect(() => {
    if (!overlayOpen) return
    const previousOverflow = document.body.style.overflow
    const previousPaddingRight = document.body.style.paddingRight
    const scrollbarWidth = window.innerWidth - document.documentElement.clientWidth
    document.body.style.overflow = 'hidden'
    if (scrollbarWidth > 0) document.body.style.paddingRight = `${scrollbarWidth}px`
    return () => {
      document.body.style.overflow = previousOverflow
      document.body.style.paddingRight = previousPaddingRight
    }
  }, [overlayOpen])

  useEffect(() => {
    if (!toast) return
    const timer = window.setTimeout(() => setToast(undefined), 3_600)
    return () => window.clearTimeout(timer)
  }, [toast])

  const handleRefresh = () => {
    setRefreshing(true)
    void loadDashboard().catch(() => setRefreshing(false))
  }

  const handleToggleExpanded = (id: string) => {
    setExpandedIds((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const handleToggleSelected = (id: string) => {
    setSelectedIds((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const handleToggleAll = () => {
    if (!dashboard) return
    const ids = flattenIds(dashboard.mailboxes.filter((mailbox) => filter === 'all' || mailbox.provider === filter), expandedIds)
    setSelectedIds((current) => ids.every((id) => current.has(id)) ? new Set() : new Set(ids))
  }

  const handleCopyAddress = async (address: string) => {
    try { await navigator.clipboard?.writeText(address) } catch { /* clipboard permission is optional */ }
    setToast(`已复制 ${address}`)
  }

  const handleRunBackup = async () => {
    setBackupRunning(true)
    if (source === 'mock') {
      await new Promise((resolve) => window.setTimeout(resolve, 900))
      setDashboard((current) => current ? { ...current, backup: { ...current.backup, lastCompletedAt: new Date().toISOString() } } : current)
      setBackupRunning(false)
      setToast('本地备份演示已完成')
      return
    }
    try {
      await apiClient.runBackup()
      setToast('备份任务已加入队列')
    } catch (error) {
      setToast(error instanceof Error ? error.message : '备份任务提交失败')
    } finally { setBackupRunning(false) }
  }

  const handleAddMailbox = async (provider: MailProvider, address: string, forwardingMailboxId: string) => {
    setAddingMailbox(true)
    if (source === 'api') {
      try {
        await apiClient.createMailbox(provider, address, forwardingMailboxId)
        await loadDashboard()
        setAddOpen(false)
        setToast(provider === 'cloudflare' ? '转发地址已保存，供应商同步待执行' : '邮箱已保存')
      } catch (error) {
        setToast(error instanceof Error ? error.message : '邮箱保存失败')
      } finally {
        setAddingMailbox(false)
      }
      return
    }
    const id = `${provider}_${Date.now().toString(36)}`
    if (provider === 'cloudflare') {
      setDashboard((current) => {
        if (!current) return current
        const destination = current.mailboxes.find((mailbox) => mailbox.id === forwardingMailboxId)
        if (!destination) return current
        const alias: MailboxRecord = {
          id,
          parentId: destination.id,
          kind: 'split',
          provider,
          address,
          displayName: 'Cloudflare 转发地址',
          health: 'attention',
          retrievalKey: destination.retrievalKey,
          auth: { modes: ['forward'], autoRefresh: false },
          forwarding: { target: destination.address, verified: false },
          children: [],
        }
        return { ...current, mailboxes: current.mailboxes.map((mailbox) => mailbox.id === destination.id ? { ...mailbox, children: [...mailbox.children, alias] } : mailbox) }
      })
      setAddOpen(false)
      setToast('转发地址已加入本地目录')
      setAddingMailbox(false)
      return
    }
    const mailbox: MailboxRecord = {
      id,
      kind: 'primary',
      provider,
      address,
      displayName: '新建邮箱',
      health: 'attention',
      retrievalKey: { status: 'ready', maskedKey: 'am_pk_preview...' },
      auth: { modes: provider === 'google' ? ['oauth'] : ['graph'], autoRefresh: false },
      children: [],
    }
    setDashboard((current) => current ? { ...current, mailboxes: [mailbox, ...current.mailboxes] } : current)
    setAddOpen(false)
    setToast('邮箱已加入本地目录，等待完成连接配置')
    setAddingMailbox(false)
  }

  const handleExportMailbox = (mailbox: MailboxRecord) => {
    setSelectedIds(new Set([mailbox.parentId || mailbox.id]))
    setTransferMode('export')
  }

  const handleImported = (result: MailboxImportResult) => {
    setToast(`导入完成：新增 ${result.importedCount}，更新 ${result.updatedCount}，跳过 ${result.skippedCount}`)
    if (source === 'api') void loadDashboard()
  }

  const openWorkspace = (platform: WorkspacePlatform) => {
    setWorkspacePlatform(platform)
    setView('workspace')
  }

  return (
    <AppShell currentView={view} onViewChange={setView} onOpenWorkspace={openWorkspace} source={source}>
      {view === 'mailboxes' && <MailboxesView dashboard={dashboard} source={source} warning={warning} loading={loading} refreshing={refreshing} filter={filter} query={query} expandedIds={expandedIds} selectedIds={selectedIds} onFilterChange={setFilter} onQueryChange={setQuery} onToggleExpanded={handleToggleExpanded} onToggleSelected={handleToggleSelected} onToggleAll={handleToggleAll} onCopyAddress={handleCopyAddress} onOpenInbox={setInboxMailbox} onExportMailbox={handleExportMailbox} onOpenDetails={setDetailMailbox} onRefresh={handleRefresh} onAdd={() => setAddOpen(true)} onImport={() => setTransferMode('import')} onExport={() => setTransferMode('export')} onRunBackup={handleRunBackup} backupRunning={backupRunning} />}
      {view === 'workspace' && <WorkspaceView platform={workspacePlatform} onPlatformChange={setWorkspacePlatform} />}
      {view === 'settings' && <SettingsView onServerChange={refreshDashboardFromSettings} mailboxes={dashboard?.mailboxes ?? []} />}
      <AddMailboxDialog open={addOpen} submitting={addingMailbox} managedMailboxes={(dashboard?.mailboxes ?? []).filter((mailbox) => mailbox.provider !== 'cloudflare')} onClose={() => setAddOpen(false)} onSubmit={handleAddMailbox} />
      {detailMailbox && <MailboxDetailDrawer mailbox={detailMailbox} onClose={() => setDetailMailbox(undefined)} />}
      {inboxMailbox && <MailboxInboxDialog mailbox={inboxMailbox} onClose={() => setInboxMailbox(undefined)} />}
      <MailboxTransferDialog open={Boolean(transferMode)} initialMode={transferMode ?? 'import'} mailboxes={dashboard?.mailboxes ?? []} selectedIds={selectedIds} source={source} onClose={() => setTransferMode(undefined)} onImported={handleImported} />
      {toast && <div className="toast" role="status"><Check aria-hidden="true" /><span>{toast}</span><button className="icon-button icon-button--tiny" type="button" title="关闭提示" aria-label="关闭提示" onClick={() => setToast(undefined)}><X /></button></div>}
    </AppShell>
  )
}
