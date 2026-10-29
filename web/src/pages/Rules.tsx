// Rules: what to do with alerts that match, checked from the top; the first match decides.
import { useCallback, useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { ago } from '../lib/format'
import type { Channel, Rule, Source } from '../lib/types'
import { Icon } from '../components/icons'
import { RuleEditor, ruleSummary } from '../components/RuleEditor'
import { MSection, useNewParam } from '../components/SrcParts'
import { Empty, Loading, Switch, errMsg, useToast } from '../components/ui'

const ACTION_ICON: Record<Rule['action'], string> = { send: 'bell', quiet: 'bell-off', drop: 'trash' }

export default function Rules() {
  const { toast } = useToast()
  const [rules, setRules] = useState<Rule[] | null>(null)
  const [sources, setSources] = useState<Source[]>([])
  const [channels, setChannels] = useState<Channel[]>([])
  const [err, setErr] = useState('')
  const [edit, setEdit] = useState<Rule | 'new' | null>(null)
  const load = useCallback(() => {
    api.get<Rule[]>(`${V1}/rules`).then(r => { setRules(r); setErr('') }).catch(e => setErr(errMsg(e)))
    api.get<Source[]>(`${V1}/sources`).then(setSources).catch(() => {})
    api.get<Channel[]>(`${V1}/channels`).then(setChannels).catch(() => {})
  }, [])
  useEffect(() => { load() }, [load])
  const clearNew = useNewParam(() => setEdit('new'))
  const close = () => { setEdit(null); clearNew() }

  const toggle = async (r: Rule, enabled: boolean) => {
    setRules(l => l && l.map(x => (x.id === r.id ? { ...x, enabled } : x)))
    try { await api.put(`${V1}/rules/${r.id}`, { enabled }) } catch (e) { toast(errMsg(e), 'err'); load() }
  }
  const move = async (i: number, d: -1 | 1) => {
    if (!rules) return
    const next = [...rules]
    const [x] = next.splice(i, 1)
    next.splice(i + d, 0, x!)
    setRules(next)
    try { await api.post(`${V1}/rules/reorder`, { ids: next.map(r => r.id) }) } catch (e) { toast(errMsg(e), 'err'); load() }
  }

  return (
    <div className="src-page">
      <p className="src-intro">Rules decide what happens to alerts: keep some quiet, throw some away, send others somewhere special or even at night. They are checked from the top; the first that matches decides.</p>
      <MSection title="Your rules" icon="sliders" sub="Switch one off to stop it without losing it." actions={<button type="button" className="btn sm primary" onClick={() => setEdit('new')}><Icon name="plus" size={14} />Make a rule</button>} flush>
        {err ? <div className="set-section-body"><p className="src-err">{err}</p></div>
          : !rules ? <div className="set-section-body"><Loading /></div>
            : rules.length === 0 ? (
              <Empty icon="sliders" title="No rules yet">
                Without rules, alerts go to your phone by their importance and quiet hours. In the inbox, “Keep similar ones quiet” makes a rule for you.
              </Empty>
            ) : (
              <ul className="rule-list">
                {rules.map((r, i) => (
                  <li key={r.id}>
                    <div className={`rule-row ${r.enabled ? '' : 'off'}`} role="button" tabIndex={0} onClick={() => setEdit(r)} onKeyDown={e => { if (e.key === 'Enter') setEdit(r) }}>
                      <span className="rule-order">{i + 1}</span>
                      <span className="rule-ico"><Icon name={ACTION_ICON[r.action]} size={16} /></span>
                      <span className="rule-main">
                        <span className="rule-name">{r.name}</span>
                        <span className="rule-sub">{ruleSummary(r, sources, channels)}</span>
                      </span>
                      <span className="src-side" onClick={e => e.stopPropagation()}>
                        <span className="rule-hits" title="How often it matched"><b>{r.hits}</b>{r.last_hit_at ? `last ${ago(r.last_hit_at)}` : 'not yet'}</span>
                        {rules.length > 1 && (
                          <span className="src-move">
                            <button type="button" className="btn ghost" aria-label="Move up" disabled={i === 0} onClick={() => move(i, -1)}><Icon name="chevron-up" size={14} /></button>
                            <button type="button" className="btn ghost" aria-label="Move down" disabled={i === rules.length - 1} onClick={() => move(i, 1)}><Icon name="chevron-down" size={14} /></button>
                          </span>
                        )}
                        <Switch checked={r.enabled} onChange={v => toggle(r, v)} label={`${r.name} on`} />
                      </span>
                    </div>
                  </li>
                ))}
              </ul>
            )}
      </MSection>
      {edit && (
        <RuleEditor rule={edit === 'new' ? null : edit} sources={sources} channels={channels} onClose={close}
          onSaved={r => { close(); load(); toast(edit === 'new' ? `Rule “${r.name}” made` : 'Saved') }}
          onDeleted={() => { close(); load(); toast('Rule removed') }} />
      )}
    </div>
  )
}
