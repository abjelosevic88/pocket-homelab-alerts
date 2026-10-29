// Where alerts go: your phone apps, an Apprise gateway, webhooks — each with how important an alert must be.
import { useCallback, useEffect, useState } from 'react'
import { api, V1 } from '../lib/api'
import { ago } from '../lib/format'
import { priorityLabel, type Channel } from '../lib/types'
import { Icon } from '../components/icons'
import { CHANNEL_KINDS, ChanEditor, kindName } from '../components/ChanEditor'
import { MSection, useNewParam } from '../components/SrcParts'
import { announceChange } from '../components/Layout'
import { Empty, Loading, Switch, errMsg, useToast } from '../components/ui'

export default function SendTo() {
  const { toast } = useToast()
  const [list, setList] = useState<Channel[] | null>(null)
  const [err, setErr] = useState('')
  const [edit, setEdit] = useState<Channel | 'new' | null>(null)
  const load = useCallback(() => { api.get<Channel[]>(`${V1}/channels`).then(l => { setList(l); setErr('') }).catch(e => setErr(errMsg(e))) }, [])
  useEffect(() => { load() }, [load])
  const clearNew = useNewParam(() => setEdit('new'))
  const close = () => { setEdit(null); clearNew() }

  const toggle = async (c: Channel, enabled: boolean) => {
    setList(l => l && l.map(x => (x.id === c.id ? { ...x, enabled } : x)))
    try { await api.put(`${V1}/channels/${c.id}`, { enabled }); announceChange() } catch (e) { toast(errMsg(e), 'err'); load() }
  }
  const move = async (i: number, d: -1 | 1) => {
    if (!list) return
    const next = [...list]
    const [x] = next.splice(i, 1)
    next.splice(i + d, 0, x!)
    setList(next)
    try { await api.post(`${V1}/channels/reorder`, { ids: next.map(c => c.id) }) } catch (e) { toast(errMsg(e), 'err'); load() }
  }

  return (
    <div className="src-page">
      <p className="src-intro">Alerts that matter go from here to your phone or anywhere else. Each place takes alerts from a level of importance up; quiet hours and rules decide the rest.</p>
      <MSection title="Places alerts go to" icon="smartphone" sub="Switch one off to pause it." actions={<button type="button" className="btn sm primary" onClick={() => setEdit('new')}><Icon name="plus" size={14} />Add</button>} flush>
        {err ? <div className="set-section-body"><p className="src-err">{err}</p></div>
          : !list ? <div className="set-section-body"><Loading /></div>
            : list.length === 0 ? (
              <Empty icon="smartphone" title="Nothing goes to your phone yet">
                Alerts are kept in your inbox. Add Pushover, ntfy, Apprise or a webhook to be told about them.
              </Empty>
            ) : (
              <ul className="chan-list">
                {list.map((c, i) => {
                  const k = CHANNEL_KINDS.find(x => x.id === c.kind)
                  return (
                    <li key={c.id}>
                      <div className={`chan-row ${c.enabled ? '' : 'off'}`} role="button" tabIndex={0} onClick={() => setEdit(c)} onKeyDown={e => { if (e.key === 'Enter') setEdit(c) }}>
                        <span className="chan-ico"><Icon name={k?.icon || 'bell'} size={17} /></span>
                        <span className="chan-main">
                          <span className="row"><span className="chan-name">{c.name}</span>{c.enabled && c.last_status === 'error' && <span className="src-pill bad"><Icon name="alert" size={12} />Failing</span>}</span>
                          <span className="chan-sub">{kindName(c.kind)} · gets {priorityLabel(c.min_priority).toLowerCase()} and up</span>
                          {c.last_status === 'error' && c.last_error
                            ? <span className="src-note bad ellipsis">{c.last_error}</span>
                            : <span className="src-note">{c.last_sent_at ? `Last sent ${ago(c.last_sent_at)}` : 'Nothing sent yet'}</span>}
                        </span>
                        <span className="src-side" onClick={e => e.stopPropagation()}>
                          {list.length > 1 && (
                            <span className="src-move">
                              <button type="button" className="btn ghost" aria-label="Move up" disabled={i === 0} onClick={() => move(i, -1)}><Icon name="chevron-up" size={14} /></button>
                              <button type="button" className="btn ghost" aria-label="Move down" disabled={i === list.length - 1} onClick={() => move(i, 1)}><Icon name="chevron-down" size={14} /></button>
                            </span>
                          )}
                          <Switch checked={c.enabled} onChange={v => toggle(c, v)} label={`${c.name} on`} />
                        </span>
                      </div>
                    </li>
                  )
                })}
              </ul>
            )}
      </MSection>
      {edit && (
        <ChanEditor channel={edit === 'new' ? null : edit} onClose={close}
          onSaved={c => { close(); announceChange(); load(); toast(edit === 'new' ? `${c.name} added` : 'Saved') }}
          onDeleted={() => { close(); announceChange(); load(); toast('Removed') }} />
      )}
    </div>
  )
}
