// Inside Pocket Hub: what needs you (unread alerts, a source or channel that is failing) goes to the
// hub's Activity panel, its sidebar badge and the Dock through Core's pocketApp (/pocket-ask.js).
// Outside the hub, or without Core, it does nothing.
import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import type { Alert, Dashboard } from '../lib/types'

interface HubItem { id: string; title: string; detail: string; state: 'needs-you' | 'failed' | 'unread'; url: string; at?: string }
interface PocketApp {
  inHub: boolean
  report(r: { count?: number; status?: string | null; items?: HubItem[]; open?: (id: string) => void }): void
}
const pocketApp = () => (window as unknown as { pocketApp?: PocketApp }).pocketApp

const SHOWN = 15
// the hub keeps recent apps loaded but hidden behind the one on screen, and Activity is for exactly those
const POLL_MS = 60_000
const POLL_HIDDEN_MS = 120_000

const firstLine = (s: string) => s.split('\n').find(l => l.trim())?.trim() || ''
const clip = (s: string, n = 120) => (s.length > n ? s.slice(0, n - 1).trimEnd() + '…' : s)
const iso = (s: string) => { const d = new Date(s); return isNaN(d.getTime()) ? undefined : d.toISOString() }
const plural = (n: number, one: string, many: string) => (n === 1 ? one : `${n} ${many}`)

/** Where an item opens: the special ones are pages, the rest alerts. */
const PAGE: Record<string, string> = { 'channels-failing': '/send-to', 'sources-failing': '/sources' }
const pathOf = (id: string) => PAGE[id] || `/alerts/${encodeURIComponent(id)}`

function itemsOf(dash: Dashboard, alerts: Alert[]): HubItem[] {
  const out: HubItem[] = []
  if (dash.channels_failing > 0) out.push({
    id: 'channels-failing', state: 'failed', url: location.origin + PAGE['channels-failing'],
    title: plural(dash.channels_failing, 'A place to send to is failing', 'places to send to are failing'),
    detail: 'Send to · alerts may not reach you',
  })
  if (dash.sources_failing > 0) out.push({
    id: 'sources-failing', state: 'failed', url: location.origin + PAGE['sources-failing'],
    title: plural(dash.sources_failing, 'A source is not working', 'sources are not working'),
    detail: 'Sources · see what is wrong',
  })
  for (const a of alerts.slice(0, SHOWN)) {
    const title = a.title || firstLine(a.body) || a.source_name || 'Alert'
    const msg = a.title ? firstLine(a.body) : a.body.split('\n').filter(l => l.trim()).slice(1).join(' ').trim()
    const detail = [a.source_name, msg && clip(msg), a.count > 1 ? `×${a.count}` : ''].filter(Boolean).join(' · ')
    out.push({
      id: String(a.id), title: clip(title, 200), detail, state: a.priority >= 4 ? 'needs-you' : 'unread',
      url: `${location.origin}/alerts/${a.id}`, at: iso(a.last_seen_at || a.received_at),
    })
  }
  return out
}

export function HubReport() {
  const { user } = useApp()
  const navigate = useNavigate()
  const nav = useRef(navigate)
  nav.current = navigate

  // pocketApp comes with the deferred /pocket-ask.js, maybe after this first runs
  const [inHub, setInHub] = useState(() => !!pocketApp()?.inHub)
  useEffect(() => {
    const ready = () => setInHub(!!pocketApp()?.inHub)
    window.addEventListener('pocket-app-ready', ready)
    ready()
    return () => window.removeEventListener('pocket-app-ready', ready)
  }, [])

  const last = useRef('')
  const send = useCallback((count: number, items: HubItem[]) => {
    const key = JSON.stringify([count, items])
    if (key === last.current) return
    last.current = key
    pocketApp()?.report({ count, status: null, items, open: id => nav.current(pathOf(String(id))) })
  }, [])

  const active = inHub && !!user
  const live = useRef(active)
  live.current = active
  const busy = useRef(false)
  const load = useCallback(() => {
    if (busy.current) return
    busy.current = true
    Promise.all([
      api.get<Dashboard>(`${V1}/dashboard`),
      api.get<{ alerts: Alert[] }>(`${V1}/alerts?view=unread&limit=${SHOWN}`),
    ]).then(([dash, list]) => live.current && send(dash.counts.unread, itemsOf(dash, list.alerts || [])))
      .catch(() => {})
      .finally(() => { busy.current = false })
  }, [send])

  useEffect(() => {
    if (!active) {
      if (last.current) send(0, [])   // signed out: nothing waits for anyone
      return
    }
    load()
    // follow the shell's live updates and changes made here (at most every 2 s)
    let soon: number | undefined
    const later = () => { if (soon == null) soon = window.setTimeout(() => { soon = undefined; load() }, 2000) }
    // the live connection lives in the inbox's shell; a light poll covers settings, dropped connections
    // and a page the hub keeps hidden (every minute on screen, every two hidden)
    let poll: number | undefined
    const schedule = () => {
      clearTimeout(poll)
      poll = window.setTimeout(() => { load(); schedule() }, document.visibilityState === 'visible' ? POLL_MS : POLL_HIDDEN_MS)
    }
    schedule()
    const wake = () => { if (document.visibilityState === 'visible') load(); schedule() }
    window.addEventListener('pal:live', later)
    window.addEventListener('pal:changed', later)
    document.addEventListener('visibilitychange', wake)
    return () => {
      clearTimeout(soon); clearTimeout(poll)
      window.removeEventListener('pal:live', later)
      window.removeEventListener('pal:changed', later)
      document.removeEventListener('visibilitychange', wake)
    }
  }, [active, load, send])

  return null
}
