import {
  ArchiveRestore,
  CalendarClock,
  Check,
  CheckCircle2,
  CircleAlert,
  Clock3,
  Cloud,
  DatabaseBackup,
  Eye,
  EyeOff,
  HardDrive,
  History,
  LoaderCircle,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Save,
  Server,
  ShieldAlert,
  X,
} from 'lucide-react'
import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react'
import { createPortal } from 'react-dom'

import { ApiClientError, apiClient } from '../api/client'
import type {
  BackupProvider,
  BackupRestoreOperation,
  BackupRun,
  BackupRunState,
  BackupTarget,
  BackupTargetConfig,
  S3BackupConfig,
  WebDAVBackupConfig,
} from '../api/types'

type SchedulePreset = 'manual' | 'daily' | 'weekly' | 'six-hours' | 'custom'
type WebDAVAuthMode = 'none' | 'basic' | 'bearer'

interface TargetDraft {
  name: string
  kind: BackupProvider
  enabled: boolean
  schedulePreset: SchedulePreset
  customSchedule: string
  retentionCount: number
  replaceConfig: boolean
  endpoint: string
  region: string
  bucket: string
  prefix: string
  accessKeyId: string
  secretAccessKey: string
  sessionToken: string
  usePathStyle: boolean
  baseUrl: string
  webdavPrefix: string
  webdavAuthMode: WebDAVAuthMode
  username: string
  password: string
  bearerToken: string
  insecureSkipVerify: boolean
}

const scheduleValues: Record<Exclude<SchedulePreset, 'manual' | 'custom'>, string> = {
  daily: '15 2 * * *',
  weekly: '15 2 * * 0',
  'six-hours': '0 */6 * * *',
}

const runLabels: Record<BackupRunState, string> = {
  pending: '等待执行',
  running: '正在备份',
  succeeded: '已完成',
  failed: '失败',
}

function newTargetDraft(): TargetDraft {
  return {
    name: '',
    kind: 's3',
    enabled: true,
    schedulePreset: 'daily',
    customSchedule: scheduleValues.daily,
    retentionCount: 14,
    replaceConfig: true,
    endpoint: '',
    region: 'us-east-1',
    bucket: '',
    prefix: 'account-manager',
    accessKeyId: '',
    secretAccessKey: '',
    sessionToken: '',
    usePathStyle: false,
    baseUrl: '',
    webdavPrefix: 'account-manager',
    webdavAuthMode: 'basic',
    username: '',
    password: '',
    bearerToken: '',
    insecureSkipVerify: false,
  }
}

function scheduleDraft(value: string): Pick<TargetDraft, 'schedulePreset' | 'customSchedule'> {
  const normalized = value.trim().toLowerCase()
  if (!normalized) return { schedulePreset: 'manual', customSchedule: scheduleValues.daily }
  if (normalized === 'daily' || normalized === scheduleValues.daily) return { schedulePreset: 'daily', customSchedule: scheduleValues.daily }
  if (normalized === 'weekly' || normalized === scheduleValues.weekly) return { schedulePreset: 'weekly', customSchedule: scheduleValues.weekly }
  if (normalized === 'six-hours' || normalized === 'six_hours' || normalized === scheduleValues['six-hours']) {
    return { schedulePreset: 'six-hours', customSchedule: scheduleValues['six-hours'] }
  }
  return { schedulePreset: 'custom', customSchedule: value }
}

function draftForTarget(target: BackupTarget): TargetDraft {
  const draft: TargetDraft = {
    ...newTargetDraft(),
    name: target.name,
    kind: target.kind,
    enabled: target.enabled,
    retentionCount: target.retentionCount,
    replaceConfig: false,
    ...scheduleDraft(target.schedule),
  }
  if (target.config?.kind === 's3') {
    draft.endpoint = target.config.endpoint ?? ''
    draft.region = target.config.region ?? 'us-east-1'
    draft.bucket = target.config.bucket
    draft.prefix = target.config.prefix ?? ''
    draft.usePathStyle = target.config.usePathStyle
  }
  if (target.config?.kind === 'webdav') {
    draft.baseUrl = target.config.baseUrl
    draft.webdavPrefix = target.config.prefix ?? ''
    draft.webdavAuthMode = target.config.authentication
    draft.insecureSkipVerify = target.config.insecureSkipVerify
  }
  return draft
}

function formatDateTime(value?: string): string {
  if (!value) return '尚无记录'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '尚无记录'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  }).format(date)
}

function formatBytes(value: number): string {
  if (value <= 0) return '-'
  if (value < 1_000) return `${value} B`
  if (value < 1_000_000) return `${(value / 1_000).toFixed(1)} KB`
  if (value < 1_000_000_000) return `${(value / 1_000_000).toFixed(1)} MB`
  return `${(value / 1_000_000_000).toFixed(2)} GB`
}

function formatSchedule(value: string): string {
  const { schedulePreset } = scheduleDraft(value)
  if (schedulePreset === 'manual') return '仅手动'
  if (schedulePreset === 'daily') return '每天 02:15'
  if (schedulePreset === 'weekly') return '每周日 02:15'
  if (schedulePreset === 'six-hours') return '每 6 小时'
  return value
}

function targetLocation(target: BackupTarget): string {
  if (target.config?.kind === 's3') return [target.config.bucket, target.config.prefix].filter(Boolean).join(' / ')
  if (target.config?.kind === 'webdav') return target.config.baseUrl
  return target.kind === 's3' ? 'S3 兼容存储' : 'WebDAV 存储'
}

function backupErrorMessage(error: unknown): string {
  if (error instanceof ApiClientError && error.status === 401) return '管理员凭据缺失或已失效'
  if (error instanceof ApiClientError && error.status === 404) return '当前服务版本缺少对应的备份端点'
  if (error instanceof ApiClientError && error.status === 409) return '任务或配置发生冲突，请刷新后重试'
  return error instanceof Error ? error.message : '备份请求失败'
}

function RunState({ state }: { state: BackupRunState }) {
  const Icon = state === 'succeeded' ? CheckCircle2 : state === 'failed' ? CircleAlert : state === 'running' ? LoaderCircle : Clock3
  return (
    <span className={`backup-run-state backup-run-state--${state}`}>
      <Icon className={state === 'running' ? 'spin' : ''} aria-hidden="true" />
      {runLabels[state]}
    </span>
  )
}

function TargetIcon({ kind }: { kind: BackupProvider }) {
  const Icon = kind === 's3' ? Cloud : HardDrive
  return <span className={`backup-target-icon backup-target-icon--${kind}`}><Icon aria-hidden="true" /></span>
}

function useOverlayLock(open: boolean) {
  useEffect(() => {
    if (!open) return
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => { document.body.style.overflow = previousOverflow }
  }, [open])
}

export function BackupSettings({ onServerChange }: { onServerChange?: () => void }) {
  const [targets, setTargets] = useState<BackupTarget[]>([])
  const [runs, setRuns] = useState<BackupRun[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [loadError, setLoadError] = useState<string>()
  const [actionError, setActionError] = useState<string>()
  const [queueingTargetId, setQueueingTargetId] = useState<string>()
  const [editingTarget, setEditingTarget] = useState<BackupTarget>()
  const [targetDialogOpen, setTargetDialogOpen] = useState(false)
  const [targetDraft, setTargetDraft] = useState<TargetDraft>(newTargetDraft)
  const [targetSaving, setTargetSaving] = useState(false)
  const [targetError, setTargetError] = useState<string>()
  const [secretsVisible, setSecretsVisible] = useState(false)
  const [historyTarget, setHistoryTarget] = useState('all')
  const [historyLimit, setHistoryLimit] = useState(10)
  const [restoreRun, setRestoreRun] = useState<BackupRun>()
  const [restoreConfirmed, setRestoreConfirmed] = useState(false)
  const [restoreSubmitting, setRestoreSubmitting] = useState(false)
  const [restoreOperation, setRestoreOperation] = useState<BackupRestoreOperation>()
  const [restoreError, setRestoreError] = useState<string>()

  const loadData = useCallback(async (signal?: AbortSignal, announce = false) => {
    if (announce) setRefreshing(true)
    try {
      const [nextTargets, nextRuns] = await Promise.all([
        apiClient.getBackupTargets(signal),
        apiClient.getBackupRuns(undefined, signal),
      ])
      if (signal?.aborted) return
      setTargets(nextTargets)
      setRuns(nextRuns)
      setLoadError(undefined)
    } catch (error) {
      if (!signal?.aborted) setLoadError(backupErrorMessage(error))
    } finally {
      if (!signal?.aborted) {
        setLoading(false)
        if (announce) setRefreshing(false)
      }
    }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    void loadData(controller.signal)
    return () => controller.abort()
  }, [loadData])

  const hasActiveRun = runs.some((run) => run.state === 'pending' || run.state === 'running')
  useEffect(() => {
    if (!hasActiveRun) return
    const timer = window.setInterval(() => void loadData(), 2_500)
    return () => window.clearInterval(timer)
  }, [hasActiveRun, loadData])

  useOverlayLock(targetDialogOpen || Boolean(restoreRun))

  const targetById = useMemo(() => new Map(targets.map((target) => [target.id, target])), [targets])
  const latestRunByTarget = useMemo(() => {
    const result = new Map<string, BackupRun>()
    for (const run of runs) {
      const current = result.get(run.targetId)
      if (!current || (run.createdAt ?? '') > (current.createdAt ?? '')) result.set(run.targetId, run)
    }
    return result
  }, [runs])
  const latestSuccess = useMemo(() => runs
    .filter((run) => run.state === 'succeeded')
    .sort((left, right) => (right.finishedAt ?? right.createdAt ?? '').localeCompare(left.finishedAt ?? left.createdAt ?? ''))[0], [runs])
  const visibleRuns = useMemo(() => runs
    .filter((run) => historyTarget === 'all' || run.targetId === historyTarget)
    .slice(0, historyLimit), [historyLimit, historyTarget, runs])

  const openCreateTarget = () => {
    setEditingTarget(undefined)
    setTargetDraft(newTargetDraft())
    setSecretsVisible(false)
    setTargetError(undefined)
    setTargetDialogOpen(true)
  }

  const openEditTarget = (target: BackupTarget) => {
    setEditingTarget(target)
    setTargetDraft(draftForTarget(target))
    setSecretsVisible(false)
    setTargetError(undefined)
    setTargetDialogOpen(true)
  }

  const closeTargetDialog = () => {
    if (targetSaving) return
    setTargetDialogOpen(false)
    setEditingTarget(undefined)
    setTargetError(undefined)
  }

  const draftSchedule = targetDraft.schedulePreset === 'manual'
    ? ''
    : targetDraft.schedulePreset === 'custom'
      ? targetDraft.customSchedule.trim()
      : scheduleValues[targetDraft.schedulePreset]

  const createConfig = (): BackupTargetConfig | undefined => {
    if (editingTarget && !targetDraft.replaceConfig) return undefined
    if (targetDraft.kind === 'webdav') {
      const config: WebDAVBackupConfig = {
        baseUrl: targetDraft.baseUrl,
        prefix: targetDraft.webdavPrefix,
        insecureSkipVerify: targetDraft.insecureSkipVerify,
      }
      if (targetDraft.webdavAuthMode === 'basic') {
        config.username = targetDraft.username
        config.password = targetDraft.password
      }
      if (targetDraft.webdavAuthMode === 'bearer') config.bearerToken = targetDraft.bearerToken
      return config
    }
    const config: S3BackupConfig = {
      endpoint: targetDraft.endpoint,
      region: targetDraft.region,
      bucket: targetDraft.bucket,
      prefix: targetDraft.prefix,
      accessKeyId: targetDraft.accessKeyId,
      secretAccessKey: targetDraft.secretAccessKey,
      sessionToken: targetDraft.sessionToken,
      usePathStyle: targetDraft.usePathStyle,
    }
    return config
  }

  const validateTarget = (): string | undefined => {
    if (!targetDraft.name.trim()) return '请填写目标名称'
    if (targetDraft.retentionCount < 1 || targetDraft.retentionCount > 365) return '保留份数需在 1 到 365 之间'
    if (targetDraft.schedulePreset === 'custom' && !targetDraft.customSchedule.trim()) return '请填写 Cron 表达式'
    if (editingTarget && !targetDraft.replaceConfig) return undefined
    if (targetDraft.kind === 's3') {
      if (!targetDraft.bucket.trim()) return '请填写 S3 Bucket'
      if (Boolean(targetDraft.accessKeyId.trim()) !== Boolean(targetDraft.secretAccessKey)) return 'Access Key ID 与 Secret Access Key 需要同时填写'
      if (targetDraft.sessionToken && !targetDraft.accessKeyId.trim()) return 'Session Token 需要配合静态访问密钥使用'
      return undefined
    }
    if (!targetDraft.baseUrl.trim()) return '请填写 WebDAV 地址'
    if (targetDraft.webdavAuthMode === 'basic' && (!targetDraft.username.trim() || !targetDraft.password)) return '请填写 WebDAV 用户名和密码'
    if (targetDraft.webdavAuthMode === 'bearer' && !targetDraft.bearerToken) return '请填写 Bearer Token'
    return undefined
  }

  const saveTarget = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (targetSaving) return
    const validationError = validateTarget()
    if (validationError) {
      setTargetError(validationError)
      return
    }
    setTargetSaving(true)
    setTargetError(undefined)
    try {
      const config = createConfig()
      if (editingTarget) {
        await apiClient.updateBackupTarget(editingTarget.id, {
          name: targetDraft.name,
          kind: editingTarget.kind,
          ...(config ? { config } : {}),
          enabled: targetDraft.enabled,
          schedule: draftSchedule,
          retentionCount: targetDraft.retentionCount,
          metadata: editingTarget.metadata,
          version: editingTarget.version,
        })
      } else {
        if (!config) throw new Error('备份配置缺失')
        await apiClient.createBackupTarget({
          name: targetDraft.name,
          kind: targetDraft.kind,
          config,
          enabled: targetDraft.enabled,
          schedule: draftSchedule,
          retentionCount: targetDraft.retentionCount,
        })
      }
      setTargetDialogOpen(false)
      setEditingTarget(undefined)
      await loadData()
      onServerChange?.()
    } catch (error) {
      setTargetError(backupErrorMessage(error))
    } finally {
      setTargetSaving(false)
    }
  }

  const queueRun = async (target: BackupTarget) => {
    if (queueingTargetId) return
    setQueueingTargetId(target.id)
    setActionError(undefined)
    try {
      const run = await apiClient.queueBackupRun(target.id)
      setRuns((current) => [run, ...current.filter((item) => item.id !== run.id)])
      onServerChange?.()
    } catch (error) {
      setActionError(backupErrorMessage(error))
    } finally {
      setQueueingTargetId(undefined)
    }
  }

  const selectRestore = (run: BackupRun) => {
    if (run.state !== 'succeeded' || !run.objectKey) return
    setRestoreRun(run)
    setRestoreConfirmed(false)
    setRestoreSubmitting(false)
    setRestoreOperation(undefined)
    setRestoreError(undefined)
  }

  const restoreActive = restoreOperation?.state === 'pending' || restoreOperation?.state === 'running'
  const closeRestore = () => {
    if (restoreSubmitting || restoreActive) return
    setRestoreRun(undefined)
    setRestoreOperation(undefined)
    setRestoreError(undefined)
    setRestoreConfirmed(false)
  }

  const startRestore = async () => {
    if (!restoreRun || !restoreConfirmed || restoreSubmitting) return
    setRestoreSubmitting(true)
    setRestoreError(undefined)
    try {
      const operation = await apiClient.restoreBackupRun(restoreRun.id)
      setRestoreOperation(operation)
    } catch (error) {
      setRestoreError(backupErrorMessage(error))
    } finally {
      setRestoreSubmitting(false)
    }
  }

  useEffect(() => {
    if (!restoreOperation || (restoreOperation.state !== 'pending' && restoreOperation.state !== 'running')) return
    const controller = new AbortController()
    const timer = window.setInterval(() => {
      void apiClient.getBackupRestore(restoreOperation.id, controller.signal)
        .then((operation) => {
          setRestoreOperation(operation)
          if (operation.state === 'succeeded' || operation.state === 'failed') {
            void loadData()
            onServerChange?.()
          }
        })
        .catch((error) => {
          if (!controller.signal.aborted) setRestoreError(backupErrorMessage(error))
        })
    }, 1_500)
    return () => {
      controller.abort()
      window.clearInterval(timer)
    }
  }, [loadData, onServerChange, restoreOperation])

  const activeRunCount = runs.filter((run) => run.state === 'pending' || run.state === 'running').length
  const filteredRunCount = runs.filter((run) => historyTarget === 'all' || run.targetId === historyTarget).length

  return (
    <>
      <div className="section-heading backup-settings-heading">
        <div><p className="eyebrow">BACKUP & RESTORE</p><h2>数据备份</h2></div>
        <div className="backup-heading-actions">
          <button className="icon-button icon-button--small" type="button" title="刷新备份状态" aria-label="刷新备份状态" onClick={() => void loadData(undefined, true)} disabled={loading || refreshing}>
            <RefreshCw className={refreshing ? 'spin' : ''} />
          </button>
          <button className="primary-button" type="button" onClick={openCreateTarget}><Plus />添加存储目标</button>
        </div>
      </div>

      {loading && targets.length === 0 && (
        <div className="backup-settings-loading" role="status"><LoaderCircle className="spin" /><span>正在读取备份配置与任务历史</span></div>
      )}

      {loadError && (
        <div className="connection-feedback connection-feedback--error" role="alert">
          <CircleAlert />
          <span><strong>备份数据加载失败</strong><small>{loadError}</small></span>
          <button className="secondary-button" type="button" onClick={() => void loadData(undefined, true)} disabled={refreshing}><RefreshCw />重试</button>
        </div>
      )}

      {!loading || targets.length > 0 ? (
        <>
          <div className="backup-overview" aria-label="备份概览">
            <div><Server /><span><small>存储目标</small><strong>{targets.length}</strong></span></div>
            <div><CalendarClock /><span><small>自动计划</small><strong>{targets.filter((target) => target.enabled && target.schedule).length}</strong></span></div>
            <div className={activeRunCount > 0 ? 'backup-overview__active' : ''}><DatabaseBackup /><span><small>进行中</small><strong>{activeRunCount}</strong></span></div>
            <div><CheckCircle2 /><span><small>最近完成</small><strong>{formatDateTime(latestSuccess?.finishedAt ?? latestSuccess?.createdAt)}</strong></span></div>
          </div>

          {actionError && (
            <div className="connection-feedback connection-feedback--error" role="alert">
              <CircleAlert /><span><strong>操作提交失败</strong><small>{actionError}</small></span>
              <button className="icon-button icon-button--small" type="button" title="关闭提示" aria-label="关闭操作错误" onClick={() => setActionError(undefined)}><X /></button>
            </div>
          )}

          <section className="backup-targets" aria-labelledby="backup-targets-title">
            <div className="backup-subheading">
              <div><p className="panel-label">STORAGE TARGETS</p><h3 id="backup-targets-title">存储目标</h3><span>每个目标独立配置连接、执行计划和保留份数</span></div>
            </div>
            {targets.length === 0 ? (
              <div className="backup-empty-state">
                <span><Cloud /></span>
                <strong>尚未配置备份存储</strong>
                <p>添加 S3 兼容存储或 WebDAV 后即可创建数据库副本。</p>
                <button className="primary-button" type="button" onClick={openCreateTarget}><Plus />添加第一个目标</button>
              </div>
            ) : (
              <div className="backup-target-list">
                {targets.map((target) => {
                  const latestRun = latestRunByTarget.get(target.id)
                  const busy = latestRun?.state === 'pending' || latestRun?.state === 'running'
                  return (
                    <article className="backup-target-row" key={target.id}>
                      <TargetIcon kind={target.kind} />
                      <div className="backup-target-row__identity">
                        <span><strong>{target.name}</strong><small title={targetLocation(target)}>{targetLocation(target)}</small></span>
                        <span className={target.enabled ? 'backup-target-enabled' : 'backup-target-enabled backup-target-enabled--off'}><span />{target.enabled ? '已启用' : '已停用'}</span>
                      </div>
                      <dl className="backup-target-row__meta">
                        <div><dt>执行计划</dt><dd>{formatSchedule(target.schedule)}</dd></div>
                        <div><dt>保留策略</dt><dd>最近 {target.retentionCount} 份</dd></div>
                        <div><dt>最近任务</dt><dd>{latestRun ? <RunState state={latestRun.state} /> : '尚未执行'}</dd></div>
                        <div><dt>更新时间</dt><dd>{formatDateTime(target.updatedAt)}</dd></div>
                      </dl>
                      <div className="backup-target-row__actions">
                        <button className="secondary-button" type="button" onClick={() => void queueRun(target)} disabled={!target.enabled || Boolean(queueingTargetId) || busy} title={!target.enabled ? '先启用该目标' : '立即创建数据库副本'}>
                          {queueingTargetId === target.id || busy ? <LoaderCircle className="spin" /> : <Play />}
                          {busy ? '执行中' : '立即备份'}
                        </button>
                        <button className="icon-button" type="button" title={`编辑 ${target.name}`} aria-label={`编辑备份目标 ${target.name}`} onClick={() => openEditTarget(target)}><Pencil /></button>
                      </div>
                    </article>
                  )
                })}
              </div>
            )}
          </section>

          <section className="backup-history" aria-labelledby="backup-history-title">
            <div className="backup-subheading backup-subheading--history">
              <div><p className="panel-label">RUN HISTORY</p><h3 id="backup-history-title">备份历史</h3><span>成功副本可在确认后恢复</span></div>
              <label className="backup-history-filter">
                <span>筛选目标</span>
                <select value={historyTarget} onChange={(event) => { setHistoryTarget(event.target.value); setHistoryLimit(10) }}>
                  <option value="all">全部目标</option>
                  {targets.map((target) => <option value={target.id} key={target.id}>{target.name}</option>)}
                </select>
              </label>
            </div>
            {visibleRuns.length === 0 ? (
              <div className="backup-history-empty"><History /><span>暂无备份任务记录</span></div>
            ) : (
              <div className="backup-history-scroller">
                <table className="backup-history-table">
                  <thead><tr><th>创建时间</th><th>存储目标</th><th>状态</th><th>副本</th><th>结果</th><th><span className="sr-only">操作</span></th></tr></thead>
                  <tbody>
                    {visibleRuns.map((run) => {
                      const target = targetById.get(run.targetId)
                      return (
                        <tr key={run.id}>
                          <td><strong>{formatDateTime(run.createdAt)}</strong><small>{run.id}</small></td>
                          <td>{target ? <span className="backup-history-target"><TargetIcon kind={target.kind} /><span>{target.name}</span></span> : run.targetId}</td>
                          <td><RunState state={run.state} /></td>
                          <td><strong>{formatBytes(run.sizeBytes)}</strong><small title={run.objectKey}>{run.objectKey || '等待生成'}</small></td>
                          <td className={run.error ? 'backup-history-result backup-history-result--error' : 'backup-history-result'}>{run.error || (run.finishedAt ? formatDateTime(run.finishedAt) : '等待任务完成')}</td>
                          <td><button className="secondary-button backup-restore-button" type="button" onClick={() => selectRestore(run)} disabled={run.state !== 'succeeded' || !run.objectKey} title={run.state === 'succeeded' && run.objectKey ? '使用此副本恢复数据库' : '成功生成副本对象后可恢复'}><ArchiveRestore />恢复</button></td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            )}
            {visibleRuns.length < filteredRunCount && <button className="backup-history-more" type="button" onClick={() => setHistoryLimit((value) => value + 10)}>显示更多记录</button>}
          </section>
        </>
      ) : null}

      {targetDialogOpen && createPortal(
        <div className="modal-layer" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && closeTargetDialog()}>
          <form className="modal backup-target-dialog" onSubmit={saveTarget} role="dialog" aria-modal="true" aria-labelledby="backup-target-dialog-title">
            <header className="modal__header backup-target-dialog__header">
              <div><p className="eyebrow">STORAGE CONNECTION</p><h2 id="backup-target-dialog-title">{editingTarget ? '编辑存储目标' : '添加存储目标'}</h2></div>
              <button className="icon-button" type="button" title="关闭" aria-label="关闭存储目标配置" onClick={closeTargetDialog} disabled={targetSaving}><X /></button>
            </header>
            <div className="backup-target-dialog__body">
              <section className="backup-form-section">
                <div className="backup-form-section__heading"><span>01</span><div><strong>目标信息</strong><small>名称、存储类型和运行状态</small></div></div>
                <div className="connection-form-grid">
                  <label className="connection-form-field connection-form-field--wide">
                    <span><strong>目标名称</strong><small>用于任务历史与恢复确认</small></span>
                    <input autoFocus value={targetDraft.name} onChange={(event) => { setTargetDraft((current) => ({ ...current, name: event.target.value })); setTargetError(undefined) }} placeholder="例如：生产数据库归档" required disabled={targetSaving} />
                  </label>
                  <div className="connection-form-field connection-form-field--wide">
                    <span><strong>存储类型</strong><small>{editingTarget ? '已有目标的存储类型保持不变' : '选择 S3 兼容服务或 WebDAV'}</small></span>
                    <div className="backup-kind-picker" role="radiogroup" aria-label="备份存储类型">
                      {(['s3', 'webdav'] as BackupProvider[]).map((kind) => {
                        const Icon = kind === 's3' ? Cloud : HardDrive
                        return <button className={targetDraft.kind === kind ? 'backup-kind-option backup-kind-option--active' : 'backup-kind-option'} type="button" role="radio" aria-checked={targetDraft.kind === kind} key={kind} onClick={() => setTargetDraft((current) => ({ ...current, kind }))} disabled={Boolean(editingTarget) || targetSaving}><Icon /><span><strong>{kind === 's3' ? 'S3 Compatible' : 'WebDAV'}</strong><small>{kind === 's3' ? 'AWS S3、R2、MinIO 等' : 'NAS 与通用 DAV 服务'}</small></span>{targetDraft.kind === kind && <Check />}</button>
                      })}
                    </div>
                  </div>
                </div>
              </section>

              <section className="backup-form-section">
                <div className="backup-form-section__heading"><span>02</span><div><strong>连接配置</strong><small>连接信息由服务端加密保存，返回结果不包含凭据</small></div></div>
                {editingTarget && (
                  <>
                    <div className="backup-replace-config">
                      <span><strong>替换连接配置</strong><small>关闭时保留当前加密配置；开启后使用下方完整配置覆盖</small></span>
                      <button className={`toggle ${targetDraft.replaceConfig ? 'toggle--checked' : ''}`} type="button" role="switch" aria-checked={targetDraft.replaceConfig} aria-label="替换备份连接配置" onClick={() => setTargetDraft((current) => ({ ...current, replaceConfig: !current.replaceConfig }))} disabled={targetSaving}><span /></button>
                    </div>
                    {!targetDraft.replaceConfig && (
                      <div className="backup-preserved-config">
                        <TargetIcon kind={editingTarget.kind} />
                        <span><strong>{targetLocation(editingTarget)}</strong><small>{editingTarget.configured ? '当前加密连接配置将保持不变' : '目标尚未保存连接配置'}</small></span>
                      </div>
                    )}
                  </>
                )}
                {(!editingTarget || targetDraft.replaceConfig) && targetDraft.kind === 's3' && (
                  <div className="connection-form-grid">
                    <label className="connection-form-field"><span><strong>Bucket</strong><small>存储桶名称</small></span><input value={targetDraft.bucket} onChange={(event) => setTargetDraft((current) => ({ ...current, bucket: event.target.value }))} required disabled={targetSaving} spellCheck={false} /></label>
                    <label className="connection-form-field"><span><strong>Region</strong><small>例如 us-east-1</small></span><input value={targetDraft.region} onChange={(event) => setTargetDraft((current) => ({ ...current, region: event.target.value }))} disabled={targetSaving} spellCheck={false} /></label>
                    <label className="connection-form-field connection-form-field--wide"><span><strong>Endpoint</strong><small>AWS S3 可留空；R2、MinIO 等填写 HTTPS 地址</small></span><input type="url" value={targetDraft.endpoint} onChange={(event) => setTargetDraft((current) => ({ ...current, endpoint: event.target.value }))} placeholder="https://ACCOUNT.r2.cloudflarestorage.com" disabled={targetSaving} spellCheck={false} /></label>
                    <label className="connection-form-field connection-form-field--wide"><span><strong>对象前缀</strong><small>备份对象保存目录，不以 / 开头</small></span><input value={targetDraft.prefix} onChange={(event) => setTargetDraft((current) => ({ ...current, prefix: event.target.value }))} placeholder="account-manager" disabled={targetSaving} spellCheck={false} /></label>
                    <label className="connection-form-field"><span><strong>Access Key ID</strong><small>留空时使用服务端默认凭据链</small></span><input value={targetDraft.accessKeyId} onChange={(event) => setTargetDraft((current) => ({ ...current, accessKeyId: event.target.value }))} autoComplete="off" disabled={targetSaving} spellCheck={false} /></label>
                    <label className="connection-form-field"><span><strong>Secret Access Key</strong><small>与 Access Key ID 成对填写</small></span><span className="connection-secret-input"><input type={secretsVisible ? 'text' : 'password'} value={targetDraft.secretAccessKey} onChange={(event) => setTargetDraft((current) => ({ ...current, secretAccessKey: event.target.value }))} autoComplete="new-password" disabled={targetSaving} aria-label="S3 Secret Access Key" /><button className="icon-button icon-button--small" type="button" title={secretsVisible ? '隐藏密钥' : '显示密钥'} aria-label={secretsVisible ? '隐藏 S3 密钥' : '显示 S3 密钥'} onClick={() => setSecretsVisible((value) => !value)} disabled={!targetDraft.secretAccessKey}><>{secretsVisible ? <EyeOff /> : <Eye />}</></button></span></label>
                    <label className="connection-form-field connection-form-field--wide"><span><strong>Session Token</strong><small>临时凭据场景使用</small></span><input type={secretsVisible ? 'text' : 'password'} value={targetDraft.sessionToken} onChange={(event) => setTargetDraft((current) => ({ ...current, sessionToken: event.target.value }))} autoComplete="new-password" disabled={targetSaving} /></label>
                    <div className="backup-inline-toggle connection-form-field--wide"><span><strong>使用 Path-style</strong><small>MinIO 等部分 S3 兼容服务需要开启</small></span><button className={`toggle ${targetDraft.usePathStyle ? 'toggle--checked' : ''}`} type="button" role="switch" aria-checked={targetDraft.usePathStyle} aria-label="S3 Path-style" onClick={() => setTargetDraft((current) => ({ ...current, usePathStyle: !current.usePathStyle }))} disabled={targetSaving}><span /></button></div>
                  </div>
                )}
                {(!editingTarget || targetDraft.replaceConfig) && targetDraft.kind === 'webdav' && (
                  <div className="connection-form-grid">
                    <label className="connection-form-field connection-form-field--wide"><span><strong>WebDAV 地址</strong><small>填写不含用户名、查询参数和片段的 HTTP(S) 地址</small></span><input type="url" value={targetDraft.baseUrl} onChange={(event) => setTargetDraft((current) => ({ ...current, baseUrl: event.target.value }))} placeholder="https://dav.example.com/backups" required disabled={targetSaving} spellCheck={false} /></label>
                    <label className="connection-form-field"><span><strong>目录前缀</strong><small>备份对象保存目录</small></span><input value={targetDraft.webdavPrefix} onChange={(event) => setTargetDraft((current) => ({ ...current, webdavPrefix: event.target.value }))} placeholder="account-manager" disabled={targetSaving} spellCheck={false} /></label>
                    <label className="connection-form-field"><span><strong>认证方式</strong><small>基础认证或 Bearer Token</small></span><select className="backup-form-select" value={targetDraft.webdavAuthMode} onChange={(event) => setTargetDraft((current) => ({ ...current, webdavAuthMode: event.target.value as WebDAVAuthMode }))} disabled={targetSaving}><option value="none">无认证</option><option value="basic">用户名与密码</option><option value="bearer">Bearer Token</option></select></label>
                    {targetDraft.webdavAuthMode === 'basic' && <><label className="connection-form-field"><span><strong>用户名</strong><small>WebDAV 登录名</small></span><input value={targetDraft.username} onChange={(event) => setTargetDraft((current) => ({ ...current, username: event.target.value }))} autoComplete="username" required disabled={targetSaving} /></label><label className="connection-form-field"><span><strong>密码</strong><small>服务端加密保存</small></span><span className="connection-secret-input"><input type={secretsVisible ? 'text' : 'password'} value={targetDraft.password} onChange={(event) => setTargetDraft((current) => ({ ...current, password: event.target.value }))} autoComplete="new-password" required disabled={targetSaving} /><button className="icon-button icon-button--small" type="button" title={secretsVisible ? '隐藏密码' : '显示密码'} aria-label={secretsVisible ? '隐藏 WebDAV 密码' : '显示 WebDAV 密码'} onClick={() => setSecretsVisible((value) => !value)} disabled={!targetDraft.password}>{secretsVisible ? <EyeOff /> : <Eye />}</button></span></label></>}
                    {targetDraft.webdavAuthMode === 'bearer' && <label className="connection-form-field connection-form-field--wide"><span><strong>Bearer Token</strong><small>服务端加密保存</small></span><span className="connection-secret-input"><input type={secretsVisible ? 'text' : 'password'} value={targetDraft.bearerToken} onChange={(event) => setTargetDraft((current) => ({ ...current, bearerToken: event.target.value }))} autoComplete="new-password" required disabled={targetSaving} /><button className="icon-button icon-button--small" type="button" title={secretsVisible ? '隐藏令牌' : '显示令牌'} aria-label={secretsVisible ? '隐藏 WebDAV Bearer Token' : '显示 WebDAV Bearer Token'} onClick={() => setSecretsVisible((value) => !value)} disabled={!targetDraft.bearerToken}>{secretsVisible ? <EyeOff /> : <Eye />}</button></span></label>}
                    <div className="backup-inline-toggle connection-form-field--wide"><span><strong>跳过 TLS 证书校验</strong><small>仅供使用自签名证书的内网 WebDAV</small></span><button className={`toggle ${targetDraft.insecureSkipVerify ? 'toggle--checked' : ''}`} type="button" role="switch" aria-checked={targetDraft.insecureSkipVerify} aria-label="跳过 WebDAV TLS 证书校验" onClick={() => setTargetDraft((current) => ({ ...current, insecureSkipVerify: !current.insecureSkipVerify }))} disabled={targetSaving}><span /></button></div>
                  </div>
                )}
              </section>

              <section className="backup-form-section">
                <div className="backup-form-section__heading"><span>03</span><div><strong>执行与保留</strong><small>自动计划按服务端时区执行</small></div></div>
                <div className="connection-form-grid">
                  <label className="connection-form-field"><span><strong>备份频率</strong><small>可仅保留手动执行</small></span><select className="backup-form-select" value={targetDraft.schedulePreset} onChange={(event) => setTargetDraft((current) => ({ ...current, schedulePreset: event.target.value as SchedulePreset }))} disabled={targetSaving}><option value="manual">仅手动</option><option value="daily">每天 02:15</option><option value="weekly">每周日 02:15</option><option value="six-hours">每 6 小时</option><option value="custom">自定义 Cron</option></select></label>
                  <label className="connection-form-field"><span><strong>保留份数</strong><small>目标中保留最近的成功副本</small></span><input type="number" value={targetDraft.retentionCount} min={1} max={365} onChange={(event) => setTargetDraft((current) => ({ ...current, retentionCount: Math.min(365, Math.max(1, Number(event.target.value) || 1)) }))} required disabled={targetSaving} /></label>
                  {targetDraft.schedulePreset === 'custom' && <label className="connection-form-field connection-form-field--wide"><span><strong>Cron 表达式</strong><small>五段格式：分钟 小时 日 月 星期</small></span><input value={targetDraft.customSchedule} onChange={(event) => setTargetDraft((current) => ({ ...current, customSchedule: event.target.value }))} placeholder="15 2 * * *" required disabled={targetSaving} spellCheck={false} /></label>}
                  <div className="backup-inline-toggle connection-form-field--wide"><span><strong>启用此目标</strong><small>停用后不会创建手动或计划任务</small></span><button className={`toggle ${targetDraft.enabled ? 'toggle--checked' : ''}`} type="button" role="switch" aria-checked={targetDraft.enabled} aria-label="启用备份目标" onClick={() => setTargetDraft((current) => ({ ...current, enabled: !current.enabled }))} disabled={targetSaving}><span /></button></div>
                </div>
              </section>
              {targetError && <div className="connection-feedback connection-feedback--error" role="alert"><CircleAlert /><span><strong>目标保存失败</strong><small>{targetError}</small></span></div>}
            </div>
            <footer className="backup-target-dialog__footer"><button className="secondary-button" type="button" onClick={closeTargetDialog} disabled={targetSaving}>取消</button><button className="primary-button" type="submit" disabled={targetSaving}>{targetSaving ? <LoaderCircle className="spin" /> : <Save />}{targetSaving ? '正在保存' : editingTarget ? '保存目标' : '创建目标'}</button></footer>
          </form>
        </div>,
        document.body,
      )}

      {restoreRun && createPortal(
        <div className="modal-layer" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && closeRestore()}>
          <section className="modal backup-restore-dialog" role="dialog" aria-modal="true" aria-labelledby="backup-restore-title">
            <header className="modal__header"><div><p className="eyebrow">DATABASE RESTORE</p><h2 id="backup-restore-title">恢复数据库副本</h2></div><button className="icon-button" type="button" title="关闭" aria-label="关闭恢复确认" onClick={closeRestore} disabled={restoreSubmitting || restoreActive}><X /></button></header>
            {!restoreOperation && (
              <>
                <div className="backup-restore-warning"><ShieldAlert /><span><strong>恢复会替换当前数据库内容</strong><small>执行期间备份与恢复任务互斥，请保持服务和数据库连接稳定。</small></span></div>
                <dl className="backup-restore-summary"><div><dt>存储目标</dt><dd>{targetById.get(restoreRun.targetId)?.name ?? restoreRun.targetId}</dd></div><div><dt>副本时间</dt><dd>{formatDateTime(restoreRun.finishedAt ?? restoreRun.createdAt)}</dd></div><div><dt>副本大小</dt><dd>{formatBytes(restoreRun.sizeBytes)}</dd></div><div><dt>任务 ID</dt><dd>{restoreRun.id}</dd></div></dl>
                <label className="backup-confirm-control"><input type="checkbox" checked={restoreConfirmed} onChange={(event) => setRestoreConfirmed(event.target.checked)} disabled={restoreSubmitting} /><span><strong>我已确认恢复此数据库副本</strong><small>恢复完成前不关闭服务或修改数据库。</small></span></label>
              </>
            )}
            {restoreOperation && (
              <div className={`backup-restore-progress backup-restore-progress--${restoreOperation.state}`}>
                <span className="backup-restore-progress__icon">{restoreOperation.state === 'succeeded' ? <CheckCircle2 /> : restoreOperation.state === 'failed' ? <CircleAlert /> : <LoaderCircle className="spin" />}</span>
                <span><strong>{restoreOperation.state === 'succeeded' ? '数据库恢复完成' : restoreOperation.state === 'failed' ? '数据库恢复失败' : restoreOperation.state === 'running' ? '正在恢复数据库' : '恢复任务等待执行'}</strong><small>{restoreOperation.error || (restoreActive ? '正在校验、解密并应用数据库副本，请保持此页面打开。' : `完成时间：${formatDateTime(restoreOperation.finishedAt)}`)}</small></span>
                {restoreActive && <span className="backup-restore-progress__bar"><span /></span>}
              </div>
            )}
            {restoreError && <div className="connection-feedback connection-feedback--error" role="alert"><CircleAlert /><span><strong>恢复请求失败</strong><small>{restoreError}</small></span></div>}
            <footer className="modal__actions">
              {!restoreOperation && <><button className="secondary-button" type="button" onClick={closeRestore} disabled={restoreSubmitting}>取消</button><button className="primary-button primary-button--danger" type="button" onClick={() => void startRestore()} disabled={!restoreConfirmed || restoreSubmitting}>{restoreSubmitting ? <LoaderCircle className="spin" /> : <ArchiveRestore />}{restoreSubmitting ? '正在提交' : '开始恢复'}</button></>}
              {restoreOperation && !restoreActive && <button className="primary-button" type="button" onClick={closeRestore}><Check />完成</button>}
            </footer>
          </section>
        </div>,
        document.body,
      )}
    </>
  )
}
