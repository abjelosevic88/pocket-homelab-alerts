// The app shell: sidebar, top bar, search palette, shortcuts, live updates — the Pocket Calendar layout.
import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import type { Dashboard, LiveEvent } from '../lib/types'
import { Icon } from './icons'
import { Sidebar } from './Sidebar'
import { CommandPalette, type PaletteCommand } from './CommandPalette'
import { ShortcutsSheet } from './ShortcutsSheet'
import { Avatar, type Anchor, type MenuItem, Menu, MenuButton, modalOpen, rectOf, useIsMobile, useMediaQuery } from './ui'
import { AskButton } from './AskButton'

const SIDEBAR_KEY = 'pal-sidebar'

/** Something was added or changed from the shell (or a page): the counts and the pages reload. */
export const announceChange = () => window.dispatchEvent(new CustomEvent('pal:changed'))
export function useOnChange(cb: () => void) {
  const ref = useRef(cb)
  ref.current = cb
  useEffect(() => {
    const h = () => ref.current()
    window.addEventListener('pal:changed', h)
    return () => window.removeEventListener('pal:changed', h)
  }, [])
}

/** Live updates from the server (an alert arrived or changed, a source's
 *  connection changed). The shell keeps one connection open for every page. */
export function useLive(cb: (e: LiveEvent) => void) {
  const ref = useRef(cb)
  ref.current = cb
  useEffect(() => {
    const h = (e: Event) => ref.current((e as CustomEvent<LiveEvent>).detail)
    window.addEventListener('pal:live', h)
    return () => window.removeEventListener('pal:live', h)
  }, [])
}

/** The page's own name for the top bar breadcrumb. */
const CrumbCtx = createContext<(t: string) => void>(() => {})
export function useCrumb(title: string | undefined) {
  const set = useContext(CrumbCtx)
  useEffect(() => { set(title || ''); return () => set('') }, [title, set])
}

/** The inbox numbers (unread, held, quiet hours) for every page that wants them. */
const CountsCtx = createContext<Dashboard | null>(null)
export const useCounts = () => useContext(CountsCtx)

export type CreateKind = 'source' | 'channel' | 'rule'

/** Open a create action from anywhere: each page opens its own editor on ?new=1. */
export const openCreate = (k: CreateKind) => window.dispatchEvent(new CustomEvent('pal:create', { detail: { kind: k } }))

const CREATE_PATH: Record<CreateKind, string> = { source: '/sources?new=1', channel: '/send-to?new=1', rule: '/rules?new=1' }

/** The add menu: the sidebar's "Add" button and the phone's + button. */
export function createItems(onCreate: (k: CreateKind) => void): MenuItem[] {
  return [
    { label: 'A source (where alerts come from)…', icon: 'inbox', hint: 'N', onSelect: () => onCreate('source') },
    { label: 'A place to send them to…', icon: 'smartphone', hint: 'C', onSelect: () => onCreate('channel') },
    { label: 'A rule…', icon: 'sliders', hint: 'M', onSelect: () => onCreate('rule') },
  ]
}

export default function Layout() {
  const navigate = useNavigate()
  const loc = useLocation()

  const mobile = useIsMobile()
  const narrow = useMediaQuery('(max-width: 1099px)')
  const drawer = mobile || narrow
  const [sidebarOpen, setSidebarOpen] = useState(() => !drawer && localStorage.getItem(SIDEBAR_KEY) !== '0')
  useEffect(() => { setSidebarOpen(!drawer && localStorage.getItem(SIDEBAR_KEY) !== '0') }, [drawer])
  const drawerOpenedAt = useRef(0)
  const toggleSidebar = () => setSidebarOpen(o => { drawerOpenedAt.current = performance.now(); const n = !o; if (!drawer) localStorage.setItem(SIDEBAR_KEY, n ? '1' : '0'); return n })
  const closeDrawer = () => { if (drawer) setSidebarOpen(false) }
  useEffect(() => { if (!loc.pathname.startsWith('/alerts/')) document.querySelector('.content')?.scrollTo({ top: 0 }) }, [loc.pathname])

  const [crumb, setCrumbRaw] = useState('')
  const setCrumb = useCallback((t: string) => setCrumbRaw(t), [])

  const [counts, setCounts] = useState<Dashboard | null>(null)
  const load = useCallback(() => { api.get<Dashboard>(`${V1}/dashboard`).then(setCounts).catch(() => {}) }, [])
  useEffect(() => { load() }, [loc.pathname, load])
  useOnChange(load)

  // one live connection for all pages; the counts follow it (at most every 2 s)
  const pending = useRef<number | null>(null)
  useEffect(() => {
    let es: EventSource | null = null
    let retry: number | undefined
    const open = () => {
      es = new EventSource('/api/live')
      es.onmessage = m => {
        try {
          const e = JSON.parse(m.data) as LiveEvent
          window.dispatchEvent(new CustomEvent('pal:live', { detail: e }))
          if (pending.current == null) pending.current = window.setTimeout(() => { pending.current = null; load() }, 2000)
        } catch { /* a ping */ }
      }
      es.onerror = () => { es?.close(); retry = window.setTimeout(open, 8000) }
    }
    open()
    // the page was asleep (phone locked): catch up
    const wake = () => { if (document.visibilityState === 'visible') { load(); window.dispatchEvent(new CustomEvent('pal:live', { detail: { type: 'refresh' } })) } }
    document.addEventListener('visibilitychange', wake)
    return () => { es?.close(); clearTimeout(retry); document.removeEventListener('visibilitychange', wake); if (pending.current) clearTimeout(pending.current) }
  }, [load])

  // the unread count in the tab title
  useEffect(() => {
    const n = counts?.counts.unread || 0
    document.title = n > 0 ? `(${n}) Pocket Alerts` : 'Pocket Alerts'
  }, [counts])

  const [palette, setPalette] = useState(false)
  const [shortcuts, setShortcuts] = useState(false)
  const [fabMenu, setFabMenu] = useState<Anchor | null>(null)

  const create = (k: CreateKind) => { closeDrawer(); navigate(CREATE_PATH[k]) }
  const createRef = useRef(create)
  createRef.current = create

  useEffect(() => {
    const open = () => setShortcuts(true)
    const make = (e: Event) => { const d = (e as CustomEvent<{ kind: CreateKind }>).detail; createRef.current(d.kind) }
    window.addEventListener('pal:shortcuts', open)
    window.addEventListener('pal:create', make)
    return () => { window.removeEventListener('pal:shortcuts', open); window.removeEventListener('pal:create', make) }
  }, [])

  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); setPalette(p => !p); return }
      if (e.metaKey || e.ctrlKey || e.altKey) return
      if (t.closest('input, textarea, select, [contenteditable="true"]') || modalOpen() || document.querySelector('.menu, .popover, .sel-pop')) return
      const go = (to: string) => { e.preventDefault(); navigate(to) }
      const mk = (k: CreateKind) => { e.preventDefault(); createRef.current(k) }
      switch (e.key) {
        case '/': e.preventDefault(); setPalette(true); break
        case '?': e.preventDefault(); setShortcuts(true); break
        case 'n': mk('source'); break
        case 'c': mk('channel'); break
        case 'm': mk('rule'); break
        case 'i': go('/'); break
        case 's': go('/sources'); break
        case 't': go('/send-to'); break
        case 'r': go('/rules'); break
        case 'q': go('/quiet'); break
        case 'y': go('/activity'); break
        case ',': go('/settings'); break
        case '[': e.preventDefault(); toggleSidebar(); break
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }) // eslint-disable-line react-hooks/exhaustive-deps

  const commands: PaletteCommand[] = [
    { id: 'inbox', label: 'Inbox', icon: 'inbox', hint: 'I', keywords: 'alerts unread home', run: () => navigate('/') },
    { id: 'important', label: 'Important alerts', icon: 'alert', keywords: 'high urgent', run: () => navigate('/?view=important') },
    { id: 'done', label: 'Done alerts', icon: 'check-circle', keywords: 'archive old', run: () => navigate('/?view=done') },
    { id: 'markall', label: 'Mark everything read', icon: 'check', keywords: 'read all clear', run: () => { api.post(`${V1}/alerts/mark`, { what: 'read' }).then(announceChange).catch(() => {}) } },
    { id: 'sources', label: 'Sources', icon: 'antenna', hint: 'S', keywords: 'where from apprise ntfy webhook topics', run: () => navigate('/sources') },
    { id: 'source', label: 'Add a source', icon: 'plus', hint: 'N', keywords: 'new address apprise ntfy follow', run: () => create('source') },
    { id: 'sendto', label: 'Send to', icon: 'smartphone', hint: 'T', keywords: 'channels pushover ntfy phone apprise webhook', run: () => navigate('/send-to') },
    { id: 'channel', label: 'Add a place to send to', icon: 'plus', hint: 'C', keywords: 'new channel pushover ntfy phone', run: () => create('channel') },
    { id: 'rules', label: 'Rules', icon: 'sliders', hint: 'R', keywords: 'filter mute route', run: () => navigate('/rules') },
    { id: 'rule', label: 'Make a rule', icon: 'plus', hint: 'M', keywords: 'new filter', run: () => create('rule') },
    { id: 'quiet', label: 'Quiet hours', icon: 'moon', hint: 'Q', keywords: 'night sleep do not disturb summary', run: () => navigate('/quiet') },
    { id: 'activity', label: 'Activity', icon: 'chart', hint: 'Y', keywords: 'history stats chart deliveries', run: () => navigate('/activity') },
    { id: 'apps', label: 'Settings: connected apps', icon: 'layers', keywords: 'tasks core integrations', run: () => navigate('/settings/apps') },
    { id: 'settings', label: 'Settings', icon: 'settings', hint: ',', run: () => navigate('/settings') },
    { id: 'shortcuts', label: 'Keyboard shortcuts', icon: 'keyboard', hint: '?', run: () => setShortcuts(true) },
  ]

  return (
    <CrumbCtx.Provider value={setCrumb}>
      <CountsCtx.Provider value={counts}>
        <div className={`app ${sidebarOpen ? 'with-sidebar' : 'no-sidebar'} ${mobile ? 'is-mobile' : ''} ${drawer ? 'drawer-mode' : ''}`}>
          {drawer && sidebarOpen && <div className="drawer-backdrop" onClick={e => { if (e.timeStamp - drawerOpenedAt.current > 400) setSidebarOpen(false) }} />}
          <div className={`sidebar-wrap ${sidebarOpen ? 'open' : ''}`}>
            <Sidebar onCreate={create} onNavigate={closeDrawer} counts={counts} />
          </div>
          <div className="main">
            <TopBar onMenu={toggleSidebar} onPalette={() => setPalette(true)} mobile={mobile} compact={narrow} sidebarOpen={sidebarOpen} crumb={crumb} />
            <main className="content"><div className="content-inner"><Outlet /></div></main>
          </div>
          {mobile && <button type="button" className="fab" aria-label="Add" title="Add" aria-haspopup="menu" aria-expanded={!!fabMenu} onClick={e => setFabMenu(fabMenu ? null : rectOf(e.currentTarget))}><Icon name="plus" size={24} strokeWidth={2.2} /></button>}
          {fabMenu && <Menu anchor={fabMenu} align="right" items={createItems(create)} onClose={() => setFabMenu(null)} width={270} />}
          {palette && <CommandPalette onClose={() => setPalette(false)} commands={commands} />}
          {shortcuts && <ShortcutsSheet onClose={() => setShortcuts(false)} />}
        </div>
      </CountsCtx.Provider>
    </CrumbCtx.Provider>
  )
}

const SECTION: Record<string, [string, string]> = {
  '': ['Inbox', '/'], alerts: ['Inbox', '/'], sources: ['Sources', '/sources'], 'send-to': ['Send to', '/send-to'],
  rules: ['Rules', '/rules'], quiet: ['Quiet hours', '/quiet'], activity: ['Activity', '/activity'],
}

function TopBar({ onMenu, onPalette, mobile, compact, sidebarOpen, crumb }: { onMenu: () => void; onPalette: () => void; mobile: boolean; compact: boolean; sidebarOpen: boolean; crumb: string }) {
  const { user, themeMode, setThemeMode, status, setUser } = useApp()
  const navigate = useNavigate()
  const loc = useLocation()
  const [section, sectionTo] = SECTION[loc.pathname.split('/')[1] || ''] || ['', '/']
  const signOut = async () => { await api.post(`${V1}/auth/logout`).catch(() => {}); setUser(null); navigate('/login') }
  const avatarMenu = (): MenuItem[] => [
    { heading: user?.name || user?.email || '' },
    { label: 'Profile', icon: 'user', onSelect: () => navigate('/settings/profile') },
    { label: 'Settings', icon: 'settings', onSelect: () => navigate('/settings'), hint: ',' },
    { label: 'Keyboard shortcuts', icon: 'keyboard', onSelect: () => window.dispatchEvent(new CustomEvent('pal:shortcuts')), hint: '?' },
    'sep',
    { heading: 'Appearance' },
    { label: 'System', checked: themeMode === 'system', onSelect: () => setThemeMode('system') },
    { label: 'Light', checked: themeMode === 'light', onSelect: () => setThemeMode('light') },
    { label: 'Dark', checked: themeMode === 'dark', onSelect: () => setThemeMode('dark') },
    ...(status?.auth_mode === 'local' ? ['sep' as const, { label: 'Sign out', icon: 'log-out', onSelect: signOut }] : []),
  ]
  return (
    <header className="topbar">
      <button type="button" className="btn ghost icon-only" onClick={onMenu} aria-label={sidebarOpen ? 'Hide the sidebar' : 'Show the sidebar'} title="Menu ([)"><Icon name={mobile || compact ? 'menu' : 'sidebar'} size={18} /></button>
      <h1 className="range-title">
        {crumb ? <><button type="button" className="crumb" onClick={() => navigate(sectionTo)}>{section}</button><Icon name="chevron-right" size={16} className="crumb-sep" /><span className="ellipsis">{crumb}</span></> : section}
      </h1>
      <span className="spacer" />
      <button type="button" className="btn ghost icon-only" onClick={onPalette} aria-label="Search" title="Search (⌘K or /)"><Icon name="search" size={18} /></button>
      <AskButton />
      <MenuButton label="Account" className="avatar-btn" align="right" width={220} items={avatarMenu}>
        <Avatar name={user?.name} email={user?.email} size={30} />
      </MenuButton>
    </header>
  )
}
