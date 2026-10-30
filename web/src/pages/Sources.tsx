// Where alerts come from: your sources (addresses, followed ntfy topics) and the Pocket apps that can send here.
import { useCallback, useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago, plural } from '../lib/format'
import { PRIORITIES, STATE_LABEL, type Alert, type AppsInfo, type EventApp, type Source } from '../lib/types'
import { Icon } from '../components/icons'
import { Select } from '../components/Select'
import { SrcEditor } from '../components/SrcEditor'
import { MSection, clockOf, useNewParam } from '../components/SrcParts'
import { announceChange, useCrumb, useLive } from '../components/Layout'
import { Confirm, CopyField, CoreHint, Empty, Field, Loading, MenuButton, SettingRow, Switch, errMsg, useToast } from '../components/ui'

const ICON_FIX: Record<string, string> = { tv: 'video', captions: 'text' }

function srcIcon(x: { icon: string; kind: string }) {
  const i = ICON_FIX[x.icon] || x.icon
  if (i) return i
  return x.kind === 'ntfy' ? 'antenna' : x.kind === 'app' ? 'layers' : 'inbox'
}

/** What a source is, in words. */
function kindHint(x: Source): string {
  if (x.kind === 'ntfy') {
    let host = x.follow_url
    try { host = new URL(x.follow_url).host } catch { /* as typed */ }
    return `Follows ntfy: ${x.follow_topics.split(',').join(', ')} on ${host}`
  }
  if (x.kind === 'app') return 'Pocket app, through Pocket Core'
  return x.topic ? `Sends here (topic “${x.topic}”)` : 'Sends here'
}

/** The one pill that says whether something needs a look. */
function SourceState({ x }: { x: Source }) {
  if (!x.enabled) return <span className="src-pill">Off</span>
  if (x.status === 'error') return <span className="src-pill bad" title={x.status_note}><Icon name="alert" size={12} />Not connected</span>
  if (x.snoozed_until) return <span className="src-pill quiet"><Icon name="moon" size={12} />Snoozed until {clockOf(x.snoozed_until)}</span>
  if (x.muted) return <span className="src-pill quiet"><Icon name="bell-off" size={12} />Muted</span>
  return null
}

export default function Sources() {
  const navigate = useNavigate()
  const { toast } = useToast()
  const [list, setList] = useState<Source[] | null>(null)
  const [apps, setApps] = useState<AppsInfo | null>(null)
  const [adding, setAdding] = useState(false)
  const [err, setErr] = useState('')
  const load = useCallback(() => {
    api.get<Source[]>(`${V1}/sources`).then(l => { setList(l); setErr('') }).catch(e => setErr(errMsg(e)))
    api.get<AppsInfo>(`${V1}/apps`).then(setApps).catch(() => {})
  }, [])
  useEffect(() => { load() }, [load])
  useLive(e => { if (e.type === 'source' || e.type === 'refresh') load() })
  const clearNew = useNewParam(() => setAdding(true))
  const close = () => { setAdding(false); clearNew() }

  const toggle = async (x: Source, enabled: boolean) => {
    setList(l => l && l.map(s => (s.id === x.id ? { ...s, enabled } : s)))
    try { await api.put(`${V1}/sources/${x.id}`, { enabled }); announceChange() } catch (e) { toast(errMsg(e), 'err'); load() }
  }
  const toggleApp = async (a: EventApp, on: boolean) => {
    setApps(s => s && { ...s, apps: s.apps.map(p => (p.slug === a.slug ? { ...p, on } : p)) })
    try { await api.put(`${V1}/apps/${a.slug}`, { on }); load() } catch (e) { toast(errMsg(e), 'err'); load() }
  }

  const mine = (list || []).filter(x => x.kind !== 'app')
  return (
    <div className="src-page">
      <p className="src-intro">Every alert comes from a source. Give a script or an app an address of its own, follow topics on an ntfy server you already use, or let your other Pocket apps send here.</p>
      <MSection title="Your sources" icon="antenna" sub="Switch one off and nothing from it comes in." actions={<button type="button" className="btn sm primary" onClick={() => setAdding(true)}><Icon name="plus" size={14} />Add a source</button>} flush>
        {err ? <div className="set-section-body"><p className="src-err">{err}</p></div>
          : !list ? <div className="set-section-body"><Loading /></div>
            : mine.length === 0 ? <Empty icon="antenna" title="No sources yet">Add one and you get an address to send alerts to.</Empty>
              : (
                <ul className="src-list">
                  {mine.map(x => (
                    <li key={x.id}>
                      <div className={`src-row ${x.enabled ? '' : 'off'}`} role="link" tabIndex={0} onClick={() => navigate(`/sources/${x.id}`)} onKeyDown={e => { if (e.key === 'Enter') navigate(`/sources/${x.id}`) }}>
                        <span className={`src-ico k-${x.kind}`}><Icon name={srcIcon(x)} size={17} /></span>
                        <span className="src-main">
                          <span className="row"><span className="src-name">{x.name}</span><SourceState x={x} /></span>
                          <span className="src-sub">{x.status === 'error' && x.enabled ? <span className="src-note bad">{x.status_note || 'Not connected'}</span> : <>{kindHint(x)} · last alert {x.last_alert_at ? ago(x.last_alert_at) : 'never'}</>}</span>
                        </span>
                        <span className="src-side">
                          <span className="src-count" title="The last 7 days"><b>{x.week.alerts}</b>{x.week.repeats > 0 ? `+${x.week.repeats} repeats` : 'this week'}</span>
                          <Switch checked={x.enabled} onChange={v => toggle(x, v)} label={`${x.name} on`} />
                        </span>
                      </div>
                    </li>
                  ))}
                </ul>
              )}
      </MSection>

      <AppsSection apps={apps} onToggle={toggleApp} onOpen={id => navigate(`/sources/${id}`)} />
      {adding && <SrcEditor onClose={close} onSaved={s => { setAdding(false); announceChange(); navigate(`/sources/${s.id}`, { replace: true }) }} />}
    </div>
  )
}

function AppsSection({ apps, onToggle, onOpen }: { apps: AppsInfo | null; onToggle: (a: EventApp, on: boolean) => void; onOpen: (id: number) => void }) {
  return (
    <MSection title="From your Pocket apps" icon="layers" sub="Alerts your other Pocket apps raise, through Pocket Core. Each one also needs to be allowed in Pocket Core." flush>
      {!apps ? <div className="set-section-body"><Loading /></div> : (
        <>
          {!apps.core && <div className="set-section-body"><CoreHint /></div>}
          {apps.core && !apps.stream && <div className="set-section-body"><p className="muted small">Not connected to Pocket Core’s events right now{apps.stream_error ? `: ${apps.stream_error}` : ''}. It keeps trying.</p></div>}
          <ul className="src-list">
            {apps.apps.map(a => {
              const open = a.on && !!a.source_id
              return (
                <li key={a.slug}>
                  <div className={`src-row ${open ? '' : 'static'} ${a.on && apps.core ? '' : 'off'}`} onClick={() => { if (open && a.source_id) onOpen(a.source_id) }}>
                    <span className="src-ico k-app"><Icon name={ICON_FIX[a.icon] || a.icon} size={17} /></span>
                    <span className="src-main">
                      <span className="src-name">{a.name}</span>
                      <span className="src-sub wrap">{a.about}</span>
                      {apps.core && a.on && !a.link_on && <span className="src-note warn">{a.link_detail || 'Not allowed yet'}. Allow it in Pocket Core → Links (Alerts → {a.name.replace('Pocket ', '')}, events:read).</span>}
                    </span>
                    <span className="src-side">
                      <Switch checked={a.on && apps.core} onChange={v => onToggle(a, v)} disabled={!apps.core} label={`Alerts from ${a.name}`} />
                    </span>
                  </div>
                </li>
              )
            })}
          </ul>
        </>
      )}
    </MSection>
  )
}

// ---- one source ----

const SNOOZE: { label: string; body: { minutes?: number; until?: string } }[] = [
  { label: 'For an hour', body: { minutes: 60 } },
  { label: 'For 4 hours', body: { minutes: 240 } },
  { label: 'Until tomorrow morning', body: { until: 'tomorrow' } },
  { label: 'For a week', body: { minutes: 10080 } },
]

export function SourcePage() {
  const { id } = useParams()
  const navigate = useNavigate()
  const { toast } = useToast()
  const [x, setX] = useState<Source | null>(null)
  const [err, setErr] = useState('')
  const [recent, setRecent] = useState<Alert[] | null>(null)
  const [app, setApp] = useState<EventApp | null>(null)
  const [ask, setAsk] = useState<'' | 'key' | 'delete'>('')
  const [busy, setBusy] = useState(false)
  const { user } = useApp()
  const isAdmin = user?.role === 'admin'
  useCrumb(x?.name)

  const load = useCallback(() => {
    api.get<Source>(`${V1}/sources/${id}`).then(s => { setX(s); setErr('') }).catch(e => setErr(errMsg(e)))
    api.get<{ alerts: Alert[] }>(`${V1}/alerts?view=all&source=${id}&limit=20`).then(r => setRecent(r.alerts)).catch(() => {})
  }, [id])
  useEffect(() => { load() }, [load])
  useEffect(() => {
    if (x?.kind !== 'app') return
    api.get<AppsInfo>(`${V1}/apps`).then(r => setApp(r.apps.find(a => a.slug === x.app_slug) || null)).catch(() => {})
  }, [x?.kind, x?.app_slug])
  useLive(e => { if ((e.type === 'source' && e.id === Number(id)) || e.type === 'refresh' || e.type === 'alert') load() })

  if (err) return <div className="src-page"><Empty icon="antenna" title="This source is not here">{err}</Empty><div className="center"><Link to="/sources" className="btn">All sources</Link></div></div>
  if (!x) return <div className="src-page"><Loading /></div>

  const save = async (patch: Record<string, unknown>, ok?: string) => {
    try { setX(await api.put<Source>(`${V1}/sources/${x.id}`, patch)); announceChange(); if (ok) toast(ok) } catch (e) { toast(errMsg(e), 'err'); load() }
  }
  const snooze = async (body: { minutes?: number; until?: string }) => {
    try {
      const s = await api.post<Source>(`${V1}/sources/${x.id}/snooze`, body)
      setX(s)
      toast(s.snoozed_until ? `Snoozed until ${clockOf(s.snoozed_until)}` : 'Awake again')
    } catch (e) { toast(errMsg(e), 'err') }
  }
  const test = async () => {
    setBusy(true)
    try {
      const r = await api.post<{ ok: boolean; error?: string; alert?: Alert }>(`${V1}/sources/${x.id}/test`)
      if (!r.ok || !r.alert) toast(r.error || 'The test was not kept', 'err')
      else {
        const a = r.alert
        toast(<>{STATE_LABEL[a.state]}{a.reason ? `: ${a.reason}` : ''}. <Link to={`/alerts/${a.id}`}>Open it</Link></>, a.state === 'sent' ? 'ok' : 'err')
      }
      load()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const newKey = async () => {
    setAsk('')
    try { setX(await api.post<Source>(`${V1}/sources/${x.id}/key`)); toast('New address made; the old one no longer works') } catch (e) { toast(errMsg(e), 'err') }
  }
  const remove = async () => {
    setAsk('')
    try { await api.del(`${V1}/sources/${x.id}`); announceChange(); toast(`${x.name} removed`); navigate('/sources') } catch (e) { toast(errMsg(e), 'err') }
  }

  const prioOpts = [{ value: '0', label: 'As sent', hint: 'what the sender says' }, ...PRIORITIES.map(p => ({ value: String(p.value), label: p.label, hint: p.hint }))]
  return (
    <div className="src-page">
      <div className="row wrap">
        <span className={`src-ico k-${x.kind}`}><Icon name={srcIcon(x)} size={17} /></span>
        <div className="grow">
          <div className="row"><span className="src-name">{x.name}</span><SourceState x={x} /></div>
          <div className="src-sub">{kindHint(x)} · {plural(x.week.alerts, 'alert')} this week{x.week.repeats ? `, ${plural(x.week.repeats, 'repeat')}` : ''}</div>
        </div>
        <div className="src-actions">
          <button type="button" className="btn sm" onClick={test} disabled={busy || !x.enabled}><Icon name="bell" size={14} />Send a test</button>
          {x.snoozed_until
            ? <button type="button" className="btn sm" onClick={() => snooze({ minutes: 0 })}><Icon name="bell" size={14} />Wake up now</button>
            : <MenuButton label="Snooze" className="btn sm" items={SNOOZE.map(s => ({ label: s.label, icon: 'moon', onSelect: () => snooze(s.body) }))}><Icon name="moon" size={14} />Snooze</MenuButton>}
        </div>
      </div>

      {x.kind === 'push' && x.addresses && (
        <MSection title="How to send here" icon="link" sub="Pick the one your app or script understands. The address itself is the password: keep it to yourself."
          actions={<button type="button" className="btn sm ghost" onClick={() => setAsk('key')}><Icon name="refresh" size={14} />New address</button>}>
          <div className="src-how">
            <div className="src-how-item">
              <span className="src-how-t">Apprise</span>
              <span className="src-how-d">For Pocket apps, scripts and anything that posts to an Apprise gateway (title, body, type).</span>
              <CopyField value={x.addresses.apprise} label="Apprise address" />
            </div>
            <div className="src-how-item">
              <span className="src-how-t">ntfy</span>
              <span className="src-how-d">For Sonarr, Radarr, Uptime Kuma and the ntfy app’s “publish”: this server, and this topic.</span>
              <div className="src-how-pair">
                <CopyField value={x.addresses.ntfy_server} label="ntfy server" />
                <CopyField value={x.addresses.ntfy_topic} label="ntfy topic" />
              </div>
            </div>
            <div className="src-how-item">
              <span className="src-how-t">Apprise URL</span>
              <span className="src-how-d">Inside Apprise, or apps that take an Apprise URL (Bazarr, SABnzbd).</span>
              <CopyField value={x.addresses.apprise_url} label="Apprise URL" />
            </div>
            <div className="src-how-item">
              <span className="src-how-t">Webhook</span>
              <span className="src-how-d">Post plain text, or JSON with title, message, priority (1–5), tags and click.</span>
              <CopyField value={x.addresses.hook} label="Webhook address" />
            </div>
            <div className="src-how-item">
              <span className="src-how-t">Try it from a terminal</span>
              <CopyField value={x.addresses.curl} label="curl example" />
            </div>
          </div>
        </MSection>
      )}

      {x.kind === 'ntfy' && <NtfyFollow x={x} onSave={p => save(p, 'Saved; reconnecting')} />}

      {x.kind === 'app' && (
        <MSection title="What comes in" icon="layers">
          <p className="muted small">{app?.about || 'Alerts from this Pocket app, through Pocket Core.'}</p>
          {app && (app.link_on
            ? <p className="small ok-text">Pocket Core lets them through.</p>
            : <p className="small src-note warn">{app.link_detail || 'Not allowed yet'}. Allow it in Pocket Core → Links (Alerts → {app.name.replace('Pocket ', '')}, events:read).</p>)}
        </MSection>
      )}

      <MSection title="How it behaves" icon="sliders">
        <SettingRow title="Name">
          <NameInput value={x.name} onSave={v => save({ name: v })} />
        </SettingRow>
        <SettingRow title="Importance" help="When the sender does not say how important an alert is.">
          <div className="qh-control"><Select value={String(x.default_priority)} onChange={v => save({ default_priority: Number(v) })} options={prioOpts} ariaLabel="Importance" /></div>
        </SettingRow>
        <SettingRow title="Muted" help="Alerts still come into your inbox, but never to your phone.">
          <Switch checked={x.muted} onChange={v => save({ muted: v })} label="Muted" />
        </SettingRow>
        <SettingRow title="On" help="Switched off, nothing from this source comes in at all.">
          <Switch checked={x.enabled} onChange={v => save({ enabled: v })} label="On" />
        </SettingRow>
      </MSection>

      {x.kind === 'push' && isAdmin && (
        <MSection title="Old ntfy and Apprise addresses" icon="link" sub="For senders that used ntfy or Apprise on this network: they keep the address they have, and dashboards that read ntfy keep reading. Only admins see this.">
          <SettingRow title="Topic" help={x.topic ? <>Plain addresses: <code>/{x.topic}</code> (ntfy) and <code>/notify/{x.topic}</code> (Apprise).</> : 'A short name, like alerts or backups.'}>
            <NameInput value={x.topic} onSave={v => save({ topic: v })} label="Topic" />
          </SettingRow>
          <SettingRow title="Take alerts at the plain name" help="No secret address needed, like ntfy and Apprise. Only safe on a private network; the name is shared by everyone on this box.">
            <Switch checked={x.open} disabled={!x.topic} onChange={v => save({ open: v }, v ? 'Open at its plain name' : 'Only the secret address now')} label="Take alerts at the plain name" />
          </SettingRow>
          <SettingRow title="Show on the ntfy topic" help={<>Things that read ntfy topics (a dashboard drawer, a phone relay) see these alerts at <code>/{x.feed_topic || 'topic'}/json</code>. Empty = on no topic.</>}>
            <NameInput value={x.feed_topic} onSave={v => save({ feed_topic: v })} empty label="ntfy topic" />
          </SettingRow>
        </MSection>
      )}

      <MSection title="Latest alerts" icon="inbox" flush actions={<Link to={`/?source=${x.id}`} className="btn sm ghost">In the inbox</Link>}>
        {!recent ? <div className="set-section-body"><Loading /></div> : recent.length === 0
          ? <Empty icon="inbox" title="Nothing yet">Send a test to see it arrive.</Empty>
          : (
            <ul className="src-alerts">
              {recent.map(a => (
                <li key={a.id}>
                  <Link to={`/alerts/${a.id}`} className="src-alert">
                    <span className="src-alert-emo">{a.emoji || <Icon name="bell" size={14} />}</span>
                    <span className="src-alert-t">{a.title || a.body.split('\n')[0]}{a.count > 1 && <span className="src-alert-n"> ×{a.count}</span>}</span>
                    <span className="src-alert-when">{ago(a.last_seen_at)}</span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
      </MSection>

      <div className="row"><span className="spacer" /><button type="button" className="btn danger-ghost sm" onClick={() => setAsk('delete')}><Icon name="trash" size={14} />Remove this source</button></div>

      {ask === 'key' && <Confirm title="Make a new address?" message="The old address stops working at once: update every app and script that sends here." confirmLabel="Make a new one" onConfirm={newKey} onCancel={() => setAsk('')} />}
      {ask === 'delete' && <Confirm title={`Remove ${x.name}?`} message="Nothing more comes in from it. Its alerts stay in your inbox." confirmLabel="Remove" onConfirm={remove} onCancel={() => setAsk('')} />}
    </div>
  )
}

/** A name field that saves when you leave it. */
function NameInput({ value, onSave, empty = false, label = 'Name' }: { value: string; onSave: (v: string) => void; empty?: boolean; label?: string }) {
  const [v, setV] = useState(value)
  useEffect(() => setV(value), [value])
  return <input className="qh-control" value={v} maxLength={80} onChange={e => setV(e.target.value)} onBlur={() => { if ((v.trim() || empty) && v.trim() !== value) onSave(v.trim()) }} onKeyDown={e => { if (e.key === 'Enter') (e.target as HTMLInputElement).blur() }} aria-label={label} />
}

/** Server, topics and token of a followed ntfy server. */
function NtfyFollow({ x, onSave }: { x: Source; onSave: (p: Record<string, unknown>) => void }) {
  const { settings } = useApp()
  const [url, setUrl] = useState(x.follow_url)
  const [topics, setTopics] = useState(x.follow_topics)
  const [token, setToken] = useState(x.follow_token)
  useEffect(() => { setUrl(x.follow_url); setTopics(x.follow_topics); setToken(x.follow_token) }, [x.follow_url, x.follow_topics, x.follow_token])
  const dirty = url !== x.follow_url || topics !== x.follow_topics || token !== x.follow_token
  return (
    <MSection title="The ntfy server" icon="antenna"
      sub={x.status === 'ok' ? 'Connected: new messages arrive as they are posted.' : x.status === 'error' ? undefined : 'Connecting…'}>
      <div className="stack">
        {x.status === 'error' && <div className="callout danger"><Icon name="alert" size={15} /><div>Not connected: {x.status_note || 'the server did not answer'}. It keeps trying.</div></div>}
        <Field label="Server" help={settings && !settings.local_targets ? 'Addresses on the local network only work for the box’s admins.' : undefined}><input value={url} onChange={e => setUrl(e.target.value)} inputMode="url" /></Field>
        <Field label="Topics" help="Separated by commas."><input value={topics} onChange={e => setTopics(e.target.value)} /></Field>
        <Field label="Access token" help="Only if the topics are protected: an ntfy access token, or user:password."><input value={token} onChange={e => setToken(e.target.value)} autoComplete="off" /></Field>
        <div className="form-actions"><button type="button" className="btn primary sm" disabled={!dirty} onClick={() => onSave({ follow_url: url, follow_topics: topics, follow_token: token })}>Save</button></div>
      </div>
    </MSection>
  )
}
