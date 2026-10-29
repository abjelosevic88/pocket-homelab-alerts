// Make or change a rule: which alerts (source, importance, words, tags) and what happens to them.
import { useEffect, useMemo, useState } from 'react'
import { api, V1 } from '../lib/api'
import { PRIORITIES, priorityLabel, type Channel, type Rule, type RuleMatch, type RulePreview, type Source } from '../lib/types'
import { Icon } from './icons'
import { Select } from './Select'
import { joinWords } from './SrcParts'
import { Confirm, Field, Modal, Segmented, SettingRow, Spinner, Switch, errMsg, useDebounce } from './ui'

const emptyMatch = (): RuleMatch => ({ source_ids: [], min_priority: 0, max_priority: 0, words: '', tags: '' })

const ACTIONS: { value: Rule['action']; label: string }[] = [
  { value: 'send', label: 'Send to me' },
  { value: 'quiet', label: 'Keep quiet' },
  { value: 'drop', label: 'Don’t keep' },
]

const ACTION_HELP: Record<Rule['action'], string> = {
  send: 'It is sent, even if it is less important than what you usually get pinged for.',
  quiet: 'It comes into your inbox, but nothing is sent.',
  drop: 'It is thrown away: not in your inbox, not sent.',
}

/** One line in words: "From Downloads, containing “test” → keep quiet". */
export function ruleSummary(r: Pick<Rule, 'match' | 'action' | 'priority' | 'channel_ids' | 'break_quiet'>, sources: Source[], channels: Channel[]): string {
  const m = r.match
  const parts: string[] = []
  const names = m.source_ids.map(id => sources.find(s => s.id === id)?.name).filter(Boolean) as string[]
  parts.push(names.length ? `From ${joinWords(names, 'or')}` : 'Any alert')
  if (m.min_priority && m.max_priority) parts.push(m.min_priority === m.max_priority ? `${priorityLabel(m.min_priority).toLowerCase()}` : `${priorityLabel(m.min_priority).toLowerCase()} to ${priorityLabel(m.max_priority).toLowerCase()}`)
  else if (m.min_priority) parts.push(`${priorityLabel(m.min_priority).toLowerCase()} and up`)
  else if (m.max_priority) parts.push(`${priorityLabel(m.max_priority).toLowerCase()} and below`)
  const words = m.words.split(',').map(w => w.trim()).filter(Boolean)
  if (words.length) parts.push(`containing ${joinWords(words.map(w => `“${w}”`), 'or')}`)
  const tags = m.tags.split(',').map(w => w.trim()).filter(Boolean)
  if (tags.length) parts.push(`tagged ${joinWords(tags, 'or')}`)
  let then = r.action === 'drop' ? 'don’t keep' : r.action === 'quiet' ? 'keep quiet' : 'send'
  if (r.action === 'send') {
    const chans = r.channel_ids.map(id => channels.find(c => c.id === id)?.name).filter(Boolean) as string[]
    if (chans.length) then += ` to ${joinWords(chans)}`
    if (r.priority) then += ` as ${priorityLabel(r.priority).toLowerCase()}`
    if (r.break_quiet) then += ', even in quiet hours'
  }
  return `${parts.join(', ')} → ${then}`
}

export function RuleEditor({ rule, sources, channels, onClose, onSaved, onDeleted }: { rule: Rule | null; sources: Source[]; channels: Channel[]; onClose: () => void; onSaved: (r: Rule) => void; onDeleted: () => void }) {
  const [name, setName] = useState(rule?.name || '')
  const [match, setMatch] = useState<RuleMatch>(rule ? { ...emptyMatch(), ...rule.match, source_ids: rule.match.source_ids || [] } : emptyMatch())
  const [action, setAction] = useState<Rule['action']>(rule?.action || 'quiet')
  const [priority, setPriority] = useState(rule?.priority || 0)
  const [chans, setChans] = useState<number[]>(rule?.channel_ids || [])
  const [breakQuiet, setBreakQuiet] = useState(rule?.break_quiet || false)
  const [enabled] = useState(rule?.enabled ?? true)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [del, setDel] = useState(false)
  const [preview, setPreview] = useState<RulePreview | null>(null)
  const [pbusy, setPbusy] = useState(false)

  const dm = useDebounce(match, 400)
  const key = useMemo(() => JSON.stringify(dm), [dm])
  useEffect(() => {
    let live = true
    setPbusy(true)
    api.post<RulePreview>(`${V1}/rules/preview`, { match: dm }).then(p => { if (live) setPreview(p) }).catch(() => {}).finally(() => { if (live) setPbusy(false) })
    return () => { live = false }
  }, [key]) // eslint-disable-line react-hooks/exhaustive-deps

  const toggleIn = (list: number[], id: number) => (list.includes(id) ? list.filter(x => x !== id) : [...list, id])
  const summary = ruleSummary({ match, action, priority, channel_ids: chans, break_quiet: breakQuiet }, sources, channels)

  const save = async () => {
    setErr('')
    const n = name.trim() || summary.slice(0, 80)
    setBusy(true)
    const body = { name: n, enabled, match, action, priority: action === 'send' ? priority : 0, channel_ids: action === 'send' ? chans : [], break_quiet: action === 'send' && breakQuiet }
    try {
      onSaved(rule ? await api.put<Rule>(`${V1}/rules/${rule.id}`, body) : await api.post<Rule>(`${V1}/rules`, body))
    } catch (e) { setErr(errMsg(e)) } finally { setBusy(false) }
  }
  const remove = async () => {
    setDel(false)
    if (!rule) return
    try { await api.del(`${V1}/rules/${rule.id}`); onDeleted() } catch (e) { setErr(errMsg(e)) }
  }

  const any = [{ value: '0', label: 'Any' }]
  const prio = PRIORITIES.map(p => ({ value: String(p.value), label: p.label, hint: p.hint }))
  return (
    <Modal title={rule ? 'Change the rule' : 'Make a rule'} onClose={onClose} size="lg" footer={<>
      {rule && <button type="button" className="btn danger-ghost" onClick={() => setDel(true)}><Icon name="trash" size={14} />Remove</button>}
      <span className="src-err grow">{err}</span>
      <button type="button" className="btn" onClick={onClose}>Cancel</button>
      <button type="button" className="btn primary" onClick={save} disabled={busy}>{rule ? 'Save' : 'Make the rule'}</button>
    </>}>
      <form className="stack" onSubmit={e => { e.preventDefault(); save() }}>
        <Field label="Name" help="Leave empty to name it after what it does.">
          <input value={name} onChange={e => setName(e.target.value)} placeholder="Downloads without sound" maxLength={80} />
        </Field>

        <h3 className="small muted">When an alert…</h3>
        <Field label="comes from" group help={match.source_ids.length ? undefined : 'Any source. Pick some to narrow it.'}>
          {sources.length === 0 ? <span className="muted small">You have no sources yet.</span> : (
            <div className="rule-chips">
              {sources.map(s => {
                const on = match.source_ids.includes(s.id)
                return (
                  <button key={s.id} type="button" aria-pressed={on} className={`chip rule-chip ${on ? 'active' : ''}`} onClick={() => setMatch(m => ({ ...m, source_ids: toggleIn(m.source_ids, s.id) }))}>
                    {on && <Icon name="check" size={13} />}{s.name}
                  </button>
                )
              })}
            </div>
          )}
        </Field>
        <div className="two-col">
          <Field label="is at least" group><Select value={String(match.min_priority)} onChange={v => setMatch(m => ({ ...m, min_priority: Number(v) }))} options={[...any, ...prio]} ariaLabel="At least this important" /></Field>
          <Field label="and at most" group><Select value={String(match.max_priority)} onChange={v => setMatch(m => ({ ...m, max_priority: Number(v) }))} options={[...any, ...prio]} ariaLabel="At most this important" /></Field>
        </div>
        <div className="two-col">
          <Field label="contains any of these words" help="In the title or the text; separate with commas."><input value={match.words} onChange={e => setMatch(m => ({ ...m, words: e.target.value }))} placeholder="test, scrub finished" /></Field>
          <Field label="has any of these tags" help="Separate with commas."><input value={match.tags} onChange={e => setMatch(m => ({ ...m, tags: e.target.value }))} placeholder="warning, nas" /></Field>
        </div>

        <div className="rule-preview" aria-live="polite">
          {!preview ? <span className="row muted"><Spinner />Looking at your last 30 days…</span> : <>
            <span className="row">
              <span className="grow">{preview.matched === 0 ? 'Would not have caught any of the last 30 days’ alerts.' : <>Would have caught <b>{preview.matched}</b> of the last 30 days’ alerts.</>}</span>
              {pbusy && <Spinner />}
            </span>
            {preview.examples.length > 0 && <ul>{preview.examples.map(a => <li key={a.id}>{a.emoji ? `${a.emoji} ` : ''}{a.title || a.body.split('\n')[0]} <span className="muted">— {a.source_name}</span></li>)}</ul>}
          </>}
        </div>

        <h3 className="small muted">…then</h3>
        <div className="rule-then">
          <Segmented options={ACTIONS} value={action} onChange={setAction} label="What happens" />
          <p className="muted small rule-explain">{ACTION_HELP[action]}</p>
          {action === 'send' && <>
            <SettingRow title="Importance" help="Change how important it counts (and so how loud it is).">
              <div className="qh-control"><Select value={String(priority)} onChange={v => setPriority(Number(v))} options={[{ value: '0', label: 'Keep as it is' }, ...prio]} ariaLabel="Importance" /></div>
            </SettingRow>
            <SettingRow title="Even during quiet hours" help="It comes through at once instead of waiting for the morning.">
              <Switch checked={breakQuiet} onChange={setBreakQuiet} label="Even during quiet hours" />
            </SettingRow>
            <Field label="Send it to" group help={chans.length ? 'Only these places.' : 'The usual places (every place that takes alerts this important).'}>
              {channels.length === 0 ? <span className="muted small">You have no places to send to yet.</span> : (
                <div className="rule-chips">
                  {channels.map(c => {
                    const on = chans.includes(c.id)
                    return (
                      <button key={c.id} type="button" aria-pressed={on} className={`chip rule-chip ${on ? 'active' : ''}`} onClick={() => setChans(l => toggleIn(l, c.id))}>
                        {on && <Icon name="check" size={13} />}{c.name}{!c.enabled && <span className="muted"> (off)</span>}
                      </button>
                    )
                  })}
                </div>
              )}
            </Field>
          </>}
        </div>
        <p className="muted small rule-explain"><Icon name="info" size={13} /> {summary}</p>
        <button type="submit" hidden />
      </form>
      {del && rule && <Confirm title={`Remove “${rule.name}”?`} message="Alerts it caught are treated like any other from now on." confirmLabel="Remove" onConfirm={remove} onCancel={() => setDel(false)} />}
    </Modal>
  )
}
