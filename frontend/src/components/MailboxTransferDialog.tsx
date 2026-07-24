import {
  AlertCircle,
  Check,
  ChevronDown,
  Copy,
  Download,
  FileSpreadsheet,
  FileText,
  LoaderCircle,
  Save,
  Settings2,
  Upload,
  X,
} from 'lucide-react'
import { useEffect, useMemo, useRef, useState, type ChangeEvent } from 'react'

import { apiClient, builtInMailboxFormats } from '../api/client'
import type {
  ImportConflictStrategy,
  MailboxExportResult,
  MailboxFormat,
  MailboxFormatField,
  MailboxImportPreview,
  MailboxImportResult,
  MailboxRecord,
} from '../api/types'

export type TransferMode = 'import' | 'export'

interface MailboxTransferDialogProps {
  open: boolean
  initialMode: TransferMode
  mailboxes: MailboxRecord[]
  selectedIds: Set<string>
  source: 'api' | 'mock'
  onClose: () => void
  onImported: (result: MailboxImportResult) => void
}

function flattenMailboxes(mailboxes: MailboxRecord[]): MailboxRecord[] {
  return mailboxes.flatMap((mailbox) => [mailbox, ...mailbox.children])
}

function csvCell(value: string, delimiter: string): string {
  if (!value.includes(delimiter) && !/["\r\n]/.test(value)) return value
  return `"${value.replace(/"/g, '""')}"`
}

function fieldValue(mailbox: MailboxRecord, target: string): string {
  switch (target) {
    case 'address':
    case 'email': return mailbox.address
    case 'provider': return mailbox.provider
    case 'display_name': return mailbox.displayName || ''
    case 'mailbox_id':
    case 'id': return mailbox.id
    case 'kind': return mailbox.kind
    case 'client_id': return ''
    case 'refresh_token': return mailbox.auth.refreshTokenExpiresAt ? '[由服务端导出]' : ''
    default: return ''
  }
}

function localExport(mailboxes: MailboxRecord[], format: MailboxFormat, ids: string[]): MailboxExportResult {
  const idSet = new Set(ids)
  const rows = flattenMailboxes(mailboxes).filter((mailbox) => idSet.size === 0 || idSet.has(mailbox.id))
  if (format.kind === 'json') {
    return {
      content: JSON.stringify(rows.map((mailbox) => Object.fromEntries(format.fields.map((field) => [field.column, fieldValue(mailbox, field.target)]))), null, 2),
      contentType: 'application/json;charset=utf-8',
      fileName: 'mailboxes-preview.json',
    }
  }
  if (format.kind === 'template') {
    const content = rows.map((mailbox) => format.fields.reduce((rendered, field) => {
      const value = fieldValue(mailbox, field.target)
      return rendered
        .split(`{{${field.column}}}`).join(value)
        .split(`{{${field.target}}}`).join(value)
    }, format.template || '')).join('\n')
    return { content, contentType: 'text/plain;charset=utf-8', fileName: 'mailboxes-preview.txt' }
  }
  const lines = rows.map((mailbox) => format.fields.map((field) => csvCell(fieldValue(mailbox, field.target), format.delimiter)).join(format.delimiter))
  if (format.hasHeader) lines.unshift(format.fields.map((field) => field.column).join(format.delimiter))
  return { content: lines.join('\n'), contentType: 'text/csv;charset=utf-8', fileName: 'mailboxes-preview.csv' }
}

function parseDelimitedLine(line: string, delimiter: string): string[] {
  if (delimiter.length !== 1) return line.split(delimiter)
  const values: string[] = []
  let current = ''
  let quoted = false
  for (let index = 0; index < line.length; index += 1) {
    const character = line[index]
    if (character === '"' && line[index + 1] === '"' && quoted) {
      current += '"'
      index += 1
    } else if (character === '"') {
      quoted = !quoted
    } else if (character === delimiter && !quoted) {
      values.push(current.trim())
      current = ''
    } else {
      current += character
    }
  }
  values.push(current.trim())
  return values
}

function localImportPreview(content: string, format: MailboxFormat, mailboxes: MailboxRecord[]): MailboxImportPreview {
  const existing = new Set(flattenMailboxes(mailboxes).map((mailbox) => mailbox.address.toLowerCase()))
  let values: Array<Record<string, string>> = []
  if (format.kind === 'json') {
    const parsed = JSON.parse(content) as unknown
    if (!Array.isArray(parsed)) throw new Error('JSON 顶层必须是数组')
    values = parsed
      .filter((item): item is Record<string, unknown> => typeof item === 'object' && item !== null && !Array.isArray(item))
      .map((item) => Object.fromEntries(format.fields.map((field) => [field.target, String(item[field.column] ?? field.default ?? '')])))
  } else {
    const lines = content.split(/\r?\n/).filter((line) => line.trim())
    const header = format.hasHeader ? parseDelimitedLine(lines[0] ?? '', format.delimiter).map((column) => column.toLowerCase()) : []
    const dataLines = format.hasHeader ? lines.slice(1) : lines
    values = dataLines.map((line) => {
      const cells = parseDelimitedLine(line, format.delimiter)
      return Object.fromEntries(format.fields.map((field, index) => {
        const columnIndex = format.hasHeader ? header.indexOf(field.column.toLowerCase()) : index
        return [field.target, (columnIndex >= 0 ? cells[columnIndex] : '') || field.default || '']
      }))
    })
  }
  const rows = values.map((row, index) => {
    const address = row.address || row.email || ''
    const conflict = Boolean(address && existing.has(address.toLowerCase()))
    const error = !address
    return {
      line: index + 1 + (format.hasHeader && format.kind !== 'json' ? 1 : 0),
      status: error ? 'error' as const : conflict ? 'conflict' as const : 'valid' as const,
      address: address || undefined,
      provider: row.provider || undefined,
      message: error ? '缺少邮箱地址' : conflict ? '邮箱已存在' : undefined,
      values: row,
    }
  })
  return {
    rows,
    totalCount: rows.length,
    validCount: rows.filter((row) => row.status !== 'error').length,
    conflictCount: rows.filter((row) => row.status === 'conflict').length,
    errorCount: rows.filter((row) => row.status === 'error').length,
  }
}

const sensitiveTargets = new Set(['refresh_token', 'password', 'pickup_key', 'platform_account_password', 'platform_access_token'])

function parseFieldMappings(value: string): MailboxFormatField[] {
  return value
    .split(/[,\n]/)
    .map((entry) => entry.trim())
    .filter(Boolean)
    .map((entry) => {
      const [rawColumn, rawTarget] = entry.split('=', 2)
      const column = rawColumn.trim()
      const target = (rawTarget || rawColumn).trim().toLowerCase()
      return {
        column,
        target,
        required: target === 'address' || undefined,
        sensitive: sensitiveTargets.has(target) || undefined,
      }
    })
    .filter((field) => field.column && field.target)
}

function triggerDownload(result: MailboxExportResult) {
  const url = URL.createObjectURL(new Blob([result.content], { type: result.contentType }))
  const link = document.createElement('a')
  link.href = url
  link.download = result.fileName
  link.click()
  URL.revokeObjectURL(url)
}

function TransferPreview({ preview }: { preview: MailboxImportPreview }) {
  return (
    <div className="transfer-preview">
      <div className="preview-summary">
        <span><strong>{preview.totalCount}</strong> 总行数</span>
        <span className="preview-summary__valid"><strong>{preview.validCount}</strong> 可导入</span>
        <span className="preview-summary__conflict"><strong>{preview.conflictCount}</strong> 冲突</span>
        <span className="preview-summary__error"><strong>{preview.errorCount}</strong> 错误</span>
      </div>
      <div className="preview-table-wrap">
        <table className="preview-table">
          <thead><tr><th>行</th><th>邮箱</th><th>平台</th><th>状态</th></tr></thead>
          <tbody>{preview.rows.slice(0, 8).map((row) => (
            <tr key={`${row.line}-${row.address ?? 'empty'}`}>
              <td>{row.line}</td>
              <td>{row.address || '—'}</td>
              <td>{row.provider || '自动识别'}</td>
              <td><span className={`preview-state preview-state--${row.status}`}>{row.status === 'valid' ? '有效' : row.status === 'conflict' ? '冲突' : row.message || '错误'}</span></td>
            </tr>
          ))}</tbody>
        </table>
      </div>
      {preview.rows.length > 8 && <p className="preview-more">另有 {preview.rows.length - 8} 行将在提交时处理</p>}
    </div>
  )
}

export function MailboxTransferDialog({ open, initialMode, mailboxes, selectedIds, source, onClose, onImported }: MailboxTransferDialogProps) {
  const [mode, setMode] = useState<TransferMode>(initialMode)
  const [formats, setFormats] = useState<MailboxFormat[]>(builtInMailboxFormats)
  const [formatId, setFormatId] = useState(builtInMailboxFormats[0].id)
  const [formatsLoading, setFormatsLoading] = useState(false)
  const [formatWarning, setFormatWarning] = useState<string>()
  const [showFormatEditor, setShowFormatEditor] = useState(false)
  const [customName, setCustomName] = useState('自定义邮箱格式')
  const [customKind, setCustomKind] = useState<MailboxFormat['kind']>('delimited')
  const [customDirection, setCustomDirection] = useState<MailboxFormat['direction']>('both')
  const [customProvider, setCustomProvider] = useState('')
  const [customDelimiter, setCustomDelimiter] = useState(',')
  const [customHeader, setCustomHeader] = useState(true)
  const [customFields, setCustomFields] = useState('email=address,provider=provider,display_name=display_name,client_id=client_id,refresh_token=refresh_token')
  const [customTemplate, setCustomTemplate] = useState('{{email}}')
  const [formatSaving, setFormatSaving] = useState(false)
  const [inputMode, setInputMode] = useState<'file' | 'text'>('file')
  const [content, setContent] = useState('')
  const [fileName, setFileName] = useState<string>()
  const [conflictStrategy, setConflictStrategy] = useState<ImportConflictStrategy>('skip')
  const [preview, setPreview] = useState<MailboxImportPreview>()
  const [importResult, setImportResult] = useState<MailboxImportResult>()
  const [exportResult, setExportResult] = useState<MailboxExportResult>()
  const [includeSensitive, setIncludeSensitive] = useState(false)
  const [exportScope, setExportScope] = useState<'all' | 'selected'>(selectedIds.size > 0 ? 'selected' : 'all')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const fileInputRef = useRef<HTMLInputElement>(null)

  const availableFormats = useMemo(() => formats.filter((format) => format.enabled && (format.direction === 'both' || format.direction === mode)), [formats, mode])
  const activeFormat = availableFormats.find((format) => format.id === formatId) ?? availableFormats[0] ?? builtInMailboxFormats[0]
  const selectedRootIds = useMemo(() => {
    if (exportScope === 'all') return mailboxes.map((mailbox) => mailbox.id)
    return mailboxes.filter((mailbox) => selectedIds.has(mailbox.id) || mailbox.children.some((child) => selectedIds.has(child.id))).map((mailbox) => mailbox.id)
  }, [exportScope, mailboxes, selectedIds])
  const localExportResult = useMemo(() => localExport(mailboxes, activeFormat, exportScope === 'all' ? [] : [...selectedIds]), [activeFormat, exportScope, mailboxes, selectedIds])

  useEffect(() => {
    if (!open) return
    setMode(initialMode)
    setError(undefined)
    setPreview(undefined)
    setImportResult(undefined)
    setExportResult(undefined)
    setExportScope(selectedIds.size > 0 ? 'selected' : 'all')
    if (source === 'mock') {
      setFormats(builtInMailboxFormats)
      setFormatsLoading(false)
      setFormatWarning(undefined)
      return
    }
    setFormatsLoading(true)
    const controller = new AbortController()
    void apiClient.getMailboxFormats(controller.signal)
      .then((items) => {
        const merged = [...items, ...builtInMailboxFormats.filter((builtIn) => !items.some((item) => item.id === builtIn.id))]
        setFormats(merged)
        setFormatWarning(undefined)
      })
      .catch((reason) => {
        if (!(reason instanceof DOMException && reason.name === 'AbortError')) {
          setFormats(builtInMailboxFormats)
          setFormatWarning('格式服务未连接，当前使用内置格式')
        }
      })
      .finally(() => setFormatsLoading(false))
    return () => controller.abort()
  }, [initialMode, open, selectedIds.size, source])

  useEffect(() => {
    if (availableFormats.length > 0 && !availableFormats.some((format) => format.id === formatId)) {
      setFormatId(availableFormats[0].id)
    }
  }, [availableFormats, formatId])

  useEffect(() => {
    if (!open) return
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', closeOnEscape)
    return () => window.removeEventListener('keydown', closeOnEscape)
  }, [onClose, open])

  if (!open) return null

  const readFile = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    if (!file) return
    setFileName(file.name)
    setContent(await file.text())
    setPreview(undefined)
    setImportResult(undefined)
    setError(undefined)
    event.target.value = ''
  }

  const previewImport = async () => {
    if (!content.trim()) return
    setBusy(true)
    setError(undefined)
    setImportResult(undefined)
    try {
      const request = { formatId: activeFormat.id, content, conflictStrategy, fileName }
      const value = source === 'mock' ? localImportPreview(content, activeFormat, mailboxes) : await apiClient.previewMailboxImport(request)
      setPreview(value)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '导入预览失败')
    } finally {
      setBusy(false)
    }
  }

  const importNow = async () => {
    if (!preview || preview.errorCount > 0 || (conflictStrategy === 'error' && preview.conflictCount > 0)) return
    setBusy(true)
    setError(undefined)
    try {
      const result = source === 'mock'
        ? {
            importedCount: Math.max(0, preview.validCount - preview.conflictCount),
            skippedCount: conflictStrategy === 'skip' ? preview.conflictCount : 0,
            updatedCount: conflictStrategy === 'update' ? preview.conflictCount : 0,
            errorCount: preview.errorCount,
          }
        : await apiClient.importMailboxes({ formatId: activeFormat.id, content, conflictStrategy, fileName })
      setImportResult(result)
      onImported(result)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '导入失败')
    } finally {
      setBusy(false)
    }
  }

  const exportNow = async () => {
    if (selectedRootIds.length === 0) return
    setBusy(true)
    setError(undefined)
    try {
      const result = source === 'mock'
        ? localExportResult
        : await apiClient.exportMailboxes({ formatId: activeFormat.id, mailboxIds: selectedRootIds, includeSensitive })
      setExportResult(result)
      triggerDownload(result)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '导出失败')
    } finally {
      setBusy(false)
    }
  }

  const saveFormat = async () => {
    const fields = parseFieldMappings(customFields)
    if (!customName.trim() || fields.length === 0 || (customKind === 'delimited' && !customDelimiter) || (customKind === 'template' && !customTemplate.trim())) return
    setFormatSaving(true)
    setError(undefined)
    try {
      const draft = {
        name: customName.trim(),
        kind: customKind,
        direction: customDirection,
        delimiter: customDelimiter,
        hasHeader: customHeader,
        fields,
        provider: customProvider || undefined,
        template: customKind === 'template' ? customTemplate : undefined,
        enabled: true,
        version: 0,
      }
      const value = source === 'mock'
        ? { ...draft, id: `local_${Date.now().toString(36)}`, builtIn: false, version: 1 }
        : await apiClient.saveMailboxFormat(draft)
      setFormats((current) => [...current.filter((item) => item.id !== value.id), value])
      setFormatId(value.id)
      setShowFormatEditor(false)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '格式保存失败')
    } finally {
      setFormatSaving(false)
    }
  }

  const copyPreview = async () => {
    try { await navigator.clipboard?.writeText(exportResult?.content || localExportResult.content) } catch { setError('浏览器未授予剪贴板权限') }
  }

  const importBlocked = !preview || preview.errorCount > 0 || (conflictStrategy === 'error' && preview.conflictCount > 0)
  const importCount = preview ? Math.max(0, preview.validCount - (conflictStrategy === 'skip' ? preview.conflictCount : 0)) : 0

  return (
    <div className="modal-layer" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <section className="transfer-dialog" role="dialog" aria-modal="true" aria-labelledby="transfer-dialog-title">
        <header className="transfer-dialog__header">
          <div>
            <p className="eyebrow">MAILBOX DATA</p>
            <h2 id="transfer-dialog-title">邮箱导入与导出</h2>
          </div>
          <button className="icon-button" type="button" title="关闭" aria-label="关闭导入导出" onClick={onClose}><X /></button>
        </header>

        <div className="transfer-mode-tabs" role="tablist" aria-label="导入或导出">
          <button className={mode === 'import' ? 'transfer-mode-tab transfer-mode-tab--active' : 'transfer-mode-tab'} type="button" role="tab" aria-selected={mode === 'import'} onClick={() => setMode('import')}><Upload />导入邮箱</button>
          <button className={mode === 'export' ? 'transfer-mode-tab transfer-mode-tab--active' : 'transfer-mode-tab'} type="button" role="tab" aria-selected={mode === 'export'} onClick={() => setMode('export')}><Download />导出邮箱</button>
        </div>

        <div className="transfer-dialog__body">
          <div className="format-toolbar">
            <label className="transfer-field"><span>数据格式</span><span className="select-control select-control--wide"><select value={activeFormat.id} onChange={(event) => { setFormatId(event.target.value); setPreview(undefined); setExportResult(undefined) }} disabled={formatsLoading}>{availableFormats.map((format) => <option value={format.id} key={format.id}>{format.name}{format.builtIn ? ' · 内置' : ''}</option>)}</select><ChevronDown /></span></label>
            <button className="secondary-button" type="button" onClick={() => setShowFormatEditor((value) => !value)}><Settings2 />自定义格式</button>
          </div>
          {formatWarning && <div className="inline-alert inline-alert--warning"><AlertCircle />{formatWarning}</div>}

          {showFormatEditor && (
            <div className="format-editor">
              <div className="format-editor__heading"><div><FileSpreadsheet /><strong>格式配置</strong></div><button className="icon-button icon-button--tiny" type="button" title="关闭格式配置" onClick={() => setShowFormatEditor(false)}><X /></button></div>
              <div className="format-editor__grid">
                <label className="transfer-field"><span>格式名称</span><input value={customName} onChange={(event) => setCustomName(event.target.value)} /></label>
                <label className="transfer-field"><span>类型</span><span className="select-control select-control--wide"><select value={customKind} onChange={(event) => { const kind = event.target.value as MailboxFormat['kind']; setCustomKind(kind); if (kind === 'template') setCustomDirection('export') }}><option value="delimited">分隔文本</option><option value="json">JSON</option><option value="template">模板文本</option></select><ChevronDown /></span></label>
                <label className="transfer-field"><span>用途</span><span className="select-control select-control--wide"><select value={customDirection} onChange={(event) => setCustomDirection(event.target.value as MailboxFormat['direction'])}><option value="both" disabled={customKind === 'template'}>导入与导出</option><option value="import" disabled={customKind === 'template'}>仅导入</option><option value="export">仅导出</option></select><ChevronDown /></span></label>
                <label className="transfer-field"><span>邮箱平台</span><span className="select-control select-control--wide"><select value={customProvider} onChange={(event) => setCustomProvider(event.target.value)}><option value="">由数据指定</option><option value="microsoft">Microsoft</option><option value="gmail">Google</option><option value="cloudflare_route">Cloudflare</option></select><ChevronDown /></span></label>
                <label className="transfer-field"><span>分隔符</span><input value={customDelimiter} onChange={(event) => setCustomDelimiter(event.target.value)} disabled={customKind !== 'delimited'} /></label>
                <label className="format-checkbox"><input type="checkbox" checked={customHeader} onChange={(event) => setCustomHeader(event.target.checked)} disabled={customKind !== 'delimited'} /><span><Check /></span>包含表头</label>
                <label className="transfer-field format-editor__wide"><span>字段映射</span><input value={customFields} onChange={(event) => setCustomFields(event.target.value)} /></label>
                {customKind === 'template' && <label className="transfer-field format-editor__wide"><span>导出模板</span><textarea value={customTemplate} onChange={(event) => setCustomTemplate(event.target.value)} /></label>}
              </div>
              <div className="format-editor__actions"><button className="primary-button" type="button" onClick={saveFormat} disabled={formatSaving}>{formatSaving ? <LoaderCircle className="spin" /> : <Save />}{formatSaving ? '正在保存' : '保存格式'}</button></div>
            </div>
          )}

          {mode === 'import' ? (
            <>
              <div className="import-source-tabs" role="tablist" aria-label="导入来源">
                <button className={inputMode === 'file' ? 'source-tab source-tab--active' : 'source-tab'} type="button" role="tab" aria-selected={inputMode === 'file'} onClick={() => setInputMode('file')}><FileSpreadsheet />文件</button>
                <button className={inputMode === 'text' ? 'source-tab source-tab--active' : 'source-tab'} type="button" role="tab" aria-selected={inputMode === 'text'} onClick={() => setInputMode('text')}><FileText />粘贴文本</button>
              </div>
              {inputMode === 'file' ? (
                <button className="file-drop" type="button" onClick={() => fileInputRef.current?.click()}><Upload /><strong>{fileName || '选择导入文件'}</strong><span>{content ? `${content.length.toLocaleString()} 个字符已读取` : 'CSV、TXT 或 JSON'}</span></button>
              ) : (
                <label className="transfer-field"><span>导入文本</span><textarea value={content} onChange={(event) => { setContent(event.target.value); setFileName(undefined); setPreview(undefined) }} placeholder="在此粘贴邮箱数据" /></label>
              )}
              <input ref={fileInputRef} className="visually-hidden" type="file" accept=".csv,.txt,.json" onChange={readFile} />
              {content && inputMode === 'file' && <pre className="raw-preview">{content.slice(0, 900)}{content.length > 900 ? '\n…' : ''}</pre>}
              <div className="import-options">
                <label className="transfer-field"><span>冲突策略</span><span className="select-control select-control--wide"><select value={conflictStrategy} onChange={(event) => { setConflictStrategy(event.target.value as ImportConflictStrategy); setPreview(undefined) }}><option value="skip">跳过已有邮箱</option><option value="update">更新已有邮箱</option><option value="error">发现冲突即停止</option></select><ChevronDown /></span></label>
                <button className="secondary-button" type="button" onClick={previewImport} disabled={!content.trim() || busy}>{busy && !preview ? <LoaderCircle className="spin" /> : <FileText />}生成预览</button>
              </div>
              {preview && <TransferPreview preview={preview} />}
              {importResult && <div className="transfer-success"><Check />已导入 {importResult.importedCount}，更新 {importResult.updatedCount}，跳过 {importResult.skippedCount}</div>}
            </>
          ) : (
            <>
              <div className="export-options">
                <label className="export-scope-option"><input type="radio" name="export-scope" value="all" checked={exportScope === 'all'} onChange={() => setExportScope('all')} /><span><strong>全部主邮箱</strong><small>{mailboxes.length} 个主邮箱</small></span></label>
                <label className={selectedIds.size > 0 ? 'export-scope-option' : 'export-scope-option export-scope-option--disabled'}><input type="radio" name="export-scope" value="selected" checked={exportScope === 'selected'} onChange={() => setExportScope('selected')} disabled={selectedIds.size === 0} /><span><strong>当前选择</strong><small>{selectedIds.size} 个地址，归并为 {selectedRootIds.length} 个主邮箱</small></span></label>
              </div>
              <label className="sensitive-toggle"><span><strong>包含敏感凭据</strong><small>由后端按管理员权限导出 Client ID 与 RT</small></span><button className={`toggle ${includeSensitive ? 'toggle--checked' : ''}`} type="button" role="switch" aria-checked={includeSensitive} onClick={() => setIncludeSensitive((value) => !value)}><span /></button></label>
              <div className="export-preview-heading"><span>文本预览</span><button className="icon-button icon-button--small" type="button" title="复制预览" aria-label="复制导出预览" onClick={copyPreview}><Copy /></button></div>
              <pre className="raw-preview raw-preview--export">{(exportResult?.content || localExportResult.content).slice(0, 1_600) || '当前范围没有可导出的邮箱'}</pre>
            </>
          )}

          {error && <div className="inline-alert inline-alert--error"><AlertCircle />{error}</div>}
        </div>

        <footer className="transfer-dialog__footer">
          <button className="secondary-button" type="button" onClick={onClose}>取消</button>
          {mode === 'import' ? <button className="primary-button" type="button" onClick={importNow} disabled={importBlocked || busy}>{busy && preview ? <LoaderCircle className="spin" /> : <Upload />}{preview ? conflictStrategy === 'error' && preview.conflictCount > 0 ? '存在冲突，停止导入' : `一键导入 ${importCount} 个邮箱` : '先生成预览'}</button> : <button className="primary-button" type="button" onClick={exportNow} disabled={selectedRootIds.length === 0 || busy}>{busy ? <LoaderCircle className="spin" /> : <Download />}{busy ? '正在导出' : `导出 ${selectedRootIds.length} 个主邮箱`}</button>}
        </footer>
      </section>
    </div>
  )
}
