import { CalendarClock, CheckCircle2, Cloud, DatabaseBackup, HardDrive, RefreshCw, TriangleAlert } from 'lucide-react'

import type { BackupDestination, BackupSummary } from '../api/types'

interface BackupPanelProps {
  backup: BackupSummary
  running: boolean
  onRun: () => void
}

function formatDate(value?: string, includeDate = false): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    ...(includeDate ? { month: '2-digit', day: '2-digit' } : {}),
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(date)
}

function formatBytes(value: number): string {
  if (value <= 0) return '—'
  if (value < 1_000_000) return `${Math.round(value / 1000)} KB`
  return `${(value / 1_000_000).toFixed(1)} MB`
}

function DestinationRow({ destination }: { destination: BackupDestination }) {
  const Icon = destination.provider === 's3' ? Cloud : HardDrive
  const StatusIcon = destination.status === 'synced' ? CheckCircle2 : TriangleAlert
  return (
    <div className="backup-destination">
      <span className={`backup-destination__icon backup-destination__icon--${destination.provider}`}><Icon aria-hidden="true" /></span>
      <span className="backup-destination__copy">
        <strong>{destination.label}</strong>
        <small>{destination.detail || '默认存储位置'}</small>
      </span>
      <span className={`backup-destination__status backup-destination__status--${destination.status}`}>
        <StatusIcon aria-hidden="true" />
        {destination.status === 'synced' ? '已同步' : destination.status === 'pending' ? '等待中' : destination.status === 'disabled' ? '未启用' : '异常'}
      </span>
    </div>
  )
}

export function BackupPanel({ backup, running, onRun }: BackupPanelProps) {
  return (
    <aside className="backup-panel" aria-labelledby="backup-title">
      <div className="backup-panel__header">
        <span className="section-icon section-icon--yellow"><DatabaseBackup aria-hidden="true" /></span>
        <span>
          <p className="eyebrow">DATA BACKUP</p>
          <h2 id="backup-title">数据备份</h2>
        </span>
        <span className={`backup-switch ${backup.automatic ? 'backup-switch--on' : ''}`}>
          <span />{backup.automatic ? '自动' : '手动'}
        </span>
      </div>

      <div className="backup-highlight">
        <span className="backup-highlight__check"><CheckCircle2 aria-hidden="true" /></span>
        <span>
          <small>最近备份</small>
          <strong>{formatDate(backup.lastCompletedAt, true)}</strong>
        </span>
        <span className="backup-size">{formatBytes(backup.databaseSizeBytes)}</span>
      </div>

      <div className="backup-schedule">
        <CalendarClock aria-hidden="true" />
        <span><small>执行计划</small><strong>{backup.cadenceLabel}</strong></span>
        <span><small>下次运行</small><strong>{formatDate(backup.nextRunAt, true)}</strong></span>
      </div>

      <div className="backup-destinations">
        <p className="panel-label">存储目标</p>
        {backup.destinations.length > 0 ? backup.destinations.map((destination) => (
          <DestinationRow destination={destination} key={`${destination.provider}-${destination.label}`} />
        )) : <p className="backup-empty">暂无已配置的存储目标</p>}
      </div>

      <button className="secondary-button secondary-button--full" type="button" onClick={onRun} disabled={running}>
        <RefreshCw className={running ? 'spin' : ''} aria-hidden="true" />
        {running ? '正在备份' : '立即备份'}
      </button>
    </aside>
  )
}
