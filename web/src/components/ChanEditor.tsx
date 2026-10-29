// Add or change a place alerts go to: Pushover, ntfy, Apprise or a webhook.
import { useEffect, useState, type ChangeEvent } from 'react'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { ago } from '../lib/format'
import type { Channel, ChannelKind, Delivery } from '../lib/types'
import { Icon } from './icons'
import { Select } from './Select'
import { prioOptions } from './SrcParts'
import { Confirm, Field, Modal, SettingRow, Spinner, Switch, errMsg } from './ui'

export const CHANNEL_KINDS: { id: ChannelKind; name: string; icon: string; d: string }[] = [
  { id: 'pushover', name: 'Pushover', icon: 'smartphone', d: 'The Pushover app on your phone, tablet or watch. Reliable, with sounds and quiet levels.' },
  { id: 'ntfy', name: 'ntfy', icon: 'bell', d: 'A topic on an ntfy server; the ntfy app shows what arrives.' },
  { id: 'apprise', name: 'Apprise', icon: 'share', d: 'An Apprise gateway: Telegram, email, Signal, Slack and about a hundred more.' },
  { id: 'webhook', name: 'Webhook', icon: 'code', d: 'Any address that takes the alert as JSON (Home Assistant, n8n, your own script).' },
]

export const kindName = (k: string) => CHANNEL_KINDS.find(c => c.id === k)?.name || k

const PUSHOVER_MAX = [
  { value: '', label: 'No limit' },
  { value: '1', label: 'High', hint: 'skips the phone’s quiet hours' },
  { value: '0', label: 'Normal', hint: 'sound and vibration' },
  { value: '-1', label: 'Low', hint: 'no sound' },
  { value: '-2', label: 'Lowest', hint: 'no notification at all' },
]

type Test = { busy: boolean; ok?: boolean; msg?: string }

export function ChanEditor({ channel, onClose, onSaved, onDeleted }: { channel: Channel | null; onClose: () => void; onSaved: (c: Channel) => void; onDeleted: () => void }) {
  const { settings } = useApp()
  const [kind, setKind] = useState<ChannelKind>(channel?.kind || 'pushover')
  const [name, setName] = useState(channel?.name || '')
  const [cfg, setCfg] = useState<Record<string, string>>(channel?.config || { url: 'https://ntfy.sh' })
  const [minP, setMinP] = useState(channel?.min_priority || 1)
  const [enabled, setEnabled] = useState(channel?.enabled ?? true)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [test, setTest] = useState<Test>({ busy: false })
  const [sends, setSends] = useState<Delivery[] | null>(null)
  const [del, setDel] = useState(false)

  useEffect(() => {
    if (!channel) return
    api.get<Delivery[]>(`${V1}/channels/${channel.id}/deliveries`).then(setSends).catch(() => setSends([]))
  }, [channel])

  const pick = (k: ChannelKind) => {
    setKind(k)
    setCfg(k === 'ntfy' ? { url: 'https://ntfy.sh' } : {})
    setTest({ busy: false })
  }
  const set = (k: string) => (e: ChangeEvent<HTMLInputElement>) => setCfg(c => ({ ...c, [k]: e.target.value }))
  const body = () => ({ kind, name: name.trim() || kindName(kind), config: cfg, min_priority: minP, enabled })

  const save = async () => {
    setErr(''); setBusy(true)
    try {
      const c = channel ? await api.put<Channel>(`${V1}/channels/${channel.id}`, body()) : await api.post<Channel>(`${V1}/channels`, body())
      onSaved(c)
    } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }
  const runTest = async () => {
    setTest({ busy: true })
    try {
      const r = await api.post<{ ok: boolean; error?: string }>(channel ? `${V1}/channels/${channel.id}/test` : `${V1}/channels/test`, body())
      setTest({ busy: false, ok: r.ok, msg: r.ok ? 'Sent. Check that it arrived.' : r.error })
    } catch (e) { setTest({ busy: false, ok: false, msg: errMsg(e) }) }
  }
  const remove = async () => {
    setDel(false)
    if (!channel) return
    try { await api.del(`${V1}/channels/${channel.id}`); onDeleted() } catch (e) { setErr(errMsg(e)) }
  }

  const localNote = settings && !settings.local_targets ? ' Addresses on the local network only work for the box’s admins.' : ''
  return (
    <Modal title={channel ? channel.name : 'Add a place to send to'} onClose={onClose} size="lg" footer={<>
      {channel && <button type="button" className="btn danger-ghost" onClick={() => setDel(true)}><Icon name="trash" size={14} />Remove</button>}
      <span className="src-err grow">{err}</span>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" onClick={save} disabled={busy}>{channel ? 'Save' : 'Add'}</button>
    </>}>
      <form className="stack" onSubmit={e => { e.preventDefault(); save() }}>
        {!channel && (
          <div className="chan-kinds" role="radiogroup" aria-label="Where to send">
            {CHANNEL_KINDS.map(k => (
              <button key={k.id} type="button" role="radio" aria-checked={kind === k.id} className={`chan-kind ${kind === k.id ? 'active' : ''}`} onClick={() => pick(k.id)}>
                <span className="chan-kind-t"><Icon name={k.icon} size={16} />{k.name}</span>
                <span className="chan-kind-d">{k.d}</span>
              </button>
            ))}
          </div>
        )}
        <Field label="Name" help="So you can tell your places apart, like “iPhone” or “Family Telegram”.">
          <input value={name} onChange={e => setName(e.target.value)} placeholder={kindName(kind)} maxLength={80} />
        </Field>

        {kind === 'pushover' && <>
          <div className="two-col">
            <Field label="Your user key" help="On pushover.net, at the top once you sign in."><input value={cfg.user || ''} onChange={set('user')} autoComplete="off" placeholder="uQiRzpo4DXghDmr9QzzfQu27cmVRsG" /></Field>
            <Field label="Application token" help="pushover.net → Create an Application (call it Pocket Alerts)."><input value={cfg.token || ''} onChange={set('token')} autoComplete="off" placeholder="azGDORePK8gMaC0QOYAMyEEuzJnyUi" /></Field>
          </div>
          <div className="two-col">
            <Field label="Only this device (optional)" help="The device name in Pushover; empty = all your devices."><input value={cfg.device || ''} onChange={set('device')} /></Field>
            <Field label="Sound (optional)" help="A Pushover sound name, like “siren” or “none”."><input value={cfg.sound || ''} onChange={set('sound')} /></Field>
          </div>
          <Field label="Never louder than" group help="Caps how loud Pushover may be, whatever the alert says.">
            <Select value={cfg.max_priority || ''} onChange={v => setCfg(c => ({ ...c, max_priority: v }))} options={PUSHOVER_MAX} ariaLabel="Never louder than" />
          </Field>
        </>}

        {kind === 'ntfy' && <>
          <div className="two-col">
            <Field label="ntfy server" help={'The address of the ntfy server.' + localNote}><input value={cfg.url || ''} onChange={set('url')} inputMode="url" placeholder="https://ntfy.sh" /></Field>
            <Field label="Topic" help="Subscribe to the same topic in the ntfy app. On ntfy.sh, pick something hard to guess."><input value={cfg.topic || ''} onChange={set('topic')} placeholder="my-home-alerts" /></Field>
          </div>
          <Field label="Access token (optional)" help="An ntfy access token, or user:password, if the topic is protected."><input value={cfg.token || ''} onChange={set('token')} autoComplete="off" /></Field>
        </>}

        {kind === 'apprise' && <>
          <Field label="Apprise address" help={'The notify address of a route on your Apprise gateway.' + localNote}><input value={cfg.url || ''} onChange={set('url')} inputMode="url" placeholder="http://apprise:8000/notify/phone" /></Field>
          <Field label="Tag (optional)" help="Only the Apprise services with this tag."><input value={cfg.tag || ''} onChange={set('tag')} /></Field>
        </>}

        {kind === 'webhook' && <>
          <Field label="Address" help={'Gets each alert as JSON: title, body, importance, tags, click, source.' + localNote}><input value={cfg.url || ''} onChange={set('url')} inputMode="url" placeholder="https://example.com/hooks/alerts" /></Field>
          <Field label="Secret (optional)" help="Sent as “Authorization: Bearer …” so the other side knows it is you."><input value={cfg.secret || ''} onChange={set('secret')} autoComplete="off" /></Field>
        </>}

        <div>
          <SettingRow title="Send alerts this important and up" help="Less important ones still come to your inbox and to your other places.">
            <div className="qh-control"><Select value={String(minP)} onChange={v => setMinP(Number(v))} options={prioOptions(true)} ariaLabel="Send alerts this important and up" /></div>
          </SettingRow>
          <SettingRow title="On" help="Switched off, nothing is sent here.">
            <Switch checked={enabled} onChange={setEnabled} label="On" />
          </SettingRow>
          <SettingRow title="Try it" help="Sends one test message now, with what is typed above.">
            <button type="button" className="btn sm" onClick={runTest} disabled={test.busy}><Icon name="bell" size={14} />Send a test</button>
          </SettingRow>
          <div className="chan-test" aria-live="polite">
            {test.busy && <><Spinner />Sending…</>}
            {!test.busy && test.ok === true && <span className="ok-text"><Icon name="check" size={14} /> {test.msg}</span>}
            {!test.busy && test.ok === false && <span className="danger-text">{test.msg}</span>}
          </div>
        </div>

        {channel && (
          <Field label="Recent sends" group>
            {!sends ? <Spinner /> : sends.length === 0 ? <span className="muted small">Nothing sent here yet.</span> : (
              <ul className="chan-deliveries">
                {sends.slice(0, 20).map(d => (
                  <li key={d.id}>
                    <span className="when">{ago(d.at)}</span>
                    <span className="what">
                      {d.kind === 'test' ? 'Test' : d.kind === 'summary' ? 'Quiet-hours summary' : 'Alert'}
                      {' · '}
                      {d.status === 'sent' ? <span className="ok-text">sent</span> : d.status === 'retrying' ? <span className="src-note warn">trying again{d.error ? `: ${d.error}` : ''}</span> : <span className="danger-text">failed: {d.error}</span>}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </Field>
        )}
        <button type="submit" hidden />
      </form>
      {del && channel && <Confirm title={`Remove ${channel.name}?`} message="Nothing more is sent there. Rules that send only there fall back to your other places." confirmLabel="Remove" onConfirm={remove} onCancel={() => setDel(false)} />}
    </Modal>
  )
}
