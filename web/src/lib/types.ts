export interface User { id: number; email: string; name: string; role: 'admin' | 'member'; created_at: string }
export interface APIToken { id: number; user_id: number; name: string; prefix: string; last_used_at: string | null; created_at: string }

export type ConnMode = 'core' | 'direct' | 'off'
export type ConnVia = '' | 'core' | 'direct'

export interface Settings {
  core_configured: boolean
  core_registered?: boolean
  title: string; date_format: string; time_format: string; week_start: number

  // quiet hours: alerts below quiet_min_priority wait and come as one summary
  quiet_enabled: boolean; quiet_start: string; quiet_end: string
  quiet_days: number[]           // 0 = Sunday … 6; [] = every night
  quiet_min_priority: number     // this important or more still comes through
  quiet_summary: boolean

  group_minutes: number          // repeats within this many minutes count up instead of pinging
  send_min_priority: number      // below this, alerts stay in the inbox only
  keep_done_days: number; keep_days: number

  // Pocket Alerts' own notifications (a channel that keeps failing)
  notify_failing: boolean; notify_url: string; notify_format: string; notify_configured: boolean

  // Pocket Tasks: make a task from an alert
  tasks_mode: ConnMode; tasks_via: ConnVia; tasks_detail: string; tasks_open_url: string; tasks_configured: boolean
  tasks_url: string; tasks_token: string; tasks_public_url: string

  ai_provider: 'auto' | 'pocket' | 'openai'; ai_pocket_model: string
  openai_api_key: string; openai_model: string; openai_base_url: string

  setup_complete: boolean; base_url: string; timezone: string
  local_targets: boolean   // this person may send to addresses on the local network
}

// ---- alerts ----

/** What happened to an alert: sent to the phone, kept in the inbox only,
 *  waiting for the end of quiet hours, no channel took it, or every channel refused it. */
export type AlertState = 'sent' | 'quiet' | 'held' | 'none' | 'failed'

export interface Alert {
  id: number
  source_id: number | null; source_name: string
  ext_id: string
  title: string; body: string
  priority: number     // importance, ntfy's scale: 1 lowest … 5 urgent
  tags: string[]
  emoji: string        // the emoji the tags stand for
  labels: string[]     // the other tags
  click_url: string
  state: AlertState; reason: string
  rule_id: number | null
  count: number        // repeats count up on one alert
  received_at: string; last_seen_at: string
  read_at: string | null; done_at: string | null
  task_url: string
}

export interface Delivery {
  id: number; alert_id: number | null; channel_id: number | null; channel: string
  kind: 'alert' | 'summary' | 'test'
  status: 'sent' | 'failed' | 'retrying'
  error: string; attempts: number; next_at: string | null; at: string
}

export interface AlertDetail { alert: Alert; deliveries: Delivery[]; source?: Source; rule?: Rule }

export type InboxView = 'open' | 'unread' | 'important' | 'done' | 'all'

export interface Dashboard {
  counts: { open: number; unread: number; important: number; held: number; today: number }
  quiet: boolean; quiet_until: string; quiet_enabled: boolean
  sources: number; sources_failing: number
  channels: number; channels_failing: number
}

// ---- sources ----

export type SourceKind = 'push' | 'ntfy' | 'app'

export interface SourceAddresses {
  apprise: string       // POST {title, body, type}
  hook: string          // POST JSON or text
  ntfy_server: string; ntfy_topic: string; ntfy_url: string
  apprise_url: string   // ntfy(s)://host/key for Apprise's ntfy plugin
  curl: string
}

export interface Source {
  id: number; name: string; kind: SourceKind; topic: string; icon: string
  default_priority: number     // 0 = as sent
  enabled: boolean; muted: boolean; snoozed_until: string | null
  follow_url: string; follow_topics: string; follow_token: string
  app_slug: string
  open: boolean          // also takes alerts at its plain topic name (admins set it)
  feed_topic: string     // its alerts can be read on this ntfy-style topic
  status: '' | 'ok' | 'error'; status_note: string
  last_alert_at: string | null; created_at: string
  addresses?: SourceAddresses
  week: { alerts: number; repeats: number; open: number }
}

/** A Pocket app whose alerts can come in through Pocket Core. */
export interface EventApp {
  slug: string; name: string; icon: string; about: string
  on: boolean; source_id: number | null
  link_on: boolean; link_detail: string
}

export interface AppsInfo { apps: EventApp[]; core: boolean; stream: boolean; stream_error: string }

// ---- channels ----

export type ChannelKind = 'pushover' | 'ntfy' | 'apprise' | 'webhook'

export interface Channel {
  id: number; name: string; kind: ChannelKind
  config: Record<string, string>   // secrets come back masked (••••)
  min_priority: number; enabled: boolean; position: number
  last_status: '' | 'ok' | 'error'; last_error: string; last_sent_at: string | null
  created_at: string; updated_at: string
}

// ---- rules ----

export interface RuleMatch {
  source_ids: number[]
  min_priority: number; max_priority: number   // 0 = any
  words: string   // comma separated, any of them
  tags: string    // comma separated, any of them
}

export interface Rule {
  id: number; name: string; enabled: boolean; position: number
  match: RuleMatch
  action: 'send' | 'quiet' | 'drop'
  priority: number          // 0 = keep
  channel_ids: number[]     // [] = the usual channels
  break_quiet: boolean
  hits: number; last_hit_at: string | null
  created_at: string; updated_at: string
}

export interface RulePreview { matched: number; of: number; examples: Alert[] }

// ---- activity ----

export interface DayCount { day: string; alerts: number; sent: number; quiet: number; repeats: number }

export interface History {
  days: DayCount[]
  sources: { id: number; name: string; alerts: number; repeats: number }[]
  deliveries: Delivery[]
  from: string; to: string
}

export interface LiveEvent { type: 'alert' | 'refresh' | 'source'; id?: number; new?: boolean }

// ---- words people see ----

export const PRIORITIES: { value: number; label: string; hint: string }[] = [
  { value: 5, label: 'Urgent', hint: 'wakes you up' },
  { value: 4, label: 'High', hint: 'worth a look soon' },
  { value: 3, label: 'Normal', hint: 'the usual' },
  { value: 2, label: 'Low', hint: 'no sound' },
  { value: 1, label: 'Lowest', hint: 'just for the record' },
]

export const priorityLabel = (p: number) => PRIORITIES.find(x => x.value === p)?.label || 'Normal'

export const STATE_LABEL: Record<AlertState, string> = {
  sent: 'Sent to you', quiet: 'Kept quiet', held: 'Waiting for the morning', none: 'Not sent', failed: 'Could not be sent',
}
