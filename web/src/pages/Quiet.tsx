// Quiet hours and how often you get pinged: nights without buzzing, one summary in the morning, repeats counted instead of sent.
import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { plural } from '../lib/format'
import type { Settings } from '../lib/types'
import { Icon } from '../components/icons'
import { Select } from '../components/Select'
import { MSection, prioOptions } from '../components/SrcParts'
import { announceChange, useCounts } from '../components/Layout'
import { Loading, SettingRow, Switch, errMsg, useToast } from '../components/ui'

// Monday first; the values are 0 = Sunday … 6 = Saturday
const DAYS: [number, string][] = [[1, 'Mon'], [2, 'Tue'], [3, 'Wed'], [4, 'Thu'], [5, 'Fri'], [6, 'Sat'], [0, 'Sun']]

const LET_THROUGH = [
  { value: '5', label: 'Urgent only', hint: 'everything else waits' },
  { value: '4', label: 'High and urgent', hint: 'normal and low wait' },
]

const GROUP = [
  { value: '0', label: 'Off', hint: 'every repeat pings you' },
  { value: '10', label: '10 minutes' },
  { value: '30', label: '30 minutes' },
  { value: '60', label: 'An hour' },
  { value: '180', label: '3 hours' },
]

export default function Quiet() {
  const { settings, setSettings } = useApp()
  const counts = useCounts()
  const { toast } = useToast()
  const [start, setStart] = useState(settings?.quiet_start || '22:30')
  const [end, setEnd] = useState(settings?.quiet_end || '07:00')
  useEffect(() => { if (settings) { setStart(settings.quiet_start); setEnd(settings.quiet_end) } }, [settings?.quiet_start, settings?.quiet_end]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!settings) return <div className="src-page"><Loading /></div>

  const save = async (patch: Partial<Settings>) => {
    setSettings({ ...settings, ...patch })
    try {
      setSettings(await api.put<Settings>(`${V1}/settings`, patch))
      announceChange()
      toast('Saved')
    } catch (e) {
      toast(errMsg(e), 'err')
      api.get<Settings>(`${V1}/settings`).then(setSettings).catch(() => {})
    }
  }
  const saveTime = (k: 'quiet_start' | 'quiet_end', v: string) => { if (/^\d{2}:\d{2}$/.test(v) && v !== settings[k]) save({ [k]: v }) }
  const days = settings.quiet_days || []
  const toggleDay = (d: number) => {
    let next = days.includes(d) ? days.filter(x => x !== d) : [...days, d]
    if (next.length === 7) next = []
    save({ quiet_days: next })
  }
  const everyNight = days.length === 0
  const held = counts?.counts.held || 0

  return (
    <div className="src-page">
      <div className={`qh-now ${counts?.quiet ? 'on' : ''}`}>
        <Icon name={counts?.quiet ? 'moon' : 'bell'} size={20} />
        <div className="grow">
          <div className="qh-now-t">{counts?.quiet ? `Quiet now, until ${counts.quiet_until}` : settings.quiet_enabled ? `Alerts reach you now; quiet from ${settings.quiet_start}` : 'Alerts reach you at any hour'}</div>
          <div className="muted small">
            {held > 0 ? <>{plural(held, 'alert')} waiting for the summary. <Link to="/">See them in the inbox</Link></> : counts?.quiet ? 'Nothing is waiting.' : settings.quiet_enabled ? 'Less important alerts will wait during the night.' : 'Switch quiet hours on to sleep without buzzing.'}
          </div>
        </div>
      </div>

      <MSection title="Quiet hours" icon="moon" sub="During quiet hours, alerts still come into your inbox, but only the important ones reach your phone. The rest wait and come as one summary when quiet hours end.">
        <SettingRow title="Quiet hours" help="On every night you pick below.">
          <Switch checked={settings.quiet_enabled} onChange={v => save({ quiet_enabled: v })} label="Quiet hours" />
        </SettingRow>
        <SettingRow title="From – to" help={`In the server’s time zone${settings.timezone ? ` (${settings.timezone})` : ''}.`}>
          <span className="qh-times">
            <input type="time" value={start} onChange={e => setStart(e.target.value)} onBlur={() => saveTime('quiet_start', start)} aria-label="Quiet from" />
            <span className="muted">–</span>
            <input type="time" value={end} onChange={e => setEnd(e.target.value)} onBlur={() => saveTime('quiet_end', end)} aria-label="Quiet until" />
          </span>
        </SettingRow>
        <SettingRow title="Nights" help={everyNight ? 'Every night. Pick some to keep quiet hours only then.' : 'Only these nights (a night belongs to the day it starts).'} stack>
          <div className="qh-days" role="group" aria-label="Nights">
            {DAYS.map(([d, label]) => {
              const on = everyNight || days.includes(d)
              return <button key={d} type="button" aria-pressed={on && !everyNight} className={`chip qh-day ${on ? 'active' : ''}`} onClick={() => toggleDay(d)}>{label}</button>
            })}
          </div>
        </SettingRow>
        <SettingRow title="Still let through" help="Alerts this important reach you even during quiet hours. Rules can let others through too.">
          <div className="qh-control"><Select value={String(settings.quiet_min_priority)} onChange={v => save({ quiet_min_priority: Number(v) })} options={LET_THROUGH} ariaLabel="Still let through" /></div>
        </SettingRow>
        <SettingRow title="One summary in the morning" help="When quiet hours end, one message lists what came in. Off: what waited stays in the inbox only.">
          <Switch checked={settings.quiet_summary} onChange={v => save({ quiet_summary: v })} label="One summary in the morning" />
        </SettingRow>
      </MSection>

      <MSection title="How often you get pinged" icon="bell" sub="Fewer, better pings at any hour.">
        <SettingRow title="Ping me for" help="Less important alerts are kept in your inbox without a ping. Each place you send to can ask for more (Send to).">
          <div className="qh-control"><Select value={String(settings.send_min_priority)} onChange={v => save({ send_min_priority: Number(v) })} options={prioOptions(true)} ariaLabel="Ping me for" /></div>
        </SettingRow>
        <SettingRow title="Count repeats for" help="When the same alert comes again while it is still open (a disk at 91%, then 92%), it counts up on the first one instead of pinging again.">
          <div className="qh-control"><Select value={String(settings.group_minutes)} onChange={v => save({ group_minutes: Number(v) })} options={GROUP.some(g => g.value === String(settings.group_minutes)) ? GROUP : [...GROUP, { value: String(settings.group_minutes), label: `${settings.group_minutes} minutes` }]} ariaLabel="Count repeats for" /></div>
        </SettingRow>
      </MSection>
    </div>
  )
}
