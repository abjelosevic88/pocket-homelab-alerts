// "Add a source": an address of its own (anything that can post) or following topics on an ntfy server.
import { useState } from 'react'
import { api, V1 } from '../lib/api'
import type { Source } from '../lib/types'
import { Icon } from './icons'
import { Field, Modal, errMsg } from './ui'

type Kind = 'push' | 'ntfy'

export function SrcEditor({ onClose, onSaved }: { onClose: () => void; onSaved: (s: Source) => void }) {
  const [kind, setKind] = useState<Kind>('push')
  const [name, setName] = useState('')
  const [server, setServer] = useState('https://ntfy.sh')
  const [topics, setTopics] = useState('')
  const [token, setToken] = useState('')
  const [backfill, setBackfill] = useState(true)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const save = async () => {
    setErr('')
    if (kind === 'push' && !name.trim()) { setErr('Give it a name, like “Backups” or “Sonarr”.'); return }
    if (kind === 'ntfy' && (!server.trim() || !topics.trim())) { setErr('Say which ntfy server and which topics to follow.'); return }
    setBusy(true)
    try {
      const body = kind === 'push'
        ? { kind, name: name.trim() }
        : { kind, name: name.trim() || topics.trim(), follow_url: server.trim(), follow_topics: topics.trim(), follow_token: token.trim(), backfill }
      onSaved(await api.post<Source>(`${V1}/sources`, body))
    } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }

  const kinds: { id: Kind; icon: string; t: string; d: string }[] = [
    { id: 'push', icon: 'link', t: 'Give it an address', d: 'A script, an app or a service sends its alerts to an address of its own (Apprise, ntfy or a webhook).' },
    { id: 'ntfy', icon: 'antenna', t: 'Follow an ntfy server', d: 'Bring in what already goes to topics on an ntfy server, without changing the senders.' },
  ]
  return (
    <Modal title="Add a source" onClose={onClose} footer={<>
      <span className="src-err grow">{err}</span>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" onClick={save} disabled={busy}>{kind === 'push' ? 'Make the address' : 'Start following'}</button>
    </>}>
      <form className="stack" onSubmit={e => { e.preventDefault(); save() }}>
        <div className="src-kinds" role="radiogroup" aria-label="Kind of source">
          {kinds.map(k => (
            <button key={k.id} type="button" role="radio" aria-checked={kind === k.id} className={`src-kind ${kind === k.id ? 'active' : ''}`} onClick={() => setKind(k.id)}>
              <span className="src-kind-t"><Icon name={k.icon} size={16} />{k.t}</span>
              <span className="src-kind-d">{k.d}</span>
            </button>
          ))}
        </div>
        <Field label="Name" help={kind === 'push' ? 'What sends here, so you know where an alert came from.' : 'Leave empty to use the topic names.'}>
          <input autoFocus value={name} onChange={e => setName(e.target.value)} placeholder={kind === 'push' ? 'Backups' : 'Home ntfy'} maxLength={80} />
        </Field>
        {kind === 'ntfy' && <>
          <Field label="ntfy server" help="The address you open the ntfy web app at.">
            <input value={server} onChange={e => setServer(e.target.value)} placeholder="https://ntfy.sh" inputMode="url" />
          </Field>
          <Field label="Topics" help="One or more, separated by commas.">
            <input value={topics} onChange={e => setTopics(e.target.value)} placeholder="alerts, downloads" />
          </Field>
          <Field label="Access token (if the topics are protected)" help="An ntfy access token, or user:password.">
            <input value={token} onChange={e => setToken(e.target.value)} placeholder="tk_…" autoComplete="off" />
          </Field>
          <label className="src-check"><input type="checkbox" checked={backfill} onChange={e => setBackfill(e.target.checked)} />Bring in the last 12 hours too (what the server still keeps)</label>
        </>}
        <button type="submit" hidden />
      </form>
    </Modal>
  )
}
