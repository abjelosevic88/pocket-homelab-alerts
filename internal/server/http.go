package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// Router builds the chi router.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	if s.cfg.TrustProxy {
		r.Use(middleware.RealIP)
	}
	r.Use(middleware.RequestID)
	r.Use(s.logging)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))

	r.Get("/pocket-ask.js", s.core.AskWidget("alerts"))
	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleReady)
	r.Get("/api/health", s.handleHealth)
	if s.cfg.MetricsEnabled {
		r.Get("/metrics", s.handleMetrics)
	}

	// Where alerts come in (the address is the secret; see ingest.go).
	r.Post("/notify/{key}", s.handleAppriseNotify)
	r.Post("/hook/{key}", s.handleHook)
	r.Post("/", s.handleNtfyPublish)
	r.Post("/{topic}", s.handleNtfyPublish)
	r.Put("/{topic}", s.handleNtfyPublish)

	// Live updates stay open, so they live outside /api/v1 and its timeout.
	r.With(s.requireAuth).Get("/api/live", s.handleLive)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(middleware.Timeout(120 * time.Second))
		r.Post("/auth/login", s.handleLogin)
		r.Post("/auth/setup", s.handleSetup)
		r.Get("/auth/status", s.handleAuthStatus)
		r.Get("/auth/oidc/start", s.handleOIDCStart)
		r.Get("/auth/oidc/callback", s.handleOIDCCallback)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Post("/auth/logout", s.handleLogout)
			r.Get("/auth/me", s.handleMe)
			r.Put("/auth/me", s.handleUpdateMe)

			// the inbox (also for Pocket Hub and Day)
			r.Get("/dashboard", s.handleDashboard)
			r.Get("/history", s.handleHistory)
			r.Get("/day/{date}", s.handleDay)
			r.Get("/summary/{date}", s.handleDay)
			r.Get("/alerts", s.handleListAlerts)
			r.Post("/alerts", s.handleCreateAlert)
			r.Post("/alerts/mark", s.handleMarkAlerts)
			r.Post("/alerts/delete", s.handleDeleteAlerts)
			r.Get("/alerts/{id}", s.handleGetAlert)
			r.Delete("/alerts/{id}", s.handleDeleteAlert)
			r.Post("/alerts/{id}/quiet", s.handleMuteSimilar)
			r.Post("/alerts/{id}/task", s.handleAlertTask)
			r.Post("/alerts/{id}/explain", s.handleExplain)

			// where alerts come from
			r.Get("/sources", s.handleListSources)
			r.Post("/sources", s.handleCreateSource)
			r.Get("/sources/{id}", s.handleGetSource)
			r.Put("/sources/{id}", s.handleUpdateSource)
			r.Delete("/sources/{id}", s.handleDeleteSource)
			r.Post("/sources/{id}/key", s.handleSourceKey)
			r.Post("/sources/{id}/snooze", s.handleSnoozeSource)
			r.Post("/sources/{id}/test", s.handleTestSource)
			r.Get("/apps", s.handleListApps)
			r.Put("/apps/{slug}", s.handleSetApp)

			// where alerts go
			r.Get("/channels", s.handleListChannels)
			r.Post("/channels", s.handleCreateChannel)
			r.Post("/channels/reorder", s.handleReorderChannels)
			r.Post("/channels/test", s.handleTestChannel)
			r.Put("/channels/{id}", s.handleUpdateChannel)
			r.Delete("/channels/{id}", s.handleDeleteChannel)
			r.Post("/channels/{id}/test", s.handleTestChannel)
			r.Get("/channels/{id}/deliveries", s.handleChannelDeliveries)

			// rules
			r.Get("/rules", s.handleListRules)
			r.Post("/rules", s.handleCreateRule)
			r.Post("/rules/reorder", s.handleReorderRules)
			r.Post("/rules/preview", s.handlePreviewRule)
			r.Put("/rules/{id}", s.handleUpdateRule)
			r.Delete("/rules/{id}", s.handleDeleteRule)

			// AI
			r.Get("/ai/status", s.handleAIStatus)
			r.Get("/ai/models", s.handleAIModels)
			r.Post("/ai/ask", s.handleAsk)

			r.Post("/integrations/test", s.handleIntegrationTest)
			r.Post("/notify/test", s.handleNotifyTest)

			r.Get("/users", s.handleListUsers)
			r.Post("/users", s.handleCreateUser)
			r.Put("/users/{id}", s.handleUpdateUser)
			r.Delete("/users/{id}", s.handleDeleteUser)

			r.Get("/settings", s.handleGetSettings)
			r.Put("/settings", s.handlePutSettings)
			r.Get("/settings/system", s.handleSystemInfo)

			r.Get("/tokens", s.handleListTokens)
			r.Post("/tokens", s.handleCreateToken)
			r.Delete("/tokens/{id}", s.handleDeleteToken)

			r.Get("/backup", s.handleBackup)
			r.Get("/export.json", s.handleExportJSON)
			r.Get("/export.csv", s.handleExportCSV)
			r.Post("/import", s.handleImportJSON)
		})
	})

	r.NotFound(s.handleSPA)
	// GET /inbox matches the ntfy route /{topic} (POST and PUT only): the
	// page, not "method not allowed".
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			s.handleSPA(w, r)
			return
		}
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	})
	return r
}

// ---------- helpers ----------

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.requests.Add(1)
		if ww.Status() >= 500 {
			s.errors.Add(1)
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			return
		}
		s.log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(), "dur", time.Since(start).String(), "ip", r.RemoteAddr)
	})
}

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

func (s *Server) fail(w http.ResponseWriter, err error, context string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, store.ErrConflict), errors.Is(err, core.ErrOperationConflict):
		writeErr(w, http.StatusConflict, "already exists")
		return
	}
	s.log.Error(context, "err", err)
	writeErr(w, http.StatusInternalServerError, context+": "+err.Error())
}

func (s *Server) readBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(nil, r.Body, s.cfg.MaxBodyBytes))
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 32<<20))
	return dec.Decode(v)
}

// decodeKeys decodes JSON into v and also reports which top-level keys were
// present, so PUT handlers can tell "not sent" from "sent empty".
func decodeKeys(r *http.Request, v any) (map[string]bool, error) {
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	keys := make(map[string]bool, len(raw))
	for k := range raw {
		keys[k] = true
	}
	return keys, json.Unmarshal(b, v)
}

func idParam(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	return n
}

func chiParam(r *http.Request, name string) string { return chi.URLParam(r, name) }

func queryInt(r *http.Request, name string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(name))
	return n
}

func queryBool(r *http.Request, name string) bool {
	v := strings.ToLower(r.URL.Query().Get(name))
	return v == "1" || v == "true" || v == "yes"
}

func validDate(s string) bool {
	if s == "" {
		return true
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// dateRange reads ?from&to (defaults: the last n days up to today).
func dateRange(r *http.Request, days int) (string, string) {
	to := r.URL.Query().Get("to")
	from := r.URL.Query().Get("from")
	if to == "" || !validDate(to) {
		to = store.Today()
	}
	if from == "" || !validDate(from) {
		from = store.AddDays(to, -(days - 1))
	}
	if from > to {
		from, to = to, from
	}
	return from, to
}

// ---------- health / metrics ----------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"core_outbox": s.coreOutboxHealth(r.Context()), "status": "ok", "version": Version, "uptime": time.Since(s.started).Round(time.Second).String()})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DB.PingContext(r.Context()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	counts := s.store.Counts(ctx)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# HELP pocket_alerts_info Build info.\n# TYPE pocket_alerts_info gauge\npocket_alerts_info{version=%q} 1\n", Version)
	fmt.Fprintf(w, "# HELP pocket_alerts_uptime_seconds Process uptime.\n# TYPE pocket_alerts_uptime_seconds gauge\npocket_alerts_uptime_seconds %d\n", int(time.Since(s.started).Seconds()))
	fmt.Fprintf(w, "# HELP pocket_alerts_http_requests_total HTTP requests served.\n# TYPE pocket_alerts_http_requests_total counter\npocket_alerts_http_requests_total %d\n", s.requests.Load())
	fmt.Fprintf(w, "# HELP pocket_alerts_http_errors_total HTTP 5xx responses.\n# TYPE pocket_alerts_http_errors_total counter\npocket_alerts_http_errors_total %d\n", s.errors.Load())
	fmt.Fprintf(w, "# HELP pocket_alerts_received_total Alerts taken since start.\n# TYPE pocket_alerts_received_total counter\npocket_alerts_received_total %d\n", s.received.Load())
	fmt.Fprintf(w, "# HELP pocket_alerts_sends_total Sends to channels since start.\n# TYPE pocket_alerts_sends_total counter\npocket_alerts_sends_total %d\n", s.sent.Load())
	fmt.Fprintf(w, "# HELP pocket_alerts_rows Rows per table.\n# TYPE pocket_alerts_rows gauge\n")
	for k, v := range counts {
		fmt.Fprintf(w, "pocket_alerts_rows{table=%q} %d\n", k, v)
	}
}

// ---------- static / SPA ----------

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if s.webFS == nil {
		http.Error(w, "web UI not built (run `make web`)", http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if f, err := s.webFS.Open(path); err == nil {
		f.Close()
		if strings.HasPrefix(path, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.FileServerFS(s.webFS).ServeHTTP(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	r.URL.Path = "/"
	http.FileServerFS(s.webFS).ServeHTTP(w, r)
}
