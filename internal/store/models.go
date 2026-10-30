package store

import "strings"

// User is an account on this box.
type User struct {
	ID           int64  `json:"id"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"` // admin | member
	CreatedAt    string `json:"created_at"`
}

// APIToken is a personal access token (hash stored, plaintext shown once).
type APIToken struct {
	ID         int64   `json:"id"`
	UserID     int64   `json:"user_id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	LastUsedAt *string `json:"last_used_at"`
	CreatedAt  string  `json:"created_at"`
}

// Source is where alerts come from.
type Source struct {
	ID              int64   `json:"id"`
	UserID          int64   `json:"-"`
	Name            string  `json:"name"`
	Kind            string  `json:"kind"` // push | ntfy | app
	Topic           string  `json:"topic"`
	PushKey         string  `json:"push_key"`
	Icon            string  `json:"icon"`
	DefaultPriority int     `json:"default_priority"`
	Enabled         bool    `json:"enabled"`
	Muted           bool    `json:"muted"`
	SnoozedUntil    *string `json:"snoozed_until"`
	FollowURL       string  `json:"follow_url"`
	FollowTopics    string  `json:"follow_topics"`
	FollowToken     string  `json:"follow_token"`
	FollowCursor    string  `json:"-"`
	AppSlug         string  `json:"app_slug"`
	Open            bool    `json:"open"`       // also takes alerts at its plain topic name (no key)
	FeedTopic       string  `json:"feed_topic"` // its alerts can be read on this ntfy-style topic
	Status          string  `json:"status"`
	StatusNote      string  `json:"status_note"`
	LastAlertAt     *string `json:"last_alert_at"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

// Snoozed reports whether the source is snoozed right now.
func (s Source) Snoozed(now string) bool { return s.SnoozedUntil != nil && *s.SnoozedUntil > now }

// Alert is one alert in a person's inbox.
type Alert struct {
	ID          int64    `json:"id"`
	UserID      int64    `json:"-"`
	SourceID    *int64   `json:"source_id"`
	SourceName  string   `json:"source_name"`
	ExtID       string   `json:"ext_id"`
	Fingerprint string   `json:"-"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Priority    int      `json:"priority"`
	Tags        []string `json:"tags"`
	ClickURL    string   `json:"click_url"`
	State       string   `json:"state"` // sent | quiet | held | none
	Reason      string   `json:"reason"`
	RuleID      *int64   `json:"rule_id"`
	Count       int      `json:"count"`
	ReceivedAt  string   `json:"received_at"`
	LastSeenAt  string   `json:"last_seen_at"`
	ReadAt      *string  `json:"read_at"`
	DoneAt      *string  `json:"done_at"`
	TaskURL     string   `json:"task_url"`
}

// Channel is where alerts go.
type Channel struct {
	ID          int64             `json:"id"`
	UserID      int64             `json:"-"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind"` // pushover | ntfy | apprise | webhook
	Config      map[string]string `json:"config"`
	MinPriority int               `json:"min_priority"`
	Enabled     bool              `json:"enabled"`
	Position    int               `json:"position"`
	LastStatus  string            `json:"last_status"`
	LastError   string            `json:"last_error"`
	LastSentAt  *string           `json:"last_sent_at"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

// RuleMatch is what a rule looks at; every part that is set must match.
type RuleMatch struct {
	SourceIDs   []int64 `json:"source_ids"`   // any of these (empty = any source)
	MinPriority int     `json:"min_priority"` // 0 = any
	MaxPriority int     `json:"max_priority"` // 0 = any
	Words       string  `json:"words"`        // the title or text contains any of these (comma separated)
	Tags        string  `json:"tags"`         // has any of these tags (comma separated)
}

// Rule says what to do with matching alerts.
type Rule struct {
	ID         int64     `json:"id"`
	UserID     int64     `json:"-"`
	Name       string    `json:"name"`
	Enabled    bool      `json:"enabled"`
	Position   int       `json:"position"`
	Match      RuleMatch `json:"match"`
	Action     string    `json:"action"`   // send | quiet | drop
	Priority   int       `json:"priority"` // 0 = keep
	ChannelIDs []int64   `json:"channel_ids"`
	BreakQuiet bool      `json:"break_quiet"`
	Hits       int       `json:"hits"`
	LastHitAt  *string   `json:"last_hit_at"`
	CreatedAt  string    `json:"created_at"`
	UpdatedAt  string    `json:"updated_at"`
}

// Delivery is one try to send to a channel.
type Delivery struct {
	ID        int64   `json:"id"`
	UserID    int64   `json:"-"`
	AlertID   *int64  `json:"alert_id"`
	ChannelID *int64  `json:"channel_id"`
	Channel   string  `json:"channel"`
	Kind      string  `json:"kind"`   // alert | summary | test
	Status    string  `json:"status"` // sent | failed | retrying
	Error     string  `json:"error"`
	Attempts  int     `json:"attempts"`
	NextAt    *string `json:"next_at"`
	At        string  `json:"at"`
}

// Settings are the box-wide values plus one person's preferences and their
// own connections. Connection modes: "core" (an app link through Pocket
// Core), "direct" (URL + token) or "off"; "" = not chosen yet.
type Settings struct {
	Title         string `json:"title"`
	SetupComplete bool   `json:"setup_complete"`

	DateFormat string `json:"date_format"`
	TimeFormat string `json:"time_format"`
	WeekStart  int    `json:"week_start"` // 1 = Monday, 7 = Sunday

	// Quiet hours: alerts below QuietMinPriority wait until QuietEnd and
	// come as one summary. Times are HH:MM in the box's time zone.
	QuietEnabled     bool   `json:"quiet_enabled"`
	QuietStart       string `json:"quiet_start"`
	QuietEnd         string `json:"quiet_end"`
	QuietDays        []int  `json:"quiet_days"`         // 0 = Sunday … 6; empty = every night
	QuietMinPriority int    `json:"quiet_min_priority"` // this important or more still comes through
	QuietSummary     bool   `json:"quiet_summary"`      // one summary when quiet hours end

	// Repeats of the same alert within this many minutes count up instead of
	// pinging again.
	GroupMinutes int `json:"group_minutes"`
	// Alerts below this importance stay in the inbox only (1 = all go out).
	SendMinPriority int `json:"send_min_priority"`
	// Done alerts are deleted after this many days, all alerts after KeepDays (0 = keep).
	KeepDoneDays int `json:"keep_done_days"`
	KeepDays     int `json:"keep_days"`

	// Notifications about Pocket Alerts itself (a channel that keeps failing).
	NotifyFailing bool   `json:"notify_failing"`
	NotifyURL     string `json:"notify_url"`
	NotifyFormat  string `json:"notify_format"` // apprise | ntfy | json

	// Pocket Tasks: "Make a task" from an alert.
	TasksMode      string `json:"tasks_mode"`
	TasksURL       string `json:"tasks_url"`
	TasksToken     string `json:"tasks_token"`
	TasksPublicURL string `json:"tasks_public_url"`

	// AI for explaining an alert and answering questions.
	AIProvider    string `json:"ai_provider"` // auto | pocket | openai
	AIPocketModel string `json:"ai_pocket_model"`
	OpenAIKey     string `json:"openai_api_key"`
	OpenAIModel   string `json:"openai_model"`
	OpenAIBaseURL string `json:"openai_base_url"`
}

// SplitTags turns "a, b ,c" into clean lowercase tags.
func SplitTags(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range strings.Split(s, ",") {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// JoinTags is the stored form of tags.
func JoinTags(s string) string { return strings.Join(SplitTags(s), ", ") }
