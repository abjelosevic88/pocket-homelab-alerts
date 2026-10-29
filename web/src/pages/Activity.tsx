// Activity: how many alerts came in day by day, what went to your phone, the busiest sources and the latest sends.
import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { ago, fmtDate, fmtDateTime, plural } from '../lib/format'
import type { DayCount, History } from '../lib/types'
import { Icon } from '../components/icons'
import { Card, Empty, Loading, PageHeader, Segmented, Spinner, useAsync } from '../components/ui'
import { useLive } from '../components/Layout'

const KIND: Record<string, string> = { alert: 'Alert', summary: 'Quiet-hours summary', test: 'Test' }

/** Every day of the range, zeros included. */
function fillDays(from: string, to: string, days: DayCount[]): DayCount[] {
  const by = new Map(days.map(d => [d.day, d]))
  const out: DayCount[] = []
  const d = new Date(from + 'T12:00:00')
  const end = new Date(to + 'T12:00:00')
  while (d <= end) {
    const k = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
    out.push(by.get(k) || { day: k, alerts: 0, sent: 0, quiet: 0, repeats: 0 })
    d.setDate(d.getDate() + 1)
  }
  return out
}

export default function Activity() {
  const [days, setDays] = useState<30 | 90>(30)
  const { data, loading, error, reload } = useAsync(() => api.get<History>(`${V1}/history?days=${days}`), [days])
  useLive(e => { if (e.type !== 'source') reload() })

  const series = useMemo(() => (data ? fillDays(data.from, data.to, data.days) : []), [data])
  const tot = useMemo(() => series.reduce((t, d) => ({ alerts: t.alerts + d.alerts, sent: t.sent + d.sent, quiet: t.quiet + d.quiet, repeats: t.repeats + d.repeats }), { alerts: 0, sent: 0, quiet: 0, repeats: 0 }), [series])
  const max = Math.max(1, ...series.map(d => d.alerts))
  const [hover, setHover] = useState<DayCount | null>(null)
  const topMax = Math.max(1, ...(data?.sources || []).map(s => s.alerts + s.repeats))

  return (
    <div className="act">
      <PageHeader title="Activity" sub="How many alerts came in, how many reached your phone, and where they came from."
        actions={<><span className="ib-load">{loading && data && <Spinner />}</span><Segmented size="sm" value={days} onChange={v => setDays(v)} options={[{ value: 30, label: '30 days' }, { value: 90, label: '90 days' }]} label="Range" /></>} />
      {error && <div className="callout danger">{error}</div>}
      {!data ? <Loading /> : (
        <div className="stack loose">
          <div className="act-tiles">
            <Tile label="Alerts" value={tot.alerts} />
            <Tile label="Sent to you" value={tot.sent} />
            <Tile label="Kept quiet" value={tot.quiet} hint="in the inbox only: low importance, muted, snoozed, quiet hours or a rule" />
            <Tile label="Pings saved" value={tot.repeats} hint="repeats that counted up on an open alert instead of pinging again" />
          </div>

          <Card title="Day by day" sub={hover ? `${fmtDate(hover.day)}: ${plural(hover.alerts, 'alert')}, ${hover.sent} sent, ${hover.quiet} kept quiet${hover.repeats ? `, ${plural(hover.repeats, 'repeat')}` : ''}` : `${fmtDate(data.from)} – ${fmtDate(data.to)}`}>
            {tot.alerts === 0 ? <p className="muted small">No alerts in these days.</p> : (
              <>
                <div className={`act-chart ${days === 90 ? 'dense' : ''}`} onMouseLeave={() => setHover(null)}>
                  {series.map(d => (
                    <div key={d.day} className={`act-col ${hover?.day === d.day ? 'hi' : ''}`} onMouseEnter={() => setHover(d)} onClick={() => setHover(d)} title={`${fmtDate(d.day)}: ${d.alerts}`}>
                      <div className="act-stack" style={{ height: `${(d.alerts / max) * 100}%` }}>
                        {d.quiet > 0 && <span className="act-quiet" style={{ flexGrow: d.quiet }} />}
                        {d.sent > 0 && <span className="act-sent" style={{ flexGrow: d.sent }} />}
                      </div>
                    </div>
                  ))}
                </div>
                <div className="act-axis small muted"><span>{fmtDate(data.from, false)}</span><span>{fmtDate(data.to, false)}</span></div>
                <div className="act-legend small muted"><span><i className="act-sent" />Sent to you</span><span><i className="act-quiet" />Kept quiet</span><span className="spacer" /><span className="tnum">most in a day: {max}</span></div>
              </>
            )}
          </Card>

          <div className="act-two">
            <Card title="Busiest sources" sub="Alerts and repeats in these days.">
              {data.sources.length === 0 ? <p className="muted small">Nothing came in.</p> : (
                <ul className="act-src">
                  {data.sources.slice(0, 10).map(s => (
                    <li key={s.id}>
                      <Link to={`/sources/${s.id}`} className="act-src-name ellipsis">{s.name}</Link>
                      <span className="act-src-bar"><span style={{ width: `${((s.alerts + s.repeats) / topMax) * 100}%` }} /></span>
                      <span className="small muted tnum act-src-n">{s.alerts}{s.repeats > 0 && <span className="faint"> +{s.repeats}</span>}</span>
                    </li>
                  ))}
                </ul>
              )}
            </Card>

            <Card title="Recent sends" sub="The latest tries to reach you, newest first.">
              {data.deliveries.length === 0 ? <Empty title="Nothing sent yet" icon="smartphone">Sends show up here once a place under <Link to="/send-to">Send to</Link> gets an alert.</Empty> : (
                <ul className="act-sends">
                  {data.deliveries.slice(0, 20).map(x => (
                    <li key={x.id}>
                      <Icon name={x.status === 'sent' ? 'check' : x.status === 'retrying' ? 'refresh' : 'x'} size={14} className={x.status === 'sent' ? 'ok' : x.status === 'failed' ? 'bad' : 'muted'} />
                      <span className="grow">
                        <span className="act-send-line">
                          {x.alert_id ? <Link to={`/alerts/${x.alert_id}`}>{x.channel || 'A removed channel'}</Link> : <b>{x.channel || 'A removed channel'}</b>}
                          <span className="muted small"> · {KIND[x.kind] || x.kind}{x.status !== 'sent' && ` · ${x.status === 'retrying' ? 'trying again' : 'failed'}`}</span>
                        </span>
                        {x.error && <span className="ib-err small">{x.error}</span>}
                      </span>
                      <span className="small muted tnum" title={fmtDateTime(x.at)}>{ago(x.at)}</span>
                    </li>
                  ))}
                </ul>
              )}
            </Card>
          </div>
        </div>
      )}
    </div>
  )
}

function Tile({ label, value, hint }: { label: string; value: number; hint?: string }) {
  return (
    <div className="act-tile" title={hint}>
      <div className="stat-l">{label}</div>
      <div className="act-tile-v tnum">{value.toLocaleString()}</div>
    </div>
  )
}
