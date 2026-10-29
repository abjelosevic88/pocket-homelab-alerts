// The inbox: every alert from every source, newest first; open one to see what happened to it.
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago, fmtDateTime, fmtYMD, plural } from '../lib/format'

/** A row's time: "12 min ago" for the last week, then just the day (the year only when it is another one). */
function when(iso: string): string {
  const t = new Date(iso)
  if (Date.now() - t.getTime() < 7 * 86400e3) return ago(iso)
  return fmtYMD(t.getFullYear(), t.getMonth() + 1, t.getDate(), t.getFullYear() !== new Date().getFullYear())
}
import type { Alert, AlertDetail, InboxView, Source } from '../lib/types'
import { STATE_LABEL, priorityLabel } from '../lib/types'
import { Icon } from '../components/icons'
import { Select } from '../components/Select'
import { Confirm, Empty, Loading, type MenuItem, MenuButton, Segmented, Spinner, errMsg, modalOpen, useDebounce, useMediaQuery, useToast } from '../components/ui'
import { announceChange, openCreate, useCounts, useCrumb, useLive, useOnChange } from '../components/Layout'

const VIEWS: { value: InboxView; label: string }[] = [
  { value: 'open', label: 'Open' }, { value: 'unread', label: 'Unread' }, { value: 'important', label: 'Important' },
  { value: 'done', label: 'Done' }, { value: 'all', label: 'All' },
]

const EMPTY_TEXT: Record<InboxView, [string, string]> = {
  open: ['Nothing open', 'Every alert is done. New ones show up here as they come in.'],
  unread: ['All read', 'There is nothing you have not seen yet.'],
  important: ['Nothing important', 'No high or urgent alert is waiting for you.'],
  done: ['Nothing done yet', 'Alerts you mark done move here.'],
  all: ['No alerts yet', 'Alerts show up here as soon as a source sends one.'],
}

const firstLine = (s: string) => s.split('\n').find(l => l.trim()) || ''
const titleOf = (a: Alert) => a.title || firstLine(a.body) || a.source_name

type ListAnswer = { alerts: Alert[]; next: string }

export default function Inbox() {
  const { id: idParam } = useParams()
  const openId = idParam ? Number(idParam) : 0
  const navigate = useNavigate()
  const [sp, setSp] = useSearchParams()
  const view = (VIEWS.some(v => v.value === sp.get('view')) ? sp.get('view') : 'open') as InboxView
  const source = sp.get('source') || ''
  const [q, setQ] = useState(sp.get('q') || '')
  const dq = useDebounce(q.trim(), 300)
  const split = useMediaQuery('(min-width: 1100px)')
  const counts = useCounts()
  const { toast } = useToast()

  const [alerts, setAlerts] = useState<Alert[] | null>(null)
  const [next, setNext] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [sources, setSources] = useState<Source[]>([])
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [hi, setHi] = useState(-1)
  const [fresh, setFresh] = useState(0)
  const [busy, setBusy] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const setParam = (k: string, v: string) => {
    const n = new URLSearchParams(sp)
    if (v) n.set(k, v); else n.delete(k)
    setSp(n, { replace: true })
  }
  useEffect(() => { if ((sp.get('q') || '') !== dq) setParam('q', dq) }, [dq]) // eslint-disable-line react-hooks/exhaustive-deps

  const query = useMemo(() => {
    const p = new URLSearchParams({ view })
    if (source) p.set('source', source)
    if (dq) p.set('q', dq)
    return p.toString()
  }, [view, source, dq])

  // the list: a fresh load, or a quiet refetch that keeps what was paged in
  const shown = useRef(0)
  shown.current = alerts?.length || 0
  const load = useCallback((keep = false) => {
    const limit = keep ? Math.min(500, Math.max(100, shown.current)) : 100
    setLoading(true)
    return api.get<ListAnswer>(`${V1}/alerts?${query}&limit=${limit}`)
      .then(r => { setAlerts(r.alerts); setNext(r.next); setError(''); if (!keep) setSelected(new Set()) })
      .catch(e => setError(errMsg(e)))
      .finally(() => setLoading(false))
  }, [query])
  useEffect(() => { setAlerts(null); setFresh(0); setHi(-1); load() }, [load])

  const loadSources = useCallback(() => { api.get<Source[]>(`${V1}/sources`).then(setSources).catch(() => {}) }, [])
  useEffect(() => { loadSources() }, [loadSources])

  const older = () => {
    if (!next) return
    setLoading(true)
    api.get<ListAnswer>(`${V1}/alerts?${query}&limit=100&before=${encodeURIComponent(next)}`)
      .then(r => { setAlerts(a => [...(a || []), ...r.alerts]); setNext(r.next) })
      .catch(e => toast(errMsg(e), 'err'))
      .finally(() => setLoading(false))
  }

  // live: refetch at most every 1.5 s; new alerts wait behind a pill while you are reading further down
  const timer = useRef<number | null>(null)
  const refetch = useCallback(() => {
    if (timer.current != null) return
    timer.current = window.setTimeout(() => { timer.current = null; load(true) }, 1500)
  }, [load])
  useEffect(() => () => { if (timer.current) clearTimeout(timer.current) }, [])
  const atTop = () => (document.querySelector('.content')?.scrollTop || 0) < 40
  const selectedRef = useRef(selected)
  selectedRef.current = selected
  useLive(e => {
    if (e.type === 'source') { loadSources(); return }
    if (e.type === 'alert' && e.new) {
      if (atTop() && selectedRef.current.size === 0) refetch()
      else setFresh(n => n + 1)
      return
    }
    refetch()
  })
  useOnChange(() => refetch())
  const showFresh = () => { setFresh(0); document.querySelector('.content')?.scrollTo({ top: 0, behavior: 'smooth' }); load(true) }

  const patch = useCallback((id: number, p: Partial<Alert>) => setAlerts(list => list?.map(a => (a.id === id ? { ...a, ...p } : a)) || list), [])

  const mark = async (ids: number[] | undefined, what: 'read' | 'done', on = true) => {
    setBusy(true)
    try {
      await api.post(`${V1}/alerts/mark`, { ids, what, on })
      const now = new Date().toISOString()
      if (ids) ids.forEach(id => patch(id, what === 'read' ? { read_at: on ? now : null } : { done_at: on ? now : null, ...(on ? { read_at: now } : {}) }))
      if (ids && what === 'done') setSelected(new Set())
      announceChange()
      load(true)
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const removeSelected = async () => {
    setConfirmDelete(false)
    setBusy(true)
    try {
      const ids = [...selected]
      await api.post(`${V1}/alerts/delete`, { ids })
      setSelected(new Set())
      if (ids.includes(openId)) navigate({ pathname: '/', search: sp.toString() })
      toast(`${plural(ids.length, 'alert')} deleted`)
      announceChange()
      load(true)
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const toggleSel = (id: number) => setSelected(s => { const n = new Set(s); if (n.has(id)) n.delete(id); else n.add(id); return n })

  const open = (id: number, replace = false) => navigate({ pathname: `/alerts/${id}`, search: sp.toString() }, { replace })
  const back = () => navigate({ pathname: '/', search: sp.toString() })

  // keyboard: j/k move, Enter opens, e done, u read/unread, x select
  const listRef = useRef(alerts)
  listRef.current = alerts
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return
      const t = e.target as HTMLElement
      if (t.closest('input, textarea, select, [contenteditable="true"]') || modalOpen() || document.querySelector('.menu, .popover, .sel-pop')) return
      const list = listRef.current || []
      if (!list.length) return
      const openIdx = list.findIndex(a => a.id === openId)
      const cur = hi >= 0 ? hi : openIdx
      const target = cur >= 0 ? list[cur] : undefined
      const move = (d: number) => {
        e.preventDefault()
        const n = Math.max(0, Math.min(list.length - 1, cur < 0 ? (d > 0 ? 0 : 0) : cur + d))
        setHi(n)
        if (openId) open(list[n].id, true)
        document.querySelector(`[data-alert="${list[n].id}"]`)?.scrollIntoView({ block: 'nearest' })
      }
      switch (e.key) {
        case 'j': move(1); break
        case 'k': move(-1); break
        case 'Enter': if (target && !t.closest('button, a')) { e.preventDefault(); open(target.id) } break
        case 'e': if (target) { e.preventDefault(); mark([target.id], 'done', !target.done_at) } break
        case 'u': if (target) { e.preventDefault(); mark([target.id], 'read', !target.read_at) } break
        case 'x': if (target) { e.preventDefault(); toggleSel(target.id) } break
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  })

  const sourceOptions = useMemo(() => [{ value: '', label: 'Every source' }, ...sources.map(s => ({ value: String(s.id), label: s.name, hint: s.muted ? 'muted' : undefined }))], [sources])
  const unread = counts?.counts.unread || 0
  const noSources = counts != null && counts.sources === 0 && sources.length === 0

  const onGone = () => {
    load(true)
    if (!split) { back(); return }
    const list = alerts || []
    const i = list.findIndex(a => a.id === openId)
    const n = list[i + 1] || list[i - 1]
    if (n && n.id !== openId) open(n.id, true); else back()
  }
  const detail = openId ? <AlertPanel key={openId} id={openId} split={split} onBack={back} onPatch={patch} onGone={onGone} /> : null

  if (openId && !split) return detail

  const list = (
    <div className="ib-list-col">
      <div className="ib-toolbar">
        <div className="ib-bar">
          <Segmented size="sm" options={VIEWS.map(v => ({ value: v.value, label: v.label }))} value={view} onChange={v => setParam('view', v === 'open' ? '' : v)} label="Which alerts" />
          <span className="spacer" />
          <span className="ib-load">{loading && <Spinner />}</span>
          {selected.size > 0 ? (
            <div className="ib-actions">
              <span className="small muted tnum">{selected.size} selected</span>
              <button type="button" className="btn sm" disabled={busy} onClick={() => mark([...selected], 'done')}><Icon name="check" size={14} />Done</button>
              <button type="button" className="btn sm" disabled={busy} onClick={() => mark([...selected], 'read')}>Read</button>
              <button type="button" className="btn sm icon-only danger-ghost" disabled={busy} title="Delete" aria-label="Delete" onClick={() => setConfirmDelete(true)}><Icon name="trash" size={14} /></button>
              <button type="button" className="btn sm icon-only ghost" title="Clear the selection" aria-label="Clear the selection" onClick={() => setSelected(new Set())}><Icon name="x" size={14} /></button>
            </div>
          ) : (
            <div className="ib-actions">
              <button type="button" className="btn sm" disabled={busy || unread === 0} onClick={() => mark(undefined, 'read')} title="Mark every open alert read"><Icon name="check" size={14} />Mark all read</button>
            </div>
          )}
        </div>
        <div className="ib-bar">
          <div className="ib-source"><Select value={source} onChange={v => setParam('source', v)} options={sourceOptions} ariaLabel="Source" placeholder="Every source" /></div>
          <div className="ib-search">
            <Icon name="search" size={15} />
            <input type="search" value={q} onChange={e => setQ(e.target.value)} placeholder="Search alerts" aria-label="Search alerts" />
          </div>
        </div>
      </div>

      {counts?.quiet && (
        <Link to="/quiet" className="callout ib-note quiet">
          <Icon name="moon" size={15} />
          <span>Quiet hours until <b>{counts.quiet_until}</b>{counts.counts.held > 0 && <> · {plural(counts.counts.held, 'alert')} waiting for the summary</>}</span>
        </Link>
      )}
      {counts && counts.channels === 0 && !noSources && (
        <div className="callout warn ib-note">
          <Icon name="smartphone" size={15} />
          <div className="grow">Nothing goes to your phone yet. Add Pushover, ntfy or another place under Send to, and alerts reach you there.</div>
          <button type="button" className="btn sm" onClick={() => openCreate('channel')}>Add one</button>
        </div>
      )}

      <div className="ib-new-wrap">{fresh > 0 && <button type="button" className="ib-new" onClick={showFresh}><Icon name="arrow-up" size={13} />{plural(fresh, 'new alert')} — show</button>}</div>

      {error && <div className="callout danger">{error}</div>}
      {alerts == null ? <Loading /> : noSources && view === 'open' && !dq && !source ? (
        <Empty title="No alerts yet" icon="inbox">
          <p>Alerts come in from your scripts, servers and apps. Add a source and Pocket Alerts gives it an address: anything that can send to Apprise, ntfy or a webhook can send there.</p>
          <button type="button" className="btn primary mt" onClick={() => openCreate('source')}><Icon name="plus" size={15} />Add a source</button>
        </Empty>
      ) : alerts.length === 0 ? (
        <Empty title={dq ? 'Nothing found' : EMPTY_TEXT[view][0]} icon={dq ? 'search' : 'check-circle'}>{dq ? `No alert matches “${dq}”.` : EMPTY_TEXT[view][1]}</Empty>
      ) : (
        <ul className="ib-list" role="list">
          {alerts.map((a, i) => (
            <AlertRow key={a.id} a={a} active={a.id === openId} hi={i === hi} checked={selected.has(a.id)} onCheck={() => toggleSel(a.id)} onOpen={() => { setHi(i); open(a.id) }} />
          ))}
        </ul>
      )}
      {alerts && next && <div className="ib-more"><button type="button" className="btn sm" disabled={loading} onClick={older}>Show older</button></div>}
      {confirmDelete && <Confirm title={`Delete ${plural(selected.size, 'alert')}?`} message="They are gone for good. To keep them out of the way, mark them done instead." confirmLabel="Delete" onConfirm={removeSelected} onCancel={() => setConfirmDelete(false)} />}
    </div>
  )

  if (!split) return <div className="ib">{list}</div>
  return (
    <div className="ib split">
      {list}
      <div className="ib-detail-col">
        {detail || <div className="ib-pick"><Icon name="inbox" size={22} /><span>Pick an alert to see what it says and what happened to it.</span></div>}
      </div>
    </div>
  )
}

function AlertRow({ a, active, hi, checked, onCheck, onOpen }: { a: Alert; active: boolean; hi: boolean; checked: boolean; onCheck: () => void; onOpen: () => void }) {
  const preview = a.title ? firstLine(a.body) : a.body.split('\n').filter(l => l.trim()).slice(1).join(' ')
  const unread = !a.read_at && !a.done_at
  return (
    <li data-alert={a.id} className={`ib-row p${a.priority} ${unread ? 'unread' : ''} ${active ? 'active' : ''} ${hi ? 'hi' : ''} ${checked ? 'checked' : ''} ${a.done_at ? 'done' : ''}`}>
      <span className="ib-prio" aria-hidden="true" />
      <label className="ib-check" title="Select">
        <input type="checkbox" checked={checked} onChange={onCheck} aria-label="Select" />
      </label>
      <button type="button" className="ib-row-main" onClick={onOpen}>
        <span className="ib-emoji" aria-hidden="true">{a.emoji ? [...a.emoji].slice(0, 2).join('') : <Icon name="bell" size={15} />}</span>
        <span className="ib-text">
          <span className="ib-line">
            <span className="ib-title ellipsis">{titleOf(a)}</span>
            {a.count > 1 && <span className="badge ib-count tnum" title={`Came ${a.count} times`}>×{a.count}</span>}
            <span className="ib-time tnum" title={fmtDateTime(a.last_seen_at)}>{when(a.last_seen_at)}</span>
          </span>
          <span className="ib-line ib-sub">
            <span className="ib-src">{a.source_name}</span>
            <span className="ib-preview ellipsis">{preview}</span>
            {a.state !== 'sent' && <span className={`ib-state s-${a.state}`}>{STATE_LABEL[a.state]}</span>}
          </span>
        </span>
        <span className="ib-dot-slot">{unread && <span className="ib-dot" aria-label="Unread" />}</span>
      </button>
    </li>
  )
}

// ---- one alert ----

const URL_RE = /(https?:\/\/[^\s<>"']+)/g
function Linked({ text }: { text: string }) {
  const parts = text.split(URL_RE)
  return <>{parts.map((p, i) => (i % 2 === 1 ? <a key={i} href={p} target="_blank" rel="noreferrer">{p}</a> : <React.Fragment key={i}>{p}</React.Fragment>))}</>
}

type Explained = { text?: string; error?: string; setup?: boolean }

function AlertPanel({ id, split, onBack, onPatch, onGone }: { id: number; split: boolean; onBack: () => void; onPatch: (id: number, p: Partial<Alert>) => void; onGone: () => void }) {
  const { settings } = useApp()
  const { toast } = useToast()
  const [d, setD] = useState<AlertDetail | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [explain, setExplain] = useState<Explained | null>(null)
  const [confirm, setConfirm] = useState(false)
  const marked = useRef(false)

  const load = useCallback(() => api.get<AlertDetail>(`${V1}/alerts/${id}`).then(r => { setD(r); setError('') }).catch(e => setError(errMsg(e))), [id])
  useEffect(() => { load() }, [load])
  const lastLoad = useRef<number | null>(null)
  useLive(e => {
    if (!((e.type === 'alert' && e.id === id) || e.type === 'refresh') || lastLoad.current != null) return
    lastLoad.current = window.setTimeout(() => { lastLoad.current = null; load() }, 800)
  })
  useEffect(() => () => { if (lastLoad.current) clearTimeout(lastLoad.current) }, [])

  // opening an unread alert reads it
  useEffect(() => {
    if (!d || marked.current || d.alert.read_at) return
    marked.current = true
    const now = new Date().toISOString()
    api.post(`${V1}/alerts/mark`, { ids: [id], what: 'read' }).then(() => { onPatch(id, { read_at: now }); announceChange() }).catch(() => {})
  }, [d, id, onPatch])

  const a = d?.alert
  useCrumb(a ? titleOf(a) : undefined)

  const run = async (key: string, fn: () => Promise<void>) => {
    setBusy(key)
    try { await fn() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy('') }
  }
  const setMark = (what: 'read' | 'done', on: boolean) => run(what, async () => {
    await api.post(`${V1}/alerts/mark`, { ids: [id], what, on })
    const now = new Date().toISOString()
    const p: Partial<Alert> = what === 'read' ? { read_at: on ? now : null } : { done_at: on ? now : null, ...(on ? { read_at: now } : {}) }
    marked.current = true
    onPatch(id, p)
    setD(x => (x ? { ...x, alert: { ...x.alert, ...p } } : x))
    announceChange()
    if (what === 'done' && on) {
      toast('Done', 'ok', { label: 'Undo', run: () => { api.post(`${V1}/alerts/mark`, { ids: [id], what: 'done', on: false }).then(() => { onPatch(id, { done_at: null }); announceChange() }).catch(() => {}) } })
      onGone()
    }
  })
  const quietLikeThis = () => run('quiet', async () => {
    await api.post(`${V1}/alerts/${id}/quiet`)
    toast(<>From now on alerts like this stay quiet. <Link to="/rules">See the rule</Link></>)
  })
  const snooze = (body: Record<string, unknown>, label: string) => run('snooze', async () => {
    if (!a?.source_id) return
    await api.post(`${V1}/sources/${a.source_id}/snooze`, body)
    toast(label)
    load()
  })
  const task = () => run('task', async () => {
    const r = await api.post<{ ok: boolean; url?: string; error?: string; not_connected?: boolean }>(`${V1}/alerts/${id}/task`, {})
    if (!r.ok) {
      toast(r.not_connected ? <>Pocket Tasks is not connected. <Link to="/settings/apps">Settings → Connected apps</Link></> : r.error || 'Pocket Tasks did not take it', 'err')
      return
    }
    toast('Added to Pocket Tasks')
    load()
  })
  const ask = () => run('explain', async () => {
    setExplain(null)
    const r = await api.post<{ ok: boolean; text?: string; error?: string; not_configured?: boolean }>(`${V1}/alerts/${id}/explain`)
    setExplain(r.ok ? { text: r.text } : { error: r.error || 'AI did not answer', setup: r.not_configured })
  })
  const remove = () => run('delete', async () => {
    setConfirm(false)
    await api.del(`${V1}/alerts/${id}`)
    toast('Alert deleted')
    announceChange()
    onGone()
  })

  if (error) return <div className="ib-panel">{!split && <BackBar onBack={onBack} />}<div className="callout danger">{error === 'not found' ? 'This alert is gone.' : error}</div></div>
  if (!d || !a) return <div className="ib-panel">{!split && <BackBar onBack={onBack} />}<Loading /></div>

  const tasksOff = settings?.tasks_mode === 'off'
  const snoozed = d.source?.snoozed_until || null
  const more = (): MenuItem[] => {
    const items: MenuItem[] = [{ label: 'Keep alerts like this quiet', icon: 'bell-off', onSelect: quietLikeThis, disabled: !!busy }]
    if (a.source_id) {
      items.push({ heading: snoozed ? `Snoozed until ${fmtDateTime(snoozed)}` : `Snooze ${a.source_name}` },
        { label: 'For an hour', icon: 'clock', onSelect: () => snooze({ minutes: 60 }, `${a.source_name} is snoozed for an hour`) },
        { label: 'For 4 hours', icon: 'clock', onSelect: () => snooze({ minutes: 240 }, `${a.source_name} is snoozed for 4 hours`) },
        { label: 'Until tomorrow morning', icon: 'moon', onSelect: () => snooze({ until: 'tomorrow' }, `${a.source_name} is snoozed until tomorrow morning`) })
      if (snoozed) items.push({ label: 'Wake it up now', icon: 'bell', onSelect: () => snooze({ minutes: 0 }, `${a.source_name} is awake again`) })
    }
    items.push('sep', { label: 'Delete', icon: 'trash', danger: true, onSelect: () => setConfirm(true) })
    return items
  }
  return (
    <article className={`ib-panel p${a.priority}`}>
      {!split && <BackBar onBack={onBack} />}
      <header className="ib-head">
        <span className="ib-head-emoji" aria-hidden="true">{a.emoji || <Icon name="bell" size={20} />}</span>
        <div className="grow">
          <h2 className="ib-head-title">{titleOf(a)}</h2>
          <div className="ib-meta small muted">
            {a.source_id ? <Link to={`/sources/${a.source_id}`}>{a.source_name}</Link> : <span>{a.source_name}</span>}
            <span aria-hidden="true">·</span><span className={`ib-imp p${a.priority}`}>{priorityLabel(a.priority)}</span>
            <span aria-hidden="true">·</span><span title={fmtDateTime(a.received_at)}>{ago(a.received_at)}</span>
          </div>
        </div>
      </header>

      <div className="ib-actbar">
        {a.done_at
          ? <button type="button" className="btn sm" disabled={!!busy} onClick={() => setMark('done', false)}><Icon name="undo" size={14} />Not done</button>
          : <button type="button" className="btn sm primary" disabled={!!busy} onClick={() => setMark('done', true)}><Icon name="check" size={14} />Done</button>}
        <button type="button" className="btn sm" disabled={!!busy || !a.read_at} onClick={() => setMark('read', false)} title="Show it as new again">Unread</button>
        {a.click_url && <a className="btn sm" href={a.click_url} target="_blank" rel="noreferrer"><Icon name="external" size={14} />Open</a>}
        <span className="spacer" />
        <MenuButton label="More" align="right" width={260} className="btn sm icon-only ghost" items={more}><Icon name="more" size={16} /></MenuButton>
      </div>

      {a.labels.length > 0 && <div className="chips ib-labels">{a.labels.map(l => <span key={l} className="ib-label">{l}</span>)}</div>}
      {a.body && <div className="ib-body"><Linked text={a.body} /></div>}
      {a.count > 1 && <p className="small muted ib-repeat"><Icon name="repeat" size={13} />Came {a.count} times, the last one {ago(a.last_seen_at)} ({fmtDateTime(a.last_seen_at)}). Repeats do not ping you again.</p>}

      <section className="ib-sec">
        <h3>What happened</h3>
        <div className={`ib-what s-${a.state}`}>
          <Icon name={a.state === 'sent' ? 'check-circle' : a.state === 'held' ? 'moon' : a.state === 'failed' ? 'x-circle' : 'bell-off'} size={15} />
          <div>
            <b>{STATE_LABEL[a.state]}</b>{a.reason && <span className="muted"> — {a.reason}</span>}
            {d.rule && <div className="small"><Link to="/rules">Rule: {d.rule.name}</Link></div>}
            {snoozed && <div className="small muted">{a.source_name} is snoozed until {fmtDateTime(snoozed)}.</div>}
          </div>
        </div>
        {d.deliveries.length > 0 && (
          <ul className="ib-deliv">
            {d.deliveries.map(x => (
              <li key={x.id}>
                <Icon name={x.status === 'sent' ? 'check' : x.status === 'retrying' ? 'refresh' : 'x'} size={14} className={x.status === 'sent' ? 'ok' : x.status === 'failed' ? 'bad' : 'muted'} />
                <span className="grow">
                  <b>{x.channel || 'A removed channel'}</b>
                  <span className="muted"> — {x.status === 'sent' ? 'sent' : x.status === 'retrying' ? 'trying again soon' : 'failed'}{x.attempts > 1 ? ` after ${x.attempts} tries` : ''}</span>
                  {x.error && <span className="ib-err small">{x.error}</span>}
                </span>
                <span className="small muted tnum" title={fmtDateTime(x.at)}>{ago(x.at)}</span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="ib-sec">
        <h3>Follow up</h3>
        <div className="ib-follow">
          {a.task_url
            ? <a className="btn sm" href={a.task_url} target="_blank" rel="noreferrer"><Icon name="check-square" size={14} />Open the task</a>
            : <button type="button" className="btn sm" disabled={!!busy || tasksOff} title={tasksOff ? 'Pocket Tasks is switched off in Settings → Connected apps' : 'Add it to Pocket Tasks'} onClick={task}><Icon name="check-square" size={14} />{busy === 'task' ? 'Adding…' : 'Make a task'}</button>}
          <button type="button" className="btn sm" disabled={!!busy} onClick={ask}><Icon name="sparkles" size={14} />{busy === 'explain' ? 'Thinking…' : 'Explain it'}</button>
        </div>
        {tasksOff && !a.task_url && <p className="small muted">Pocket Tasks is switched off in <Link to="/settings/apps">Settings → Connected apps</Link>.</p>}
        {explain && (
          <div className={`ib-explain ${explain.error ? 'err' : ''}`}>
            {explain.text != null
              ? <><div className="ib-explain-h small muted"><Icon name="sparkles" size={13} />What AI makes of it</div><div className="ib-explain-text">{explain.text}</div></>
              : <>{explain.error}{explain.setup && <> <Link to="/settings/ai">Settings → AI</Link></>}</>}
          </div>
        )}
      </section>

      <p className="small faint ib-ids">Received {fmtDateTime(a.received_at)}{a.ext_id && <> · message id {a.ext_id}</>}</p>
      {confirm && <Confirm title="Delete this alert?" message="It is gone for good. To keep it out of the way, mark it done instead." confirmLabel="Delete" onConfirm={remove} onCancel={() => setConfirm(false)} />}
    </article>
  )
}

function BackBar({ onBack }: { onBack: () => void }) {
  return <div className="ib-back"><button type="button" className="btn ghost sm" onClick={onBack}><Icon name="chevron-left" size={15} />Inbox</button></div>
}
