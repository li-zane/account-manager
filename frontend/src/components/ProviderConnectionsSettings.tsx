import {
  CheckCircle2,
  CircleAlert,
  Clock3,
  Eye,
  EyeOff,
  LoaderCircle,
  RefreshCw,
  Save,
  ShieldCheck,
} from 'lucide-react'
import { useCallback, useEffect, useState, type FormEvent } from 'react'

import { ApiClientError, apiClient } from '../api/client'
import type { ProviderConnection } from '../api/types'
import { ProviderMark } from './ProviderMark'

const CLOUDFLARE_PROVIDER = 'cloudflare_route'
const DEFAULT_API_BASE_URL = 'https://api.cloudflare.com/client/v4'

function formatUpdatedAt(value?: string): string {
  if (!value) return '尚未更新'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '尚未更新'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(date)
}

function connectionErrorMessage(error: unknown): string {
  if (error instanceof ApiClientError && error.status === 401) return '管理员令牌缺失或已失效'
  if (error instanceof ApiClientError && error.status === 404) return '当前服务尚未启用连接配置接口'
  return error instanceof Error ? error.message : '服务连接请求失败'
}

export function ProviderConnectionsSettings() {
  const [connection, setConnection] = useState<ProviderConnection>()
  const [accountId, setAccountId] = useState('')
  const [zoneId, setZoneId] = useState('')
  const [zoneName, setZoneName] = useState('')
  const [apiBaseUrl, setApiBaseUrl] = useState(DEFAULT_API_BASE_URL)
  const [apiToken, setApiToken] = useState('')
  const [enabled, setEnabled] = useState(true)
  const [tokenVisible, setTokenVisible] = useState(false)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [loadError, setLoadError] = useState<string>()
  const [saveError, setSaveError] = useState<string>()
  const [savedMessage, setSavedMessage] = useState<string>()
  const [versionConflict, setVersionConflict] = useState(false)

  const applyConnection = useCallback((value?: ProviderConnection) => {
    setConnection(value)
    setAccountId(value?.accountId ?? '')
    setZoneId(value?.zoneId ?? '')
    setZoneName(value?.zoneName ?? '')
    setApiBaseUrl(value?.apiBaseUrl ?? DEFAULT_API_BASE_URL)
    setEnabled(value?.enabled ?? true)
    setApiToken('')
    setTokenVisible(false)
  }, [])

  const loadConnections = useCallback(async (signal?: AbortSignal) => {
    setLoading(true)
    setLoadError(undefined)
    setSaveError(undefined)
    setSavedMessage(undefined)
    setVersionConflict(false)
    try {
      const connections = await apiClient.getProviderConnections(signal)
      if (signal?.aborted) return
      const cloudflare = connections.find((item) => item.provider === CLOUDFLARE_PROVIDER || item.provider === 'cloudflare')
      applyConnection(cloudflare)
    } catch (error) {
      if (!signal?.aborted) setLoadError(connectionErrorMessage(error))
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }, [applyConnection])

  useEffect(() => {
    const controller = new AbortController()
    void loadConnections(controller.signal)
    return () => controller.abort()
  }, [loadConnections])

  const clearFeedback = () => {
    setSaveError(undefined)
    setSavedMessage(undefined)
    setVersionConflict(false)
  }

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (saving) return
    setSaving(true)
    setSaveError(undefined)
    setSavedMessage(undefined)
    setVersionConflict(false)
    try {
      const saved = await apiClient.saveProviderConnection(CLOUDFLARE_PROVIDER, {
        accountId,
        zoneId,
        zoneName,
        apiBaseUrl,
        apiToken,
        enabled,
        version: connection?.version ?? 0,
      })
      applyConnection(saved)
      setSavedMessage('连接配置已保存')
    } catch (error) {
      if (error instanceof ApiClientError && (error.status === 409 || error.code === 'conflict')) {
        setVersionConflict(true)
        setSaveError('服务器上的配置已被其他请求更新，请重新读取后再提交')
      } else {
        setSaveError(connectionErrorMessage(error))
      }
    } finally {
      setSaving(false)
    }
  }

  const configured = Boolean(connection?.configured)
  const formUnavailable = Boolean(loadError && !connection)

  return (
    <>
      <div className="section-heading provider-connection-heading">
        <div><p className="eyebrow">SERVICE CONNECTIONS</p><h2>服务连接</h2></div>
        <button className="icon-button icon-button--small" type="button" title="重新读取连接状态" aria-label="重新读取 Cloudflare 连接状态" onClick={() => void loadConnections()} disabled={loading || saving}>
          <RefreshCw className={loading ? 'spin' : ''} />
        </button>
      </div>

      {loading && !connection && (
        <div className="provider-connection-loading" role="status"><LoaderCircle className="spin" /><span>正在读取 Cloudflare 连接</span></div>
      )}

      {loadError && (
        <div className="connection-feedback connection-feedback--error" role="alert">
          <CircleAlert />
          <span><strong>连接信息加载失败</strong><small>{loadError}</small></span>
          <button className="secondary-button" type="button" onClick={() => void loadConnections()} disabled={loading}><RefreshCw />重试</button>
        </div>
      )}

      {!formUnavailable && !(loading && !connection) && (
        <>
          <div className="provider-connection-status" aria-label="Cloudflare 连接状态">
            <div className={configured ? 'connection-status-item connection-status-item--ready' : 'connection-status-item connection-status-item--pending'}>
              {configured ? <ShieldCheck /> : <CircleAlert />}
              <span><small>配置</small><strong>{configured ? '已配置凭据' : '等待配置'}</strong></span>
            </div>
            <div className={connection?.enabled ? 'connection-status-item connection-status-item--ready' : 'connection-status-item'}>
              <CheckCircle2 />
              <span><small>运行状态</small><strong>{connection ? (connection.enabled ? '已启用' : '已停用') : '尚未创建'}</strong></span>
            </div>
            <div className="connection-status-item">
              <Clock3 />
              <span><small>最后更新</small><strong>{formatUpdatedAt(connection?.updatedAt)}</strong></span>
            </div>
            <div className="connection-status-item">
              <RefreshCw />
              <span><small>配置版本</small><strong>v{connection?.version ?? 0}</strong></span>
            </div>
          </div>

          <form className="provider-connection-form" onSubmit={submit}>
            <div className="provider-connection-form__intro">
              <ProviderMark provider="cloudflare" />
              <span><strong>Cloudflare Email Routing</strong><small>{connection?.name || 'default'} 连接</small></span>
            </div>

            <div className="connection-form-grid">
              <label className="connection-form-field">
                <span><strong>Account ID</strong><small>Cloudflare 账户标识</small></span>
                <input value={accountId} onChange={(event) => { setAccountId(event.target.value); clearFeedback() }} autoComplete="off" spellCheck={false} required disabled={saving || loading} />
              </label>
              <label className="connection-form-field">
                <span><strong>Zone ID</strong><small>邮箱域名所在区域</small></span>
                <input value={zoneId} onChange={(event) => { setZoneId(event.target.value); clearFeedback() }} autoComplete="off" spellCheck={false} required disabled={saving || loading} />
              </label>
              <label className="connection-form-field">
                <span><strong>Zone Name</strong><small>例如 rainynight.me</small></span>
                <input value={zoneName} onChange={(event) => { setZoneName(event.target.value); clearFeedback() }} autoComplete="off" spellCheck={false} placeholder="rainynight.me" disabled={saving || loading} />
              </label>
              <label className="connection-form-field">
                <span><strong>API Base URL</strong><small>Cloudflare API 入口</small></span>
                <input type="url" value={apiBaseUrl} onChange={(event) => { setApiBaseUrl(event.target.value); clearFeedback() }} autoComplete="url" spellCheck={false} placeholder={DEFAULT_API_BASE_URL} disabled={saving || loading} />
              </label>
              <label className="connection-form-field connection-form-field--wide">
                <span><strong>API Token</strong><small>{configured ? '留空会保留当前令牌' : '首次保存时需要填写'}</small></span>
                <span className="connection-secret-input">
                  <input type={tokenVisible ? 'text' : 'password'} value={apiToken} onChange={(event) => { setApiToken(event.target.value); clearFeedback() }} autoComplete="new-password" spellCheck={false} required={!configured} disabled={saving || loading} aria-label="Cloudflare API Token" />
                  <button className="icon-button icon-button--small" type="button" title={tokenVisible ? '隐藏 API Token' : '显示 API Token'} aria-label={tokenVisible ? '隐藏 Cloudflare API Token' : '显示 Cloudflare API Token'} onClick={() => setTokenVisible((value) => !value)} disabled={!apiToken || saving}>
                    {tokenVisible ? <EyeOff /> : <Eye />}
                  </button>
                </span>
              </label>
            </div>

            {saveError && (
              <div className={versionConflict ? 'connection-feedback connection-feedback--conflict' : 'connection-feedback connection-feedback--error'} role="alert">
                <CircleAlert />
                <span><strong>{versionConflict ? '版本冲突' : '连接保存失败'}</strong><small>{saveError}</small></span>
                {versionConflict && <button className="secondary-button" type="button" onClick={() => void loadConnections()} disabled={loading}><RefreshCw />重新读取</button>}
              </div>
            )}
            {savedMessage && <div className="connection-feedback connection-feedback--success" role="status"><CheckCircle2 /><span><strong>{savedMessage}</strong><small>API Token 输入已清空</small></span></div>}

            <div className="provider-connection-form__footer">
              <div className="connection-enabled-control">
                <span><strong>启用连接</strong><small>控制 Cloudflare 邮箱配置与路由同步</small></span>
                <button className={`toggle ${enabled ? 'toggle--checked' : ''}`} type="button" role="switch" aria-checked={enabled} aria-label="启用 Cloudflare 连接" onClick={() => { setEnabled((value) => !value); clearFeedback() }} disabled={saving || loading}><span /></button>
              </div>
              <div className="provider-connection-form__actions">
                <button className="secondary-button" type="button" onClick={() => void loadConnections()} disabled={loading || saving}><RefreshCw />重载</button>
                <button className="primary-button" type="submit" disabled={loading || saving}>{saving ? <LoaderCircle className="spin" /> : <Save />}{saving ? '正在保存' : '保存连接'}</button>
              </div>
            </div>
          </form>
        </>
      )}
    </>
  )
}
