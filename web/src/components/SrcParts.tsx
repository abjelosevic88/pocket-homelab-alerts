// Small pieces the Sources, Send to, Rules and Quiet hours pages share.
import React, { useEffect } from 'react'
import { useSearchParams } from 'react-router-dom'
import { PRIORITIES } from '../lib/types'
import { fmtTime } from '../lib/format'
import type { SelectOption } from './Select'
import { Icon } from './icons'

/** A titled block in the Settings look. */
export function MSection({ title, sub, icon, actions, children, flush }: { title: React.ReactNode; sub?: React.ReactNode; icon?: string; actions?: React.ReactNode; children?: React.ReactNode; flush?: boolean }) {
  return (
    <section className="set-section">
      <div className="set-section-head">
        {icon && <span className="set-app-ico"><Icon name={icon} size={17} /></span>}
        <div className="grow"><h3>{title}</h3>{sub && <p>{sub}</p>}</div>
        {actions && <div className="row">{actions}</div>}
      </div>
      {children && (flush ? <div>{children}</div> : <div className="set-section-body">{children}</div>)}
    </section>
  )
}

/** ?new=1 opens a page's editor (the Add menu); the parameter goes away when it closes. */
export function useNewParam(open: () => void): () => void {
  const [sp, setSp] = useSearchParams()
  const isNew = sp.get('new') === '1'
  useEffect(() => { if (isNew) open() }, [isNew]) // eslint-disable-line react-hooks/exhaustive-deps
  return () => { if (sp.has('new')) { sp.delete('new'); setSp(sp, { replace: true }) } }
}

/** Importance choices for the custom Select. */
export const prioOptions = (andUp = false): SelectOption[] =>
  PRIORITIES.map(p => ({ value: String(p.value), label: andUp ? `${p.label} and up` : p.label, hint: p.hint }))

/** "From Downloads and Kuma" style lists. */
export function joinWords(items: string[], last = 'and'): string {
  if (items.length <= 1) return items[0] || ''
  return `${items.slice(0, -1).join(', ')} ${last} ${items[items.length - 1]}`
}

/** HH:MM of a stored time. */
export function clockOf(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const sameDay = d.toDateString() === new Date().toDateString()
  const t = fmtTime(d)
  return sameDay ? t : `${d.toLocaleDateString([], { weekday: 'short' })} ${t}`
}
