import {
  Bell,
  Bot,
  CircleHelp,
  Inbox,
  PanelsTopLeft,
  Settings2,
  Sparkles,
} from 'lucide-react'
import type { ReactNode } from 'react'

export type ViewKey = 'mailboxes' | 'workspace' | 'settings'
export type WorkspacePlatform = 'chatgpt' | 'grok'

interface AppShellProps {
  children: ReactNode
  currentView: ViewKey
  onViewChange: (view: ViewKey) => void
  onOpenWorkspace: (platform: WorkspacePlatform) => void
  source: 'api' | 'mock'
}

const navigation = [
  { key: 'mailboxes' as const, label: '邮箱管理', icon: Inbox },
  { key: 'workspace' as const, label: '工作区', icon: PanelsTopLeft },
  { key: 'settings' as const, label: '设置', icon: Settings2 },
]

export function AppShell({ children, currentView, onViewChange, onOpenWorkspace, source }: AppShellProps) {
  return (
    <div className="app-shell">
      <aside className="sidebar" aria-label="主导航">
        <button className="brand" type="button" onClick={() => onViewChange('mailboxes')}>
          <span className="brand__stamp" aria-hidden="true">
            <span className="brand__stamp-fold" />
          </span>
          <span className="brand__copy">
            <strong>账号管理台</strong>
            <small>ACCOUNT MANAGER</small>
          </span>
        </button>

        <nav className="sidebar-nav">
          <p className="nav-label">管理台</p>
          {navigation.map(({ key, label, icon: Icon }) => (
            <button
              className={`nav-item ${currentView === key ? 'nav-item--active' : ''}`}
              type="button"
              key={key}
              onClick={() => onViewChange(key)}
              aria-current={currentView === key ? 'page' : undefined}
            >
              <Icon aria-hidden="true" />
              <span>{label}</span>
            </button>
          ))}
        </nav>

        <div className="pinned-workspaces">
          <div className="pinned-workspaces__title">
            <p className="nav-label">固定工作区</p>
            <span className="pin-dot" title="已固定" />
          </div>
          <button className="workspace-link" type="button" onClick={() => onOpenWorkspace('chatgpt')}>
            <span className="workspace-icon workspace-icon--chatgpt">
              <Sparkles aria-hidden="true" />
            </span>
            <span>
              <strong>ChatGPT</strong>
              <small>12 个账号</small>
            </span>
          </button>
          <button className="workspace-link" type="button" onClick={() => onOpenWorkspace('grok')}>
            <span className="workspace-icon workspace-icon--grok">
              <Bot aria-hidden="true" />
            </span>
            <span>
              <strong>Grok</strong>
              <small>5 个账号</small>
            </span>
          </button>
        </div>

        <div className="sidebar-footer">
          <button className="help-link" type="button" title="帮助中心">
            <CircleHelp aria-hidden="true" />
            <span>帮助中心</span>
          </button>
          <div className="account-card">
            <span className="account-avatar" aria-hidden="true">Z</span>
            <span>
              <strong>管理员</strong>
              <small>{source === 'api' ? '生产环境' : '本地预览'}</small>
            </span>
            <span className={`environment-dot environment-dot--${source}`} title={source === 'api' ? 'API 已连接' : 'Mock 数据'} />
          </div>
        </div>
      </aside>

      <header className="mobile-header">
        <button className="brand brand--mobile" type="button" onClick={() => onViewChange('mailboxes')}>
          <span className="brand__stamp" aria-hidden="true"><span className="brand__stamp-fold" /></span>
          <span className="brand__copy"><strong>账号管理台</strong></span>
        </button>
        <button className="icon-button" type="button" title="通知" aria-label="通知">
          <Bell />
          <span className="notification-dot" />
        </button>
      </header>

      <main className="main-content">{children}</main>

      <nav className="mobile-nav" aria-label="移动端主导航">
        {navigation.map(({ key, label, icon: Icon }) => (
          <button
            className={currentView === key ? 'mobile-nav__item mobile-nav__item--active' : 'mobile-nav__item'}
            type="button"
            key={key}
            onClick={() => onViewChange(key)}
            aria-current={currentView === key ? 'page' : undefined}
          >
            <Icon aria-hidden="true" />
            <span>{label}</span>
          </button>
        ))}
      </nav>
    </div>
  )
}
