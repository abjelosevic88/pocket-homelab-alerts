// Left column: the add button, sections with their counts, and whether quiet hours are on — the Pocket Calendar sidebar.
import { useState } from 'react'
import { NavLink } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import type { Dashboard } from '../lib/types'
import { Icon } from './icons'
import { createItems, type CreateKind } from './Layout'
import { type Anchor, Menu, rectOf } from './ui'

export function Sidebar({ onCreate, onNavigate, counts }: { onCreate: (k: CreateKind) => void; onNavigate: () => void; counts: Dashboard | null }) {
  const { status, settings } = useApp()
  const [menu, setMenu] = useState<Anchor | null>(null)
  const title = settings?.title || status?.title || 'Pocket Alerts'
  const c = counts?.counts
  const nav: { to: string; label: string; icon: string; end?: boolean; count?: number; warn?: boolean }[] = [
    { to: '/', label: 'Inbox', icon: 'inbox', end: true, count: c?.unread },
    { to: '/sources', label: 'Sources', icon: 'antenna', warn: !!counts?.sources_failing },
    { to: '/send-to', label: 'Send to', icon: 'smartphone', warn: !!counts?.channels_failing },
    { to: '/rules', label: 'Rules', icon: 'sliders' },
    { to: '/quiet', label: 'Quiet hours', icon: 'moon', count: c?.held },
    { to: '/activity', label: 'Activity', icon: 'chart' },
  ]
  return (
    <aside className="sidebar" aria-label="Pocket Alerts">
      <div className="sb-brand">
        <img src="/favicon.svg" alt="" className="sb-logo" />
        <span className="sb-title">{title !== 'Pocket Alerts' ? title : <>Pocket <b>Alerts</b></>}</span>
        {status?.version && <span className="sb-version" title={`Version ${status.version}`}>{/^v?\d+\.\d+/.test(status.version) ? 'v' + status.version.replace(/^v/, '').replace(/[-+].*$/, '') : status.version}</span>}
      </div>
      <button type="button" className="btn primary create-btn" aria-haspopup="menu" aria-expanded={!!menu} onClick={e => setMenu(menu ? null : rectOf(e.currentTarget))}>
        <Icon name="plus" size={15} strokeWidth={2.2} />Add<Icon name="chevron-down" size={15} className="create-caret" />
      </button>
      {menu && <Menu anchor={menu} items={createItems(onCreate)} onClose={() => setMenu(null)} width={290} />}

      <nav className="sb-nav" aria-label="Sections">
        {nav.map(n => (
          <NavLink key={n.to} to={n.to} end={n.end} onClick={onNavigate} className={({ isActive }) => `sb-nav-item ${isActive ? 'active' : ''}`}>
            <Icon name={n.icon} size={16} />{n.label}
            {n.warn && <span className="sb-warn" title="Something here is not working"><Icon name="alert" size={13} /></span>}
            {!!n.count && <span className="sb-count">{n.count > 999 ? '999+' : n.count}</span>}
          </NavLink>
        ))}
      </nav>

      {counts && (
        <section className="sb-sec" aria-label="Right now">
          <div className={`sb-quiet ${counts.quiet ? 'on' : ''}`}>
            <Icon name={counts.quiet ? 'moon' : 'bell'} size={15} />
            <span>
              {counts.quiet ? <>Quiet until <b>{counts.quiet_until}</b></> : counts.channels === 0 ? 'Nothing goes to your phone yet' : <>Alerts reach you</>}
              {counts.quiet && counts.counts.held > 0 && <span className="muted"> · {counts.counts.held} waiting</span>}
            </span>
          </div>
        </section>
      )}

      <div className="sb-foot">
        <NavLink to="/settings" className="sb-foot-link" onClick={onNavigate}><Icon name="settings" size={15} />Settings</NavLink>
        <button type="button" className="sb-foot-link" onClick={() => { onNavigate(); window.dispatchEvent(new CustomEvent('pal:shortcuts')) }}><Icon name="keyboard" size={15} />Shortcuts</button>
      </div>
    </aside>
  )
}
