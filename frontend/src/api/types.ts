export type MailProvider = 'microsoft' | 'cloudflare' | 'google'
export type MailboxKind = 'primary' | 'split'
export type MailboxHealth = 'healthy' | 'attention' | 'offline'
export type RetrievalKeyStatus = 'ready' | 'expiring' | 'expired' | 'missing' | 'not_applicable'
export type BackupProvider = 's3' | 'webdav'
export type BackupDestinationStatus = 'synced' | 'pending' | 'error' | 'disabled'
export type MailAccessMode = 'graph' | 'imap' | 'oauth' | 'forward'

export interface RetrievalKeySummary {
  status: RetrievalKeyStatus
  maskedKey?: string
  expiresAt?: string
  issuedAt?: string
}

export interface MailAuthSummary {
  modes: MailAccessMode[]
  refreshTokenExpiresAt?: string
  autoRefresh: boolean
}

export interface ForwardingSummary {
  target: string
  verified: boolean
}

export interface MailboxRecord {
  id: string
  parentId?: string
  kind: MailboxKind
  provider: MailProvider
  address: string
  displayName?: string
  health: MailboxHealth
  retrievalKey: RetrievalKeySummary
  auth: MailAuthSummary
  forwarding?: ForwardingSummary
  lastMessageAt?: string
  createdAt?: string
  children: MailboxRecord[]
}

export interface MailboxCredentialSummary {
  id?: string
  credentialType: string
  clientId?: string
  retrievalMethods: string[]
  maskedRefreshToken?: string
  hasRefreshToken: boolean
  maskedGraphRefreshToken?: string
  hasGraphRefreshToken: boolean
  maskedImapRefreshToken?: string
  hasImapRefreshToken: boolean
  expiresAt?: string
  graphTokenExpiresAt?: string
  imapTokenExpiresAt?: string
  refreshAfter?: string
  refreshStatus: string
  lastRefreshedAt?: string
  lastRefreshError?: string
  autoRefresh: boolean
}

export interface MailboxAliasDetail {
  id: string
  address: string
  provider: MailProvider
  kind: 'split' | 'forward'
  enabled: boolean
}

export interface LinkedPlatformAccount {
  id: string
  platform: string
  loginAddress: string
  status: string
  mailboxAliasId?: string
}

export interface MailboxDetail {
  mailbox: MailboxRecord
  credentials: MailboxCredentialSummary[]
  aliases: MailboxAliasDetail[]
  accounts: LinkedPlatformAccount[]
}

export interface RevealedCredential {
  clientId?: string
  refreshToken: string
  graphRefreshToken?: string
  imapRefreshToken?: string
  credentialType: string
  retrievalMethods: string[]
  expiresAt?: string
  graphTokenExpiresAt?: string
  imapTokenExpiresAt?: string
  revealedUntil?: string
}

export interface ProviderConnection {
  id?: string
  provider: string
  name?: string
  accountId?: string
  zoneId?: string
  zoneName?: string
  apiBaseUrl?: string
  configured: boolean
  enabled: boolean
  version: number
  createdAt?: string
  updatedAt?: string
}

export interface SaveProviderConnectionInput {
  accountId: string
  zoneId: string
  zoneName: string
  apiBaseUrl: string
  apiToken: string
  enabled: boolean
  version: number
}

export interface TokenRefreshSettings {
  enabled: boolean
  leadTimeMinutes: number
  version: number
  updatedAt?: string
}

export interface SaveTokenRefreshSettingsInput {
  enabled: boolean
  leadTimeMinutes: number
  version: number
}

export type MailboxFormatKind = 'delimited' | 'json' | 'template'
export type MailboxFormatDirection = 'import' | 'export' | 'both'

export interface MailboxFormatField {
  column: string
  target: string
  required?: boolean
  sensitive?: boolean
  default?: string
}

export interface MailboxFormat {
  id: string
  name: string
  kind: MailboxFormatKind
  direction: MailboxFormatDirection
  delimiter: string
  hasHeader: boolean
  fields: MailboxFormatField[]
  provider?: string
  template?: string
  builtIn: boolean
  enabled: boolean
  version: number
}

export type ImportConflictStrategy = 'skip' | 'update' | 'error'

export interface MailboxImportPreviewRow {
  line: number
  status: 'valid' | 'conflict' | 'error'
  address?: string
  provider?: string
  message?: string
  values: Record<string, string>
}

export interface MailboxImportPreview {
  rows: MailboxImportPreviewRow[]
  totalCount: number
  validCount: number
  conflictCount: number
  errorCount: number
}

export interface MailboxImportRequest {
  formatId: string
  content: string
  conflictStrategy: ImportConflictStrategy
  fileName?: string
}

export interface MailboxImportResult {
  importedCount: number
  skippedCount: number
  updatedCount: number
  errorCount: number
}

export interface MailboxExportRequest {
  formatId: string
  mailboxIds: string[]
  includeSensitive: boolean
}

export interface MailboxExportResult {
  content: string
  contentType: string
  fileName: string
}

export interface BackupDestination {
  provider: BackupProvider
  label: string
  status: BackupDestinationStatus
  lastCompletedAt?: string
  detail?: string
}

export interface BackupSummary {
  automatic: boolean
  cadenceLabel: string
  lastCompletedAt?: string
  nextRunAt?: string
  databaseSizeBytes: number
  destinations: BackupDestination[]
}

export interface S3BackupConfig {
  endpoint?: string
  region?: string
  bucket: string
  prefix?: string
  accessKeyId?: string
  secretAccessKey?: string
  sessionToken?: string
  usePathStyle?: boolean
}

export interface WebDAVBackupConfig {
  baseUrl: string
  prefix?: string
  username?: string
  password?: string
  bearerToken?: string
  insecureSkipVerify?: boolean
}

export type BackupTargetConfig = S3BackupConfig | WebDAVBackupConfig

export interface S3BackupConfigSummary {
  kind: 's3'
  endpoint?: string
  region?: string
  bucket: string
  prefix?: string
  usePathStyle: boolean
  credentialsConfigured: boolean
  sessionTokenConfigured: boolean
}

export interface WebDAVBackupConfigSummary {
  kind: 'webdav'
  baseUrl: string
  prefix?: string
  authentication: 'none' | 'basic' | 'bearer'
  usernameConfigured: boolean
  insecureSkipVerify: boolean
}

export type BackupTargetConfigSummary = S3BackupConfigSummary | WebDAVBackupConfigSummary

export interface BackupTarget {
  id: string
  name: string
  kind: BackupProvider
  enabled: boolean
  schedule: string
  retentionCount: number
  metadata: Record<string, unknown>
  configured: boolean
  config?: BackupTargetConfigSummary
  keyVersion?: string
  version: number
  createdAt?: string
  updatedAt?: string
}

export type BackupRunState = 'pending' | 'running' | 'succeeded' | 'failed'

export interface BackupRun {
  id: string
  targetId: string
  state: BackupRunState
  objectKey?: string
  checksum?: string
  sizeBytes: number
  error?: string
  startedAt?: string
  finishedAt?: string
  createdAt?: string
  updatedAt?: string
}

export interface CreateBackupTargetInput {
  name: string
  kind: BackupProvider
  config: BackupTargetConfig
  enabled: boolean
  schedule: string
  retentionCount: number
  metadata?: Record<string, unknown>
}

export interface UpdateBackupTargetInput {
  name: string
  kind: BackupProvider
  config?: BackupTargetConfig
  enabled: boolean
  schedule: string
  retentionCount: number
  metadata?: Record<string, unknown>
  version: number
}

export type BackupRestoreState = 'pending' | 'running' | 'succeeded' | 'failed'

export interface BackupRestoreOperation {
  id: string
  runId: string
  state: BackupRestoreState
  error?: string
  startedAt?: string
  finishedAt?: string
  createdAt?: string
  updatedAt?: string
}

export interface MailboxDashboard {
  mailboxes: MailboxRecord[]
  backup: BackupSummary
  updatedAt: string
}

export interface DashboardResult {
  data: MailboxDashboard
  source: 'api' | 'mock'
  warning?: string
}
