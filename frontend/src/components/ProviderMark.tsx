import { AtSign, Cloud, Grid2X2, type LucideIcon } from 'lucide-react'

import type { MailProvider } from '../api/types'

interface ProviderMeta {
  label: string
  shortLabel: string
  icon: LucideIcon
}

export const providerMeta: Record<MailProvider, ProviderMeta> = {
  microsoft: { label: 'Microsoft', shortLabel: 'MS', icon: Grid2X2 },
  cloudflare: { label: 'Cloudflare 域名', shortLabel: 'CF', icon: Cloud },
  google: { label: 'Google', shortLabel: 'G', icon: AtSign },
}

interface ProviderMarkProps {
  provider: MailProvider
  size?: 'small' | 'medium'
  showLabel?: boolean
}

export function ProviderMark({ provider, size = 'medium', showLabel = false }: ProviderMarkProps) {
  const meta = providerMeta[provider]
  const Icon = meta.icon

  return (
    <span className={`provider-wrap ${showLabel ? 'provider-wrap--labeled' : ''}`}>
      <span className={`provider-mark provider-mark--${provider} provider-mark--${size}`} aria-hidden="true">
        <Icon strokeWidth={2.2} />
      </span>
      {showLabel && <span>{meta.label}</span>}
    </span>
  )
}
