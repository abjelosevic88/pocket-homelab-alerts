package server

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/store"
)

// ---- demo data (DEMO_DATA=true) ----
//
// Ten weeks of what a home server sends: nightly backups, finished
// downloads, a service going down and up, the arr apps' health, disk space
// and certificates — through real rules and quiet hours. In demo mode
// nothing is really sent: every channel "works".

type demoSource struct {
	key      string
	src      store.Source
	priority int
}

func (s *Server) seedDemo(ctx context.Context, uid int64) error {
	if have, _ := s.store.ListSources(ctx, uid); len(have) > 0 {
		return nil
	}
	rnd := rand.New(rand.NewSource(42))
	st, _ := s.store.GetSettings(ctx, uid)
	st.QuietEnabled, st.QuietStart, st.QuietEnd = true, "22:30", "07:00"
	if err := s.store.SaveUserSettings(ctx, uid, st); err != nil {
		return err
	}

	srcs := map[string]*store.Source{}
	for _, d := range []demoSource{
		{"backups", store.Source{Name: "Backups", Topic: "backups", Icon: "archive"}, 0},
		{"downloads", store.Source{Name: "Downloads", Topic: "downloads", Icon: "download"}, 0},
		{"kuma", store.Source{Name: "Uptime Kuma", Topic: "kuma", Icon: "activity"}, 0},
		{"arr", store.Source{Name: "Sonarr & Radarr", Topic: "arr", Icon: "video"}, 4},
		{"certs", store.Source{Name: "Certificates", Topic: "certs", Icon: "lock"}, 0},
		{"monitoring", store.Source{Name: "Pocket Monitoring", Kind: "app", AppSlug: "monitoring", Icon: "activity", Status: "ok"}, 0},
		{"nas", store.Source{Name: "NAS (ntfy)", Kind: "ntfy", FollowURL: "http://truenas.local:8096", FollowTopics: "truenas", Icon: "server", Status: "ok"}, 0},
	} {
		x := d.src
		if x.Kind == "" {
			x.Kind = "push"
		}
		x.Enabled, x.DefaultPriority = true, d.priority
		if err := s.store.CreateSource(ctx, uid, &x); err != nil {
			return err
		}
		srcs[d.key] = &x
	}

	phone := &store.Channel{Name: "iPhone (Pushover)", Kind: "pushover", MinPriority: 1, Enabled: true, Config: map[string]string{"user": "uQiRzpo4DXghDmr9QzzfQu27cmVRsG", "token": "azGDORePK8gMaC0QOYAMyEEuzJnyUi"}}
	mac := &store.Channel{Name: "ntfy on the Mac", Kind: "ntfy", MinPriority: 4, Enabled: true, Config: map[string]string{"url": "https://ntfy.sh", "topic": "ana-home-alerts"}}
	mail := &store.Channel{Name: "Email (Apprise)", Kind: "apprise", MinPriority: 5, Enabled: false, Config: map[string]string{"url": "http://apprise:8000/notify/email"}}
	for _, c := range []*store.Channel{phone, mac, mail} {
		if err := s.store.CreateChannel(ctx, uid, c); err != nil {
			return err
		}
	}
	for _, r := range []*store.Rule{
		{Name: "Downloads: no sound", Enabled: true, Action: "send", Priority: 2, Match: store.RuleMatch{SourceIDs: []int64{srcs["downloads"].ID}}},
		{Name: "Failed backups wake me up", Enabled: true, Action: "send", BreakQuiet: true, Match: store.RuleMatch{SourceIDs: []int64{srcs["backups"].ID}, MinPriority: 4}},
		{Name: "Kuma's test pings", Enabled: true, Action: "drop", Match: store.RuleMatch{SourceIDs: []int64{srcs["kuma"].ID}, Words: "test notification"}},
		{Name: "Cert reminders only to the phone", Enabled: true, Action: "send", ChannelIDs: []int64{phone.ID}, Match: store.RuleMatch{SourceIDs: []int64{srcs["certs"].ID}}},
		{Name: "Low NAS chatter", Enabled: false, Action: "quiet", Match: store.RuleMatch{SourceIDs: []int64{srcs["nas"].ID}, MaxPriority: 2}},
	} {
		if r.ChannelIDs == nil {
			r.ChannelIDs = []int64{}
		}
		if err := s.store.CreateRule(ctx, uid, r); err != nil {
			return err
		}
	}

	shows := []string{"Severance S02E07", "The Bear S03E04", "Dune: Part Two (2024)", "Slow Horses S04E02", "Shogun S01E09", "Andor S02E03",
		"The Last of Us S02E05", "Past Lives (2023)", "Fallout S01E08", "Blue Eye Samurai S01E04", "Arcane S02E06", "Perfect Days (2023)"}
	services := []string{"Jellyfin", "Nextcloud", "Immich", "Home Assistant", "Vaultwarden"}
	type ev struct {
		at  time.Time
		src string
		in  incoming
	}
	var evs []ev
	add := func(t time.Time, src string, in incoming) { evs = append(evs, ev{t, src, in}) }
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, -70)
	for d := 0; d <= 70; d++ {
		day := start.AddDate(0, 0, d)
		at := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
		// nightly backups at 03:10; a few fail
		if rnd.Intn(23) == 0 {
			add(at(3, 10+rnd.Intn(15)), "backups", incoming{Title: "Backup failed: photos", Body: "restic: Fatal: unable to open repository at sftp:nas:/backup/photos: ssh: connect to host nas port 22: Connection timed out", Priority: 4, Tags: []string{"x"}})
		} else {
			add(at(3, 10+rnd.Intn(8)), "backups", incoming{Title: "Nightly backup finished", Body: fmt.Sprintf("8 sources, %d.%d GB added, took %d min.", rnd.Intn(4), rnd.Intn(10), 12+rnd.Intn(30)), Priority: 2, Tags: []string{"white_check_mark"}})
		}
		// downloads through the day
		for i := 0; i < rnd.Intn(5); i++ {
			add(at(9+rnd.Intn(14), rnd.Intn(60)), "downloads", incoming{Title: "Job finished", Body: shows[rnd.Intn(len(shows))], Priority: 3, Tags: []string{"inbox_tray"}})
		}
		// a service goes down now and then
		if rnd.Intn(6) == 0 {
			svc := services[rnd.Intn(len(services))]
			t := at(rnd.Intn(24), rnd.Intn(60))
			add(t, "kuma", incoming{Title: "[🔴 Down] " + svc, Body: svc + " is not answering: connect ECONNREFUSED 10.220.28.1:8100", Priority: 4, Tags: []string{"red_circle"}})
			add(t.Add(time.Duration(2+rnd.Intn(30))*time.Minute), "kuma", incoming{Title: "[✅ Up] " + svc, Body: svc + " is back (" + fmt.Sprint(40+rnd.Intn(200)) + " ms).", Priority: 3, Tags: []string{"green_circle"}})
		}
		// the arr apps' health, repeating for a while
		if rnd.Intn(9) == 0 {
			t := at(10+rnd.Intn(10), rnd.Intn(60))
			for i := 0; i < 1+rnd.Intn(5); i++ {
				add(t.Add(time.Duration(i*6)*time.Minute), "arr", incoming{Title: "Health issue", Body: "Indexers unavailable due to failures for more than 6 hours: Nyaa, 1337x", Tags: []string{"warning"}})
			}
		}
		// disk space from Pocket Monitoring
		if rnd.Intn(12) == 0 {
			pct := 90 + rnd.Intn(6)
			t := at(rnd.Intn(24), rnd.Intn(60))
			add(t, "monitoring", incoming{Title: "Server alert: Home server", Body: fmt.Sprintf("Disk /mnt/storage is %d%% full (threshold 90%%)", pct), Priority: 4, Tags: []string{"rotating_light"}})
			add(t.Add(10*time.Minute), "monitoring", incoming{Title: "Server alert: Home server", Body: fmt.Sprintf("Disk /mnt/storage is %d%% full (threshold 90%%)", pct+1), Priority: 4, Tags: []string{"rotating_light"}})
		}
		// the NAS's own chatter
		if rnd.Intn(4) == 0 {
			add(at(rnd.Intn(24), rnd.Intn(60)), "nas", incoming{Title: "Scrub finished on pool tank", Body: "0 errors, 2.31 TiB scanned.", Priority: 2, Tags: []string{"floppy_disk"}})
		}
		if day.Weekday() == time.Monday {
			add(at(9, 0), "certs", incoming{Title: "Certificate expires soon", Body: fmt.Sprintf("home.example.net expires in %d days (Let's Encrypt renews at 30).", 14+rnd.Intn(20)), Priority: 3, Tags: []string{"lock"}})
		}
	}
	// a fresh morning: a few things to look at
	add(now.Add(-95*time.Minute), "arr", incoming{Title: "Health issue", Body: "Download client qBittorrent is unavailable due to failures", Tags: []string{"warning"}})
	add(now.Add(-40*time.Minute), "monitoring", incoming{Title: "Server alert: Home server", Body: "Container immich_machine_learning restarted 3 times in 10 minutes", Priority: 4, Tags: []string{"rotating_light"}})
	add(now.Add(-12*time.Minute), "downloads", incoming{Title: "Job finished", Body: "Severance S02E08", Priority: 3, Tags: []string{"inbox_tray"}})
	add(now.Add(-3*time.Minute), "certs", incoming{Title: "Certificate renewed", Body: "home.example.net is good until " + now.AddDate(0, 3, 0).Format("2 Jan 2006") + ".", Priority: 2, Tags: []string{"white_check_mark"}})

	for i := 1; i < len(evs); i++ {
		for j := i; j > 0 && evs[j].at.Before(evs[j-1].at); j-- {
			evs[j], evs[j-1] = evs[j-1], evs[j]
		}
	}
	for _, e := range evs {
		e.in.At = e.at
		if _, _, err := s.receive(ctx, uid, srcs[e.src], e.in); err != nil {
			return err
		}
	}
	// what a person would have done with them: old ones read and done, the
	// last day's still waiting; nights past were summed up in the morning
	recent := now.Add(-26 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := s.store.DB.ExecContext(ctx, `UPDATE alerts SET read_at = last_seen_at, done_at = CASE WHEN id % 7 = 0 THEN NULL ELSE last_seen_at END
		WHERE user_id = ? AND last_seen_at < ?`, uid, recent); err != nil {
		return err
	}
	if _, err := s.store.DB.ExecContext(ctx, `UPDATE alerts SET read_at = last_seen_at WHERE user_id = ? AND last_seen_at < ? AND id % 3 = 0`,
		uid, now.Add(-3*time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if on, _ := quietNow(st, now); !on {
		if _, err := s.store.DB.ExecContext(ctx, `UPDATE alerts SET state = 'sent', reason = 'In the summary at 07:00' WHERE user_id = ? AND state = 'held'`, uid); err != nil {
			return err
		}
	}
	s.log.Info("demo data seeded", "alerts", len(evs))
	return nil
}

// demoDeliver records sends as if they went out, at the alert's time.
func (s *Server) demoDeliver(ctx context.Context, uid int64, a *store.Alert, chans []store.Channel) {
	for i := range chans {
		c := &chans[i]
		d := &store.Delivery{AlertID: &a.ID, ChannelID: &c.ID, Channel: c.Name, Kind: "alert", Status: "sent", At: a.ReceivedAt}
		_ = s.store.AddDelivery(ctx, uid, d)
		_, _ = s.store.DB.ExecContext(ctx, `UPDATE channels SET last_status = 'ok', last_sent_at = ? WHERE id = ?`, a.ReceivedAt, c.ID)
	}
}
