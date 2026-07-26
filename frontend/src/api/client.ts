import { mockDashboard } from './mock-data'
import type {
  BackupDestination,
  BackupDestinationStatus,
  BackupProvider,
  BackupRestoreOperation,
  BackupRestoreState,
  BackupRun,
  BackupRunState,
  BackupSummary,
  BackupTarget,
  BackupTargetConfig,
  BackupTargetConfigSummary,
  CreateBackupTargetInput,
  CachedMessage,
  CachedMessagesResult,
  DashboardResult,
  MailAccessMode,
  MailboxDashboard,
  MailboxDetail,
  MailboxExportRequest,
  MailboxExportResult,
  MailboxFormat,
  MailboxFormatDirection,
  MailboxFormatField,
  MailboxFormatKind,
  MailboxHealth,
  MailboxImportPreview,
  MailboxImportPreviewRow,
  MailboxImportRequest,
  MailboxImportResult,
  MailboxCredentialSummary,
  MailboxAliasDetail,
  LinkedPlatformAccount,
  MailboxRecord,
	ManagedCacheQuery,
	ManagedCacheResult,
  MessageFolder,
  MessageProbeSettings,
  MessageSyncState,
  MailProvider,
  ProviderConnection,
  RevealedCredential,
  RetrievalKeyStatus,
  SaveProviderConnectionInput,
  SaveMessageProbeSettingsInput,
  SaveTokenRefreshSettingsInput,
  S3BackupConfig,
  TokenRefreshSettings,
  UpdateBackupTargetInput,
  WebDAVBackupConfig,
} from './types'

const API_BASE = (import.meta.env.VITE_API_BASE_URL ?? '').replace(/\/$/, '')
const API_TOKEN = import.meta.env.VITE_API_TOKEN ?? ''

type UnknownRecord = Record<string, unknown>

export const builtInMailboxFormats: MailboxFormat[] = [
  {
    id: 'fmt_builtin_pickup2',
    name: '平台取件格式',
    kind: 'delimited',
    direction: 'both',
    delimiter: '----',
    hasHeader: false,
    fields: [
      { column: 'email', target: 'address', required: true },
      { column: 'pickup_key', target: 'pickup_key', required: true, sensitive: true },
    ],
    builtIn: true,
    enabled: true,
    version: 1,
  },
  {
    id: 'fmt_builtin_outlook4',
    name: 'Outlook 邮箱凭证',
    kind: 'delimited',
    direction: 'both',
    delimiter: '----',
    hasHeader: false,
    provider: 'microsoft',
    fields: [
      { column: 'email', target: 'address', required: true },
      { column: 'password', target: 'password', sensitive: true },
      { column: 'client_id', target: 'client_id' },
      { column: 'refresh_token', target: 'refresh_token', sensitive: true },
    ],
    builtIn: true,
    enabled: true,
    version: 1,
  },
]

function isRecord(value: unknown): value is UnknownRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function text(value: unknown, fallback = ''): string {
  return typeof value === 'string' ? value : fallback
}

function optionalText(value: unknown): string | undefined {
  const result = text(value).trim()
  return result && !/^\?+$/.test(result) ? result : undefined
}

function bool(value: unknown, fallback = false): boolean {
  return typeof value === 'boolean' ? value : fallback
}

function numberValue(value: unknown, fallback = 0): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

function provider(value: unknown): MailProvider {
  const normalized = text(value).toLowerCase()
  if (normalized === 'cloudflare' || normalized === 'cloudflare_route' || normalized === 'cf' || normalized === 'domain') return 'cloudflare'
  if (normalized === 'google' || normalized === 'gmail') return 'google'
  return 'microsoft'
}

function health(value: unknown): MailboxHealth {
  if (value === 'attention' || value === 'warning') return 'attention'
  if (value === 'offline' || value === 'error' || value === 'deactive' || value === 'disabled') return 'offline'
  return 'healthy'
}

function retrievalStatus(value: unknown): RetrievalKeyStatus {
  if (value === 'ready') return 'ready'
  if (value === 'expiring' || value === 'expired' || value === 'missing' || value === 'not_applicable') return value
  return 'missing'
}

function accessModes(value: unknown, mailboxProvider: MailProvider): MailAccessMode[] {
  const allowed = new Set<MailAccessMode>(['graph', 'imap', 'oauth', 'forward'])
  const supplied = Array.isArray(value) || typeof value === 'string'
  const values = Array.isArray(value) ? value : typeof value === 'string' ? value.split(/[+,]/) : []
  const parsed = values
    .map((item) => text(item).trim().toLowerCase())
    .filter((item): item is MailAccessMode => allowed.has(item as MailAccessMode))

  if (parsed.length > 0) return parsed
  if (supplied) return []
  if (mailboxProvider === 'cloudflare') return ['forward']
  if (mailboxProvider === 'google') return ['oauth', 'imap']
  return ['graph']
}

function normalizeMailbox(value: unknown, parentId?: string): MailboxRecord | null {
  if (!isRecord(value)) return null

  const mailboxProvider = provider(value.provider ?? value.platform)
  const id = text(value.id ?? value.mailbox_id ?? value.public_id)
  const address = text(value.address ?? value.email)
  if (!id || !address) return null

  const retrieval = isRecord(value.retrieval_key) ? value.retrieval_key : {}
  const auth = isRecord(value.auth) ? value.auth : {}
  const forwarding = isRecord(value.forwarding) ? value.forwarding : null
  const aliasValues = Array.isArray(value.children)
    ? value.children
    : Array.isArray(value.aliases)
      ? value.aliases
      : []

  const children = aliasValues
    .map((alias, index) => {
      if (typeof alias === 'string') {
        return normalizeMailbox(
          {
            id: `${id}_alias_${index + 1}`,
            address: alias,
            provider: mailboxProvider,
            kind: 'split',
            health: value.health ?? value.status,
            retrieval_key: retrieval,
            auth,
            forwarding,
          },
          id,
        )
      }
      return normalizeMailbox(alias, id)
    })
    .filter((item): item is MailboxRecord => item !== null)

  const target = text(forwarding?.target ?? value.forward_to)

  return {
    id,
    parentId,
    kind: parentId || value.kind === 'split' ? 'split' : 'primary',
    provider: mailboxProvider,
    address,
    displayName: optionalText(value.display_name ?? value.name),
    health: health(value.health ?? value.status),
    retrievalKey: {
      status: retrievalStatus(retrieval.status ?? value.retrieval_key_status),
      maskedKey: text(retrieval.masked_key ?? value.masked_retrieval_key) || undefined,
      expiresAt: text(retrieval.expires_at ?? value.retrieval_key_expires_at) || undefined,
      issuedAt: text(retrieval.issued_at) || undefined,
    },
    auth: {
      modes: accessModes(auth.modes ?? value.access_modes, mailboxProvider),
      refreshTokenExpiresAt: text(auth.refresh_token_expires_at ?? value.refresh_token_expires_at) || undefined,
      refreshStatus: optionalText(auth.refresh_status ?? value.refresh_status),
      refreshTokenValidity: (() => {
        const validity = text(auth.refresh_token_validity ?? value.refresh_token_validity)
        return validity === 'expiry_not_returned' || validity === 'missing' || validity === 'error' || validity === 'unknown' || validity === 'not_applicable' ? validity : undefined
      })(),
      graphAccessTokenExpiresAt: text(auth.graph_access_token_expires_at ?? value.graph_access_token_expires_at) || undefined,
      imapAccessTokenExpiresAt: text(auth.imap_access_token_expires_at ?? value.imap_access_token_expires_at) || undefined,
      autoRefresh: bool(auth.auto_refresh ?? value.auto_refresh, mailboxProvider !== 'cloudflare'),
    },
    forwarding: target
      ? {
          target,
          verified: bool(forwarding?.verified ?? value.forwarding_verified),
        }
      : undefined,
    lastMessageAt: text(value.last_message_at) || undefined,
    createdAt: text(value.created_at) || undefined,
    children,
  }
}

function backupProvider(value: unknown): BackupProvider {
  return text(value).toLowerCase() === 'webdav' ? 'webdav' : 's3'
}

function backupStatus(value: unknown): BackupDestinationStatus {
  if (value === 'synced' || value === 'error' || value === 'disabled') return value
  return 'pending'
}

function cadenceLabel(value: unknown): string {
  const raw = text(value).trim()
  const dailyCron = raw.match(/^(\d{1,2})\s+(\d{1,2})\s+\*\s+\*\s+\*$/)
  if (dailyCron) {
    const [, minute, hour] = dailyCron
    return `每天 ${hour.padStart(2, '0')}:${minute.padStart(2, '0')}`
  }
  return raw || '未设置'
}

function formatKind(value: unknown): MailboxFormatKind {
  if (value === 'json' || value === 'template') return value
  return 'delimited'
}

function formatDirection(value: unknown): MailboxFormatDirection {
  if (value === 'import' || value === 'export') return value
  return 'both'
}

function normalizeFormatField(value: unknown): MailboxFormatField | null {
  if (typeof value === 'string') {
    const field = value.trim()
    return field ? { column: field, target: field } : null
  }
  if (!isRecord(value)) return null
  const column = text(value.column ?? value.name).trim()
  const target = text(value.target ?? value.field).trim()
  if (!column || !target) return null
  return {
    column,
    target,
    required: bool(value.required) || undefined,
    sensitive: bool(value.sensitive) || undefined,
    default: optionalText(value.default),
  }
}

function formatFields(value: unknown): MailboxFormatField[] {
  if (!Array.isArray(value)) return []
  return value.map(normalizeFormatField).filter((field): field is MailboxFormatField => field !== null)
}

function maskedToken(value: unknown): string | undefined {
  const token = optionalText(value)
  return token && /^[*\u2022\u25cf\u00b7]+$/u.test(token) ? token : undefined
}

function retrievalMethods(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return [...new Set(value
    .map((item) => text(item).trim().toLowerCase())
    .filter(Boolean))]
}

function normalizeRetrievalCapabilities(value: unknown): MailboxCredentialSummary['retrievalCapabilities'] {
  if (!Array.isArray(value)) return []
  return value.flatMap((item) => {
    if (!isRecord(item)) return []
    const method = text(item.method).trim().toLowerCase()
    const rawStatus = text(item.status).trim().toLowerCase()
    const status = rawStatus === 'configured' || rawStatus === 'verified' || rawStatus === 'failed' || rawStatus === 'unknown' ? rawStatus : 'unknown'
    if (!method) return []
    return [{
      method,
      status,
      accessTokenExpiresAt: optionalText(item.access_token_expires_at),
      checkedAt: optionalText(item.checked_at),
    }]
  })
}

function normalizeCredential(value: unknown): MailboxCredentialSummary | null {
  if (!isRecord(value)) return null
  const metadata = isRecord(value.metadata) ? value.metadata : {}
  const credentialType = text(value.credential_type ?? value.kind ?? value.type)
  if (!credentialType) return null
  const refreshAfter = optionalText(value.refresh_after)
  const refreshStatus = text(value.refresh_status, 'unknown')
  const explicitAutoRefresh = value.auto_refresh ?? metadata.auto_refresh
  const maskedRefreshToken = maskedToken(
    value.masked_refresh_token
    ?? value.refresh_token_masked
    ?? value.refresh_token
    ?? metadata.masked_refresh_token
    ?? metadata.refresh_token_masked,
  )
  return {
    id: optionalText(value.id),
    credentialType,
    clientId: optionalText(value.client_id ?? metadata.client_id),
    retrievalMethods: retrievalMethods(value.retrieval_methods ?? metadata.retrieval_methods),
    retrievalCapabilities: normalizeRetrievalCapabilities(value.retrieval_capabilities ?? metadata.retrieval_capabilities),
    maskedRefreshToken,
    hasRefreshToken: bool(value.has_refresh_token, Boolean(maskedRefreshToken)),
    refreshTokenValidity: (() => {
      const validity = text(value.refresh_token_validity)
      return validity === 'expiry_not_returned' || validity === 'missing' || validity === 'error' || validity === 'unknown' || validity === 'not_applicable' ? validity : undefined
    })(),
    expiresAt: optionalText(value.expires_at),
    graphTokenExpiresAt: optionalText(value.graph_token_expires_at),
    imapTokenExpiresAt: optionalText(value.imap_token_expires_at),
    refreshAfter,
    refreshStatus,
    lastRefreshedAt: optionalText(value.last_refreshed_at),
    lastRefreshError: optionalText(value.last_refresh_error),
    autoRefresh: typeof explicitAutoRefresh === 'boolean'
      ? explicitAutoRefresh
      : Boolean(refreshAfter) || ['active', 'due', 'error'].includes(refreshStatus),
  }
}

function normalizeAlias(value: unknown): MailboxAliasDetail | null {
  if (!isRecord(value)) return null
  const id = text(value.id ?? value.alias_id)
  const address = text(value.address ?? value.email)
  if (!id || !address) return null
  return {
    id,
    address,
    provider: provider(value.provider),
    kind: value.kind === 'forward' ? 'forward' : 'split',
    enabled: bool(value.enabled, true),
  }
}

function normalizeAccount(value: unknown): LinkedPlatformAccount | null {
  if (!isRecord(value)) return null
  const id = text(value.id ?? value.account_id)
  const platform = text(value.platform)
  if (!id || !platform) return null
  return {
    id,
    platform,
    loginAddress: text(value.login_address ?? value.email),
    status: text(value.status, 'unknown'),
    mailboxAliasId: optionalText(value.mailbox_alias_id ?? value.alias_id),
  }
}

function normalizeProviderConnection(value: unknown): ProviderConnection | null {
  if (!isRecord(value)) return null
  const providerKey = text(value.provider).trim()
  if (!providerKey) return null
  return {
    id: optionalText(value.id),
    provider: providerKey,
    name: optionalText(value.name),
    accountId: optionalText(value.account_id),
    zoneId: optionalText(value.zone_id),
    zoneName: optionalText(value.zone_name),
    apiBaseUrl: optionalText(value.api_base_url),
    configured: bool(value.configured),
    enabled: bool(value.enabled),
    version: Math.max(0, Math.trunc(numberValue(value.version))),
    createdAt: optionalText(value.created_at),
    updatedAt: optionalText(value.updated_at),
  }
}

function normalizeTokenRefreshSettings(value: unknown): TokenRefreshSettings {
  const payload = isRecord(value) && isRecord(value.data) ? value.data : value
  if (!isRecord(payload)) throw new Error('令牌刷新设置响应格式无效')
  const leadTimeMinutes = Math.trunc(numberValue(payload.lead_time_minutes, 5))
  if (leadTimeMinutes < 1 || leadTimeMinutes > 30) {
    throw new Error('令牌刷新提前时间超出范围')
  }
  return {
    enabled: bool(payload.enabled, true),
    leadTimeMinutes,
    version: Math.max(0, Math.trunc(numberValue(payload.version))),
    updatedAt: optionalText(payload.updated_at),
  }
}

function normalizeMessageProbeSettings(value: unknown): MessageProbeSettings {
  const payload = isRecord(value) && isRecord(value.data) ? value.data : value
  if (!isRecord(payload)) throw new Error('邮件探测设置响应格式无效')
  const intervalMinutes = Math.trunc(numberValue(payload.interval_minutes, 10))
  if (intervalMinutes < 1 || intervalMinutes > 1440) throw new Error('邮件探测间隔超出范围')
  return {
    enabled: bool(payload.enabled),
    intervalMinutes,
    version: Math.max(0, Math.trunc(numberValue(payload.version))),
    updatedAt: optionalText(payload.updated_at),
  }
}

function stringList(value: unknown): string[] {
  return Array.isArray(value) ? value.map((item) => text(item)).filter(Boolean) : []
}

function normalizeCachedMessage(value: unknown): CachedMessage | null {
  if (!isRecord(value)) return null
  const id = text(value.id)
  const receivedAt = text(value.received_at)
  if (!id || !receivedAt) return null
  return {
    id,
		mailboxId: text(value.mailbox_id),
    providerMessageId: text(value.provider_message_id),
    internetMessageId: optionalText(value.internet_message_id),
    folder: text(value.folder) === 'Junk' ? 'Junk' : 'INBOX',
    from: text(value.from),
    to: stringList(value.to),
    cc: stringList(value.cc),
    subject: text(value.subject, '(无主题)'),
    text: optionalText(value.text),
    html: optionalText(value.html),
    receivedAt,
    unread: bool(value.unread),
		viewedAt: optionalText(value.viewed_at),
		retrievalMethod: optionalText(value.retrieval_method),
  }
}

function normalizeMessageSyncState(value: unknown): MessageSyncState | undefined {
  if (!isRecord(value)) return undefined
  const targetId = text(value.target_id)
  const lastSyncedAt = text(value.last_synced_at)
  if (!targetId || !lastSyncedAt) return undefined
  return {
    targetId,
    lastMessageAt: optionalText(value.last_message_at),
    lastSyncedAt,
    lastError: optionalText(value.last_error),
    retrievalMethod: optionalText(value.retrieval_method),
    cursor: optionalText(value.cursor),
    uidValidity: Math.max(0, Math.trunc(numberValue(value.uid_validity))),
    highestUid: Math.max(0, Math.trunc(numberValue(value.highest_uid))),
  }
}

function normalizeCachedMessagesResult(value: unknown): CachedMessagesResult {
  const payload = isRecord(value) && isRecord(value.data) ? value.data : value
  if (!isRecord(payload)) throw new Error('邮件缓存响应格式无效')
  const messages = (Array.isArray(payload.messages) ? payload.messages : [])
    .map(normalizeCachedMessage)
    .filter((item): item is CachedMessage => item !== null)
  return {
    messages,
    count: Math.max(messages.length, Math.trunc(numberValue(payload.count, messages.length))),
    newCount: Math.max(0, Math.trunc(numberValue(payload.new_count))),
    sync: normalizeMessageSyncState(payload.sync),
		complete: bool(payload.complete),
  }
}

function managedCacheQuery(value: ManagedCacheQuery): string {
	const query = new URLSearchParams()
	if (value.mailboxId) query.set('mailbox_id', value.mailboxId)
	if (value.folder) query.set('folder', value.folder)
	if (value.after) query.set('after', value.after)
	if (value.before) query.set('before', value.before)
	if (value.query) query.set('q', value.query)
	if (value.limit) query.set('limit', String(value.limit))
	if (value.offset) query.set('offset', String(value.offset))
	return query.toString()
}

function normalizeMailboxDetail(value: unknown): MailboxDetail {
  if (!isRecord(value)) throw new Error('邮箱详情响应格式无效')
  const payload = isRecord(value.data) ? value.data : value
  const mailboxValue = isRecord(payload.mailbox) ? payload.mailbox : payload
  const mailbox = normalizeMailbox(mailboxValue)
  if (!mailbox) throw new Error('邮箱详情缺少邮箱信息')

  const aliases = (Array.isArray(payload.aliases) ? payload.aliases : mailbox.children)
    .map(normalizeAlias)
    .filter((item): item is MailboxAliasDetail => item !== null)
  const credentials = (Array.isArray(payload.credentials)
    ? payload.credentials
    : isRecord(payload.credential)
      ? [payload.credential]
      : [])
    .map(normalizeCredential)
    .filter((item): item is MailboxCredentialSummary => item !== null)
  const accounts = (Array.isArray(payload.accounts) ? payload.accounts : Array.isArray(payload.platform_accounts) ? payload.platform_accounts : [])
    .map(normalizeAccount)
    .filter((item): item is LinkedPlatformAccount => item !== null)

  return { mailbox, credentials, aliases, accounts }
}

function mailboxFormatProvider(value: unknown): MailboxFormat['provider'] {
  const normalized = text(value).trim().toLowerCase()
  if (normalized === 'microsoft' || normalized === 'gmail' || normalized === 'cloudflare_route') return normalized
  return undefined
}

function normalizeMailboxFormat(value: unknown): MailboxFormat | null {
  if (!isRecord(value)) return null
  const config = isRecord(value.config) ? value.config : value
  const id = text(value.id ?? value.format_id)
  const name = text(value.name)
  if (!id || !name) return null
  return {
    id,
    name,
    kind: formatKind(value.kind ?? config.kind),
    direction: formatDirection(value.direction ?? config.direction),
    delimiter: text(value.delimiter ?? config.delimiter, ','),
    hasHeader: bool(value.has_header ?? value.hasHeader ?? config.has_header ?? config.hasHeader, true),
    fields: formatFields(value.fields ?? value.columns ?? config.fields ?? config.columns),
    provider: mailboxFormatProvider(value.provider ?? config.provider),
    template: optionalText(value.template ?? config.template),
    builtIn: bool(value.builtin ?? value.built_in ?? value.builtIn),
    enabled: bool(value.enabled, true),
    version: numberValue(value.version, 1),
  }
}

function previewStatus(value: unknown, row: UnknownRecord): MailboxImportPreviewRow['status'] {
  if (bool(row.exists) || bool(row.duplicate) || value === 'skip' || value === 'update') return 'conflict'
  if (Array.isArray(row.errors) && row.errors.length > 0) return 'error'
  if (value === 'conflict' || value === 'error' || value === 'invalid') return value === 'conflict' ? 'conflict' : 'error'
  return 'valid'
}

function normalizeImportPreview(value: unknown): MailboxImportPreview {
  if (!isRecord(value)) throw new Error('导入预览响应格式无效')
  const payload = isRecord(value.data) ? value.data : value
  const rawRows = Array.isArray(payload.rows) ? payload.rows : Array.isArray(payload.items) ? payload.items : []
  const rows = rawRows.map((row, index): MailboxImportPreviewRow | null => {
    if (!isRecord(row)) return null
    const rawValues = isRecord(row.values) ? row.values : row
    const values = Object.fromEntries(Object.entries(rawValues).flatMap(([key, item]) => typeof item === 'string' || typeof item === 'number' ? [[key, String(item)]] : []))
    const errors = Array.isArray(row.errors) ? row.errors.map((item) => text(item)).filter(Boolean) : []
    return {
      line: numberValue(row.line ?? row.line_number, index + 1),
      status: previewStatus(row.status ?? row.action, row),
      address: optionalText(row.address ?? rawValues.address ?? rawValues.email),
      provider: optionalText(row.provider ?? rawValues.provider),
      message: optionalText(row.message ?? row.error) ?? (errors.length > 0 ? errors.join('; ') : undefined),
      values,
    }
  }).filter((row): row is MailboxImportPreviewRow => row !== null)

  return {
    rows,
    totalCount: numberValue(payload.total_rows ?? payload.total_count ?? payload.total, rows.length),
    validCount: numberValue(payload.valid_rows ?? payload.valid_count ?? payload.valid, rows.filter((row) => row.status !== 'error').length),
    conflictCount: numberValue(payload.conflict_rows ?? payload.conflict_count ?? payload.conflicts, rows.filter((row) => row.status === 'conflict').length),
    errorCount: numberValue(payload.invalid_rows ?? payload.error_count ?? payload.errors, rows.filter((row) => row.status === 'error').length),
  }
}

function normalizeBackup(value: unknown): BackupSummary {
  const raw = isRecord(value) ? value : {}
  const destinations = (Array.isArray(raw.destinations) ? raw.destinations : [])
    .map((destination): BackupDestination | null => {
      if (!isRecord(destination)) return null
      return {
        provider: backupProvider(destination.provider),
        label: text(destination.label, text(destination.provider, 'Backup')),
        status: backupStatus(destination.status),
        lastCompletedAt: text(destination.last_completed_at ?? destination.lastCompletedAt) || undefined,
        detail: optionalText(destination.detail),
      }
    })
    .filter((item): item is BackupDestination => item !== null)

  return {
    automatic: bool(raw.automatic ?? raw.enabled),
    cadenceLabel: cadenceLabel(raw.cadence_label ?? raw.schedule),
    lastCompletedAt: text(raw.last_completed_at ?? raw.lastCompletedAt) || undefined,
    nextRunAt: text(raw.next_run_at ?? raw.nextRunAt) || undefined,
    databaseSizeBytes: numberValue(raw.database_size_bytes ?? raw.databaseSizeBytes),
    destinations,
  }
}

function backupRunState(value: unknown): BackupRunState {
  if (value === 'running' || value === 'succeeded' || value === 'failed') return value
  return 'pending'
}

function backupRestoreState(value: unknown): BackupRestoreState {
  if (value === 'running' || value === 'succeeded' || value === 'failed') return value
  return 'pending'
}

function normalizeBackupTarget(value: unknown): BackupTarget | null {
  if (!isRecord(value)) return null
  const id = text(value.id).trim()
  const name = text(value.name).trim()
  if (!id || !name) return null
  const kind = backupProvider(value.kind ?? value.provider)
  const rawConfig = isRecord(value.config) ? value.config : undefined
  let config: BackupTargetConfigSummary | undefined
  if (rawConfig && kind === 'webdav') {
    const authentication = text(rawConfig.authentication)
    config = {
      kind,
      baseUrl: text(rawConfig.base_url ?? rawConfig.baseUrl),
      prefix: optionalText(rawConfig.prefix),
      authentication: authentication === 'basic' || authentication === 'bearer' ? authentication : 'none',
      usernameConfigured: bool(rawConfig.username_configured ?? rawConfig.usernameConfigured),
      insecureSkipVerify: bool(rawConfig.insecure_skip_verify ?? rawConfig.insecureSkipVerify),
    }
  } else if (rawConfig) {
    config = {
      kind: 's3',
      endpoint: optionalText(rawConfig.endpoint),
      region: optionalText(rawConfig.region),
      bucket: text(rawConfig.bucket),
      prefix: optionalText(rawConfig.prefix),
      usePathStyle: bool(rawConfig.use_path_style ?? rawConfig.usePathStyle),
      credentialsConfigured: bool(rawConfig.credentials_configured ?? rawConfig.credentialsConfigured),
      sessionTokenConfigured: bool(rawConfig.session_token_configured ?? rawConfig.sessionTokenConfigured),
    }
  }
  return {
    id,
    name,
    kind,
    enabled: bool(value.enabled, true),
    schedule: text(value.schedule),
    retentionCount: Math.max(1, Math.trunc(numberValue(value.retention_count ?? value.retentionCount, 14))),
    metadata: isRecord(value.metadata) ? value.metadata : {},
    configured: bool(value.configured, Boolean(config)),
    config,
    keyVersion: optionalText(value.key_version ?? value.keyVersion),
    version: Math.max(0, Math.trunc(numberValue(value.version))),
    createdAt: optionalText(value.created_at ?? value.createdAt),
    updatedAt: optionalText(value.updated_at ?? value.updatedAt),
  }
}

function normalizeBackupRun(value: unknown): BackupRun | null {
  if (!isRecord(value)) return null
  const id = text(value.id).trim()
  const targetId = text(value.target_id ?? value.targetId).trim()
  if (!id || !targetId) return null
  return {
    id,
    targetId,
    state: backupRunState(value.state ?? value.status),
    objectKey: optionalText(value.object_key ?? value.objectKey),
    checksum: optionalText(value.checksum),
    sizeBytes: Math.max(0, numberValue(value.size_bytes ?? value.sizeBytes)),
    error: optionalText(value.error ?? value.message),
    startedAt: optionalText(value.started_at ?? value.startedAt),
    finishedAt: optionalText(value.finished_at ?? value.finishedAt),
    createdAt: optionalText(value.created_at ?? value.createdAt),
    updatedAt: optionalText(value.updated_at ?? value.updatedAt),
  }
}

function normalizeBackupRestore(value: unknown): BackupRestoreOperation | null {
  if (!isRecord(value)) return null
  const id = text(value.id ?? value.restore_id).trim()
  const runId = text(value.run_id ?? value.runId).trim()
  if (!id || !runId) return null
  return {
    id,
    runId,
    state: backupRestoreState(value.state ?? value.status),
    error: optionalText(value.error ?? value.message),
    startedAt: optionalText(value.started_at ?? value.startedAt),
    finishedAt: optionalText(value.finished_at ?? value.finishedAt),
    createdAt: optionalText(value.created_at ?? value.createdAt),
    updatedAt: optionalText(value.updated_at ?? value.updatedAt),
  }
}

function backupConfigPayload(kind: BackupProvider, config: BackupTargetConfig): UnknownRecord {
  if (kind === 'webdav') {
    const value = config as WebDAVBackupConfig
    return {
      base_url: value.baseUrl.trim(),
      prefix: value.prefix?.trim() || undefined,
      username: value.username?.trim() || undefined,
      password: value.password || undefined,
      bearer_token: value.bearerToken || undefined,
      insecure_skip_verify: value.insecureSkipVerify || undefined,
    }
  }
  const value = config as S3BackupConfig
  return {
    endpoint: value.endpoint?.trim() || undefined,
    region: value.region?.trim() || undefined,
    bucket: value.bucket.trim(),
    prefix: value.prefix?.trim() || undefined,
    access_key_id: value.accessKeyId?.trim() || undefined,
    secret_access_key: value.secretAccessKey || undefined,
    session_token: value.sessionToken || undefined,
    use_path_style: value.usePathStyle || undefined,
  }
}

function backupTargetPayload(input: CreateBackupTargetInput | UpdateBackupTargetInput): UnknownRecord {
  return {
    name: input.name.trim(),
    kind: input.kind,
    ...(input.config ? { config: backupConfigPayload(input.kind, input.config) } : {}),
    enabled: input.enabled,
    schedule: input.schedule.trim(),
    retention_count: input.retentionCount,
    metadata: input.metadata ?? {},
    ...('version' in input ? { version: input.version } : {}),
  }
}

function normalizeDashboard(value: unknown): MailboxDashboard {
  if (!isRecord(value)) throw new Error('邮箱概览响应格式无效')

  const mailboxValues = Array.isArray(value.mailboxes)
    ? value.mailboxes
    : isRecord(value.data) && Array.isArray(value.data.mailboxes)
      ? value.data.mailboxes
      : []
  const mailboxes = mailboxValues
    .map((mailbox) => normalizeMailbox(mailbox))
    .filter((item): item is MailboxRecord => item !== null)

  const backupValue = value.backup ?? (isRecord(value.data) ? value.data.backup : undefined)
  return {
    mailboxes,
    backup: normalizeBackup(backupValue),
    updatedAt: text(value.updated_at ?? value.updatedAt, new Date().toISOString()),
  }
}

export class ApiClientError extends Error {
  readonly status: number
  readonly code?: string

  constructor(message: string, status: number, code?: string) {
    super(message)
    this.name = 'ApiClientError'
    this.status = status
    this.code = code
  }
}

async function rawRequest(path: string, init?: RequestInit): Promise<Response> {
  const response = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
      ...(API_TOKEN ? { Authorization: `Bearer ${API_TOKEN}` } : {}),
      ...init?.headers,
    },
  })

  if (!response.ok) {
    let detail = ''
    let code: string | undefined
    try {
      const payload = await response.json() as unknown
      if (isRecord(payload)) {
        detail = text(payload.message ?? payload.error)
        code = optionalText(payload.error ?? payload.code)
      }
    } catch {
      // The status code remains the stable fallback for non-JSON errors.
    }
    throw new ApiClientError(detail || `请求失败 (${response.status})`, response.status, code)
  }

  return response
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await rawRequest(path, init)
  return (await response.json()) as T
}

function formatPayload(format: Omit<MailboxFormat, 'id' | 'builtIn'>): UnknownRecord {
  return {
    name: format.name,
    kind: format.kind,
    direction: format.direction,
    delimiter: format.delimiter,
    has_header: format.hasHeader,
    fields: format.fields,
    provider: format.provider,
    template: format.template,
    enabled: format.enabled,
    version: format.version,
  }
}

function importPayload(input: MailboxImportRequest): UnknownRecord {
  return {
    format_id: input.formatId,
    data: input.content,
    conflict_strategy: input.conflictStrategy,
  }
}

export const apiClient = {
  async getMailboxDashboard(signal?: AbortSignal): Promise<DashboardResult> {
    if (import.meta.env.VITE_USE_MOCKS === 'true') {
      return { data: mockDashboard, source: 'mock' }
    }

    const controller = new AbortController()
    let timedOut = false
    const cancelFromCaller = () => controller.abort()
    signal?.addEventListener('abort', cancelFromCaller, { once: true })
    const timeoutId = globalThis.setTimeout(() => {
      timedOut = true
      controller.abort()
    }, 3_000)

    try {
      const response = await request<unknown>('/api/v1/mailboxes/overview', { signal: controller.signal })
      return { data: normalizeDashboard(response), source: 'api' }
    } catch (error) {
      if (signal?.aborted) throw error
      throw new Error(timedOut ? 'API 请求超时' : error instanceof Error ? error.message : 'API 暂时不可用')
    } finally {
      globalThis.clearTimeout(timeoutId)
      signal?.removeEventListener('abort', cancelFromCaller)
    }
  },

  async runBackup(): Promise<BackupRun> {
    const response = await request<unknown>('/api/v1/backups', { method: 'POST', body: JSON.stringify({ reason: 'manual' }) })
    const payload = isRecord(response) && isRecord(response.data) ? response.data : response
    const run = normalizeBackupRun(payload)
    if (!run) throw new Error('备份任务响应格式无效')
    return run
  },

  async getBackupTargets(signal?: AbortSignal): Promise<BackupTarget[]> {
    const response = await request<unknown>('/api/v1/backups/targets?limit=100', { signal })
    const payload = isRecord(response) && isRecord(response.data) ? response.data : response
    const items = Array.isArray(payload)
      ? payload
      : isRecord(payload) && Array.isArray(payload.items)
        ? payload.items
        : []
    return items.map(normalizeBackupTarget).filter((item): item is BackupTarget => item !== null)
  },

  async createBackupTarget(input: CreateBackupTargetInput): Promise<BackupTarget> {
    const response = await request<unknown>('/api/v1/backups/targets', {
      method: 'POST',
      body: JSON.stringify(backupTargetPayload(input)),
    })
    const payload = isRecord(response) && isRecord(response.data) ? response.data : response
    const target = normalizeBackupTarget(payload)
    if (!target) throw new Error('备份目标响应格式无效')
    return target
  },

  async updateBackupTarget(targetId: string, input: UpdateBackupTargetInput): Promise<BackupTarget> {
    const response = await request<unknown>(`/api/v1/backups/targets/${encodeURIComponent(targetId)}`, {
      method: 'PUT',
      body: JSON.stringify(backupTargetPayload(input)),
    })
    const payload = isRecord(response) && isRecord(response.data) ? response.data : response
    const target = normalizeBackupTarget(payload)
    if (!target) throw new Error('备份目标响应格式无效')
    return target
  },

  async getBackupRuns(targetId?: string, signal?: AbortSignal): Promise<BackupRun[]> {
    const query = new URLSearchParams({ limit: '100' })
    if (targetId) query.set('target_id', targetId)
    const response = await request<unknown>(`/api/v1/backups/runs?${query}`, { signal })
    const payload = isRecord(response) && isRecord(response.data) ? response.data : response
    const items = Array.isArray(payload)
      ? payload
      : isRecord(payload) && Array.isArray(payload.items)
        ? payload.items
        : []
    return items.map(normalizeBackupRun).filter((item): item is BackupRun => item !== null)
  },

  async queueBackupRun(targetId: string): Promise<BackupRun> {
    const response = await request<unknown>('/api/v1/backups/runs', {
      method: 'POST',
      body: JSON.stringify({ target_id: targetId }),
    })
    const payload = isRecord(response) && isRecord(response.data) ? response.data : response
    const run = normalizeBackupRun(payload)
    if (!run) throw new Error('备份任务响应格式无效')
    return run
  },

  async getBackupRun(runId: string, signal?: AbortSignal): Promise<BackupRun> {
    const response = await request<unknown>(`/api/v1/backups/runs/${encodeURIComponent(runId)}`, { signal })
    const payload = isRecord(response) && isRecord(response.data) ? response.data : response
    const run = normalizeBackupRun(payload)
    if (!run) throw new Error('备份任务响应格式无效')
    return run
  },

  async restoreBackupRun(runId: string): Promise<BackupRestoreOperation> {
    const response = await request<unknown>(`/api/v1/backups/runs/${encodeURIComponent(runId)}/restore`, {
      method: 'POST',
      body: JSON.stringify({ confirm: 'RESTORE' }),
    })
    const payload = isRecord(response) && isRecord(response.data) ? response.data : response
    const operation = normalizeBackupRestore(payload)
    if (!operation) throw new Error('恢复任务响应格式无效')
    return operation
  },

  async getBackupRestore(restoreId: string, signal?: AbortSignal): Promise<BackupRestoreOperation> {
    const response = await request<unknown>(`/api/v1/backups/restores/${encodeURIComponent(restoreId)}`, { signal })
    const payload = isRecord(response) && isRecord(response.data) ? response.data : response
    const operation = normalizeBackupRestore(payload)
    if (!operation) throw new Error('恢复任务响应格式无效')
    return operation
  },

  async createMailbox(mailboxProvider: MailProvider, address: string, forwardingMailboxId?: string): Promise<void> {
    if (mailboxProvider === 'cloudflare') {
      if (!forwardingMailboxId) throw new Error('请选择平台内的转发邮箱')
      await request(`/api/v1/mailboxes/${encodeURIComponent(forwardingMailboxId)}/aliases`, {
        method: 'POST',
        body: JSON.stringify({ provider: 'cloudflare_route', address, kind: 'forward' }),
      })
      return
    }
    const providerKey = mailboxProvider === 'google'
      ? 'gmail'
      : 'microsoft'
    await request('/api/v1/mailboxes', {
      method: 'POST',
      body: JSON.stringify({ provider: providerKey, address }),
    })
  },

  async issuePickupKey(mailboxId: string, label: string): Promise<string> {
    const response = await request<unknown>(`/api/v1/mailboxes/${encodeURIComponent(mailboxId)}/pickup-keys`, {
      method: 'POST',
      body: JSON.stringify({ label }),
    })
    if (!isRecord(response) || typeof response.token !== 'string' || !response.token) {
      throw new Error('取件密钥响应格式无效')
    }
    return response.token
  },

  async getMailboxDetail(mailboxId: string, signal?: AbortSignal): Promise<MailboxDetail> {
    const response = await request<unknown>(`/api/v1/mailboxes/${encodeURIComponent(mailboxId)}/detail`, { signal })
    return normalizeMailboxDetail(response)
  },

  async revealMailboxCredential(mailboxId: string, credentialType?: string): Promise<RevealedCredential> {
    const response = await request<unknown>(`/api/v1/mailboxes/${encodeURIComponent(mailboxId)}/credentials/reveal`, {
      method: 'POST',
      body: JSON.stringify(credentialType ? { credential_type: credentialType } : {}),
    })
    if (!isRecord(response)) throw new Error('凭据响应格式无效')
    const payload = isRecord(response.data) ? response.data : response
    const type = text(payload.credential_type, credentialType)
    const methods = retrievalMethods(payload.retrieval_methods)
    const genericRefreshToken = optionalText(payload.refresh_token)
    const refreshToken = genericRefreshToken
    if (!refreshToken || !type) throw new Error('凭据响应缺少令牌或类型')
    return {
      clientId: optionalText(payload.client_id),
      refreshToken,
      credentialType: type,
      retrievalMethods: methods,
      retrievalCapabilities: normalizeRetrievalCapabilities(payload.retrieval_capabilities),
      refreshTokenValidity: (() => {
        const validity = text(payload.refresh_token_validity)
      return validity === 'expiry_not_returned' || validity === 'missing' || validity === 'error' || validity === 'unknown' || validity === 'not_applicable' ? validity : undefined
      })(),
      expiresAt: optionalText(payload.expires_at),
      graphTokenExpiresAt: optionalText(payload.graph_token_expires_at),
      imapTokenExpiresAt: optionalText(payload.imap_token_expires_at),
      revealedUntil: optionalText(payload.revealed_until),
    }
  },

  async getCachedMessages(mailbox: Pick<MailboxRecord, 'id' | 'parentId'>, folder: MessageFolder, signal?: AbortSignal): Promise<CachedMessagesResult> {
    const base = mailbox.parentId
      ? `/api/v1/mailbox-aliases/${encodeURIComponent(mailbox.id)}`
      : `/api/v1/mailboxes/${encodeURIComponent(mailbox.id)}`
    const response = await request<unknown>(`${base}/cached-messages?folder=${encodeURIComponent(folder)}&limit=100`, { signal })
    return normalizeCachedMessagesResult(response)
  },

  async syncCachedMessages(mailbox: Pick<MailboxRecord, 'id' | 'parentId'>, folder: MessageFolder, method?: string): Promise<CachedMessagesResult> {
    const base = mailbox.parentId
      ? `/api/v1/mailbox-aliases/${encodeURIComponent(mailbox.id)}`
      : `/api/v1/mailboxes/${encodeURIComponent(mailbox.id)}`
    const query = new URLSearchParams({ folder, limit: '100' })
    if (method) query.set('method', method)
    const response = await request<unknown>(`${base}/messages/sync?${query.toString()}`, { method: 'POST' })
    return normalizeCachedMessagesResult(response)
  },

	async markCachedMessageViewed(mailboxId: string, messageId: string): Promise<void> {
		await request<unknown>(`/api/v1/mailboxes/${encodeURIComponent(mailboxId)}/cached-messages/${encodeURIComponent(messageId)}/viewed`, { method: 'POST' })
	},

	async queryManagedCache(input: ManagedCacheQuery, signal?: AbortSignal): Promise<ManagedCacheResult> {
		const response = await request<unknown>(`/api/v1/message-cache?${managedCacheQuery(input)}`, { signal })
		const payload = isRecord(response) && isRecord(response.data) ? response.data : response
		if (!isRecord(payload)) throw new Error('邮件缓存响应格式无效')
		return {
			messages: (Array.isArray(payload.messages) ? payload.messages : []).map(normalizeCachedMessage).filter((item): item is CachedMessage => item !== null),
			count: Math.max(0, Math.trunc(numberValue(payload.count))),
		}
	},

	async deleteManagedCache(input: ManagedCacheQuery): Promise<number> {
		const response = await request<unknown>(`/api/v1/message-cache?${managedCacheQuery(input)}`, { method: 'DELETE' })
		return isRecord(response) ? Math.max(0, Math.trunc(numberValue(response.deleted))) : 0
	},

	async restoreManagedCache(mailboxId: string, input: ManagedCacheQuery): Promise<number> {
		const response = await request<unknown>(`/api/v1/mailboxes/${encodeURIComponent(mailboxId)}/cached-messages/range?${managedCacheQuery(input)}`, { method: 'POST' })
		return isRecord(response) ? Math.max(0, Math.trunc(numberValue(response.cached))) : 0
	},

	async exportManagedCache(input: ManagedCacheQuery): Promise<void> {
		const response = await rawRequest(`/api/v1/message-cache/export?${managedCacheQuery(input)}`, { headers: { Accept: 'text/csv' } })
		const url = URL.createObjectURL(await response.blob())
		const link = document.createElement('a')
		link.href = url
		link.download = `mail-cache-${new Date().toISOString().slice(0, 10)}.csv`
		link.click()
		URL.revokeObjectURL(url)
	},

  async getProviderConnections(signal?: AbortSignal): Promise<ProviderConnection[]> {
    const response = await request<unknown>('/api/v1/provider-connections', { signal })
    const payload = isRecord(response) && isRecord(response.data) ? response.data : response
    const items = Array.isArray(payload)
      ? payload
      : isRecord(payload) && Array.isArray(payload.items)
        ? payload.items
        : []
    return items
      .map(normalizeProviderConnection)
      .filter((item): item is ProviderConnection => item !== null)
  },

  async saveProviderConnection(providerKey: string, input: SaveProviderConnectionInput): Promise<ProviderConnection> {
    const apiToken = input.apiToken.trim()
    const response = await request<unknown>(`/api/v1/provider-connections/${encodeURIComponent(providerKey)}`, {
      method: 'PUT',
      body: JSON.stringify({
        account_id: input.accountId.trim(),
        zone_id: input.zoneId.trim(),
        zone_name: input.zoneName.trim(),
        api_base_url: input.apiBaseUrl.trim(),
        ...(apiToken ? { api_token: apiToken } : {}),
        enabled: input.enabled,
        version: input.version,
      }),
    })
    const payload = isRecord(response) && isRecord(response.data) ? response.data : response
    const connection = normalizeProviderConnection(payload)
    if (!connection) throw new Error('服务连接响应格式无效')
    return connection
  },

  async getTokenRefreshSettings(signal?: AbortSignal): Promise<TokenRefreshSettings> {
    const response = await request<unknown>('/api/v1/settings/token-refresh', { signal })
    return normalizeTokenRefreshSettings(response)
  },

  async saveTokenRefreshSettings(input: SaveTokenRefreshSettingsInput): Promise<TokenRefreshSettings> {
    const response = await request<unknown>('/api/v1/settings/token-refresh', {
      method: 'PUT',
      body: JSON.stringify({
        enabled: input.enabled,
        lead_time_minutes: input.leadTimeMinutes,
        version: input.version,
      }),
    })
    return normalizeTokenRefreshSettings(response)
  },

  async getMessageProbeSettings(signal?: AbortSignal): Promise<MessageProbeSettings> {
    const response = await request<unknown>('/api/v1/settings/message-probe', { signal })
    return normalizeMessageProbeSettings(response)
  },

  async saveMessageProbeSettings(input: SaveMessageProbeSettingsInput): Promise<MessageProbeSettings> {
    const response = await request<unknown>('/api/v1/settings/message-probe', {
      method: 'PUT',
      body: JSON.stringify({
        enabled: input.enabled,
        interval_minutes: input.intervalMinutes,
        version: input.version,
      }),
    })
    return normalizeMessageProbeSettings(response)
  },

  async getMailboxFormats(signal?: AbortSignal): Promise<MailboxFormat[]> {
    const response = await request<unknown>('/api/v1/mailbox-formats', { signal })
    if (!isRecord(response) && !Array.isArray(response)) throw new Error('格式列表响应无效')
    const items = Array.isArray(response)
      ? response
      : Array.isArray(response.items)
        ? response.items
        : Array.isArray(response.formats)
          ? response.formats
          : []
    const formats = items.map(normalizeMailboxFormat).filter((item): item is MailboxFormat => item !== null)
    return formats.length > 0 ? formats : builtInMailboxFormats
  },

  async saveMailboxFormat(format: Omit<MailboxFormat, 'id' | 'builtIn'>, existingId?: string): Promise<MailboxFormat> {
    const path = existingId ? `/api/v1/mailbox-formats/${encodeURIComponent(existingId)}` : '/api/v1/mailbox-formats'
    const response = await request<unknown>(path, {
      method: existingId ? 'PUT' : 'POST',
      body: JSON.stringify(formatPayload(format)),
    })
    const payload = isRecord(response) && isRecord(response.data) ? response.data : response
    const normalized = normalizeMailboxFormat(payload)
    if (!normalized) throw new Error('格式保存响应无效')
    return normalized
  },

  async previewMailboxImport(input: MailboxImportRequest): Promise<MailboxImportPreview> {
    const response = await request<unknown>('/api/v1/mailboxes/import/preview', {
      method: 'POST',
      body: JSON.stringify(importPayload(input)),
    })
    return normalizeImportPreview(response)
  },

  async importMailboxes(input: MailboxImportRequest): Promise<MailboxImportResult> {
    const response = await request<unknown>('/api/v1/mailboxes/import', {
      method: 'POST',
      body: JSON.stringify(importPayload(input)),
    })
    if (!isRecord(response)) throw new Error('导入响应格式无效')
    const payload = isRecord(response.data) ? response.data : response
    return {
      importedCount: numberValue(payload.created ?? payload.imported_count ?? payload.imported),
      skippedCount: numberValue(payload.skipped_count ?? payload.skipped),
      updatedCount: numberValue(payload.updated_count ?? payload.updated),
      errorCount: numberValue(payload.invalid_rows ?? payload.error_count ?? payload.errors),
    }
  },

  async exportMailboxes(input: MailboxExportRequest): Promise<MailboxExportResult> {
    const response = await rawRequest('/api/v1/mailboxes/export', {
      method: 'POST',
      headers: { Accept: 'text/csv, application/json;q=0.9, text/plain;q=0.8' },
      body: JSON.stringify({
        format_id: input.formatId,
        mailbox_ids: input.mailboxIds,
        include_sensitive: input.includeSensitive,
      }),
    })
    const content = await response.text()
    const disposition = response.headers.get('content-disposition') ?? ''
    const encodedName = disposition.match(/filename\*=UTF-8''([^;]+)/i)?.[1]
    const plainName = disposition.match(/filename="?([^";]+)"?/i)?.[1]
    const fileName = encodedName ? decodeURIComponent(encodedName) : plainName || `mailboxes-${new Date().toISOString().slice(0, 10)}.csv`
    return {
      content,
      contentType: response.headers.get('content-type') || 'text/csv;charset=utf-8',
      fileName,
    }
  },

  async previewMailboxExport(input: MailboxExportRequest, signal?: AbortSignal): Promise<MailboxExportResult> {
    const response = await request<unknown>('/api/v1/mailboxes/export/preview', {
      method: 'POST', signal,
      body: JSON.stringify({ format_id: input.formatId, mailbox_ids: input.mailboxIds, include_sensitive: input.includeSensitive }),
    })
    const payload = isRecord(response) && isRecord(response.data) ? response.data : response
    if (!isRecord(payload)) throw new Error('导出预览响应格式无效')
    return {
      content: text(payload.content),
      contentType: 'text/plain;charset=utf-8',
      fileName: text(payload.filename, 'mailboxes-preview.txt'),
    }
  },
}
