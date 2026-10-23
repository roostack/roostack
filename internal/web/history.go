package web

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/sleep"
)

// History (SPEC.md §6.14): what Roostack did on each server (each finished
// log: applies, backups, updates, restores, ...) and what people did (the
// audit log), newest first, kept for a chosen number of days.

// historyKeepKey is how many days history is kept (default 90).
const historyKeepKey = "history.keep_days"

// historyKeeps are the choices, in days.
var historyKeeps = []int{30, 90, 365}

const historyPage = 50

// historyKinds filter the list: "" everything, "people" the audit log,
// else a log kind.
var historyKinds = []struct{ ID, Title string }{
	{"", "Everything"},
	{"people", "What people did"},
	{activity.Apply, "Applying changes"},
	{activity.Backup, "Backups"},
	{activity.OSUpdates, "Updates"},
	{activity.Restore, "Restores"},
	{activity.Import, "Bringing data in"},
}

// historyEntry is one line of the page.
type historyEntry struct {
	At                  time.Time
	Server, ServerID    string // empty for what people did
	Title, Detail       string
	Failed              bool
	EventID             int64 // its log, if it has one
	Kind, Who, Duration string
}

func (s *Server) historyKeepDays(ctx context.Context) int {
	v, _ := s.Store.Setting(ctx, historyKeepKey)
	if n, err := strconv.Atoi(v); err == nil && slices.Contains(historyKeeps, n) {
		return n
	}
	return 90
}

func (s *Server) historyPage(w http.ResponseWriter, r *http.Request) {
	data := s.pageData(r)
	kind := r.URL.Query().Get("kind")
	var before time.Time
	if v, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64); err == nil {
		before = time.Unix(v, 0)
	}
	nodes := s.serverNodes()
	entries, unreachable := s.historyEntries(r, nodes, kind, before)
	if unreachable != nil {
		data["Unreachable"] = unreachable
	}
	slices.SortStableFunc(entries, func(a, b historyEntry) int { return b.At.Compare(a.At) })
	if len(entries) > historyPage {
		entries = entries[:historyPage]
		data["Older"] = entries[len(entries)-1].At.Unix()
	}
	data["Entries"], data["Kind"], data["Kinds"] = entries, kind, historyKinds
	data["MultiServer"] = len(nodes) > 1
	data["KeepDays"], data["Keeps"] = s.historyKeepDays(r.Context()), historyKeeps
	data["Deleted"] = r.URL.Query().Get("deleted")
	s.render(w, "settings-history", withArea(data, "history"))
}

// historyEntries is what people did and each server's finished work
// before a time (or the latest, for the zero time), newest first, at
// most historyPage of each; and the servers that couldn't be asked.
func (s *Server) historyEntries(r *http.Request, nodes []serverNode, kind string, before time.Time) ([]historyEntry, []string) {
	var entries []historyEntry
	var unreachable []string
	if kind == "" || kind == "people" {
		if audit, err := s.Store.AuditBefore(r.Context(), before, historyPage); err == nil {
			for _, a := range audit {
				entries = append(entries, historyEntry{At: a.At, Who: a.Username, Title: a.Action, Detail: a.Detail, Kind: "people"})
			}
		}
	}
	if kind != "people" {
		for _, mn := range nodes {
			h := mn.node.History()
			if h == nil {
				continue
			}
			events, err := h.HistoryPage(r.Context(), before, kind, historyPage)
			if err != nil {
				unreachable = append(unreachable, mn.view.Label())
				continue
			}
			for _, e := range events {
				he := historyEntry{At: e.Finished, Server: mn.view.Label(), ServerID: mn.view.ID, Title: e.Title,
					Detail: e.Status, Failed: e.Error != "", EventID: e.ID, Kind: e.Kind}
				if e.Error != "" {
					he.Detail = e.Error
				}
				if d := e.Finished.Sub(e.Started).Round(time.Second); d >= time.Second {
					he.Duration = d.String()
				}
				entries = append(entries, he)
			}
		}
	}
	slices.SortStableFunc(entries, func(a, b historyEntry) int { return b.At.Compare(a.At) })
	return entries, unreachable
}

// historyEventPage shows one event's log.
func (s *Server) historyEventPage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	for _, mn := range s.serverNodes() {
		if mn.view.ID != r.PathValue("server") {
			continue
		}
		h := mn.node.History()
		if h == nil {
			break
		}
		e, err := h.HistoryEvent(r.Context(), id)
		if err != nil {
			break
		}
		data := s.pageData(r)
		data["Event"], data["Server"], data["Lines"] = e, mn.view.Label(), strings.Split(e.Output, "\n")
		s.render(w, "settings-history-event", withArea(data, "history"))
		return
	}
	http.NotFound(w, r)
}

// historyKeepSubmit sets how long history is kept, and deletes what's
// older now.
func (s *Server) historyKeepSubmit(w http.ResponseWriter, r *http.Request) {
	days, err := strconv.Atoi(r.FormValue("days"))
	if err != nil || !slices.Contains(historyKeeps, days) {
		http.Error(w, "Choose how long to keep history.", http.StatusBadRequest)
		return
	}
	s.Store.SetSetting(r.Context(), historyKeepKey, strconv.Itoa(days))
	s.Store.Audit(r.Context(), currentUser(r), "history", "kept for "+strconv.Itoa(days)+" days")
	s.cullHistory(r.Context(), time.Now().AddDate(0, 0, -days))
	redirect(w, r, "/settings/history")
}

// historyDeleteSubmit deletes all history, on every server; the deletion
// itself is the first new entry.
func (s *Server) historyDeleteSubmit(w http.ResponseWriter, r *http.Request) {
	n := s.cullHistory(r.Context(), time.Now().Add(time.Minute))
	s.Store.Audit(r.Context(), currentUser(r), "history", "deleted all history ("+strconv.FormatInt(n, 10)+" entries)")
	redirect(w, r, "/settings/history?deleted="+strconv.FormatInt(n, 10))
}

// cullHistory deletes history older than before on every server, and the
// audit log here; it returns how many entries went.
func (s *Server) cullHistory(ctx context.Context, before time.Time) int64 {
	var total int64
	if n, err := s.Store.CullAudit(ctx, before); err == nil {
		total += n
	} else {
		s.Log.Error("deleting old history", "err", err)
	}
	for _, mn := range s.serverNodes() {
		if h := mn.node.History(); h != nil {
			if n, err := h.CullHistory(ctx, before); err == nil {
				total += n
			} else {
				s.Log.Warn("deleting old history", "server", mn.view.Name, "err", err)
			}
		}
	}
	return total
}

// RunHistoryCull deletes history older than the chosen days, on every
// server, a minute after starting and then daily.
func (s *Server) RunHistoryCull(ctx context.Context) {
	wait := time.Minute
	for {
		if !sleep.For(ctx, wait) {
			return
		}
		if n := s.cullHistory(ctx, time.Now().AddDate(0, 0, -s.historyKeepDays(ctx))); n > 0 {
			s.Log.Info("deleted old history", "entries", n)
		}
		wait = 24 * time.Hour
	}
}
