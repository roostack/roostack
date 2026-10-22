package store

import (
	"context"
	"time"
)

// MonitorResult is one check's result (SPEC.md §6.15).
type MonitorResult struct {
	Key string
	At  time.Time
	Up  bool
	// Paused: not checked (its service stopped on purpose, say); Err says
	// why.
	Paused bool
	// Status is the HTTP status (web checks), Millis how long the answer
	// took.
	Status, Millis int
	// Err is what went wrong, in words for people.
	Err string
	// CertIssued and CertExpires are the certificate's dates (web checks
	// over HTTPS).
	CertIssued, CertExpires time.Time
}

// MonitorHour sums one check's results in one hour.
type MonitorHour struct {
	Key  string
	Hour time.Time
	// Checks counts every result, Ups those up, Paused those paused.
	Checks, Ups, Paused int
	// MillisSum adds up the answer times of the results up; MillisMax is
	// the slowest.
	MillisSum, MillisMax int
}

// MonitorOutage is a time a check was down; Ended is zero while it lasts.
type MonitorOutage struct {
	Key            string
	Started, Ended time.Time
	Err            string
}

// MonitorCheck is a check someone added (SPEC.md §6.15, "Your own
// checks"): a web address, a host and port, or a host to ping.
type MonitorCheck struct {
	ID      int64
	Name    string
	Address string
	// SkipVerify doesn't check a web address's certificate.
	SkipVerify bool
	// Alert: tell people when it stops answering.
	Alert   bool
	Created time.Time
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func timeOrZero(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// AddMonitorResult keeps a result and adds it to its hour's summary.
func (s *Store) AddMonitorResult(ctx context.Context, r MonitorResult) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO monitor_results (key, at, up, paused, status, millis, error, cert_issued, cert_expires)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.Key, r.At.Unix(), boolInt(r.Up), boolInt(r.Paused), r.Status, r.Millis, r.Err,
		unixOrZero(r.CertIssued), unixOrZero(r.CertExpires)); err != nil {
		return err
	}
	ms := 0
	if r.Up {
		ms = r.Millis
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO monitor_hours (key, hour, checks, ups, paused, millis_sum, millis_max)
		VALUES (?, ?, 1, ?, ?, ?, ?)
		ON CONFLICT (key, hour) DO UPDATE SET checks = checks + 1, ups = ups + excluded.ups, paused = paused + excluded.paused,
			millis_sum = millis_sum + excluded.millis_sum, millis_max = max(millis_max, excluded.millis_max)`,
		r.Key, r.At.Truncate(time.Hour).Unix(), boolInt(r.Up), boolInt(r.Paused), ms, ms); err != nil {
		return err
	}
	return tx.Commit()
}

const monitorResultCols = `key, at, up, paused, status, millis, error, cert_issued, cert_expires`

func scanMonitorResult(row scanner) (MonitorResult, error) {
	var r MonitorResult
	var at, issued, expires int64
	var up, paused int
	if err := row.Scan(&r.Key, &at, &up, &paused, &r.Status, &r.Millis, &r.Err, &issued, &expires); err != nil {
		return r, err
	}
	r.At, r.Up, r.Paused = time.Unix(at, 0), up == 1, paused == 1
	r.CertIssued, r.CertExpires = timeOrZero(issued), timeOrZero(expires)
	return r, nil
}

// MonitorResults are a check's results since a time, oldest first.
func (s *Store) MonitorResults(ctx context.Context, key string, since time.Time) ([]MonitorResult, error) {
	return queryAll(ctx, s.db, scanMonitorResult, `SELECT `+monitorResultCols+` FROM monitor_results
		WHERE key = ? AND at >= ? ORDER BY at, rowid`, key, since.Unix())
}

// LastMonitorResults are a check's n latest results, newest first.
func (s *Store) LastMonitorResults(ctx context.Context, key string, n int) ([]MonitorResult, error) {
	return queryAll(ctx, s.db, scanMonitorResult, `SELECT `+monitorResultCols+` FROM monitor_results
		WHERE key = ? ORDER BY at DESC, rowid DESC LIMIT ?`, key, n)
}

// MonitorHours are a check's hourly summaries since a time, oldest first.
func (s *Store) MonitorHours(ctx context.Context, key string, since time.Time) ([]MonitorHour, error) {
	return queryAll(ctx, s.db, scanMonitorHour, `SELECT key, hour, checks, ups, paused, millis_sum, millis_max FROM monitor_hours
		WHERE key = ? AND hour >= ? ORDER BY hour`, key, since.Truncate(time.Hour).Unix())
}

func scanMonitorHour(row scanner) (MonitorHour, error) {
	var h MonitorHour
	var hour int64
	if err := row.Scan(&h.Key, &hour, &h.Checks, &h.Ups, &h.Paused, &h.MillisSum, &h.MillisMax); err != nil {
		return h, err
	}
	h.Hour = time.Unix(hour, 0)
	return h, nil
}

// StartMonitorOutage records that a check went down.
func (s *Store) StartMonitorOutage(ctx context.Context, key string, started time.Time, why string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO monitor_outages (key, started, error) VALUES (?, ?, ?)`, key, started.Unix(), why)
	return err
}

// EndMonitorOutage records that a check is up again.
func (s *Store) EndMonitorOutage(ctx context.Context, key string, ended time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE monitor_outages SET ended = ? WHERE key = ? AND ended = 0`, ended.Unix(), key)
	return err
}

// MonitorOutages are a check's outages that started since a time (or are
// still going), newest first.
func (s *Store) MonitorOutages(ctx context.Context, key string, since time.Time) ([]MonitorOutage, error) {
	return queryAll(ctx, s.db, scanMonitorOutage, `SELECT key, started, ended, error FROM monitor_outages
		WHERE key = ? AND (started >= ? OR ended = 0) ORDER BY started DESC, rowid DESC`, key, unixOrZero(since))
}

func scanMonitorOutage(row scanner) (MonitorOutage, error) {
	var o MonitorOutage
	var started, ended int64
	if err := row.Scan(&o.Key, &started, &ended, &o.Err); err != nil {
		return o, err
	}
	o.Started, o.Ended = time.Unix(started, 0), timeOrZero(ended)
	return o, nil
}

// PruneMonitor deletes results from before one time, and summaries and
// finished outages from before another.
func (s *Store) PruneMonitor(ctx context.Context, resultsBefore, hoursBefore time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM monitor_results WHERE at < ?`, resultsBefore.Unix()); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM monitor_hours WHERE hour < ?`, hoursBefore.Unix()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM monitor_outages WHERE ended != 0 AND ended < ?`, hoursBefore.Unix())
	return err
}

// ForgetMonitorCheck deletes everything kept about checks that no longer
// exist (a service removed, an own check deleted).
func (s *Store) ForgetMonitorCheck(ctx context.Context, key string) error {
	for _, table := range []string{"monitor_results", "monitor_hours", "monitor_outages"} {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE key = ?`, key); err != nil {
			return err
		}
	}
	return nil
}

// MonitorChecks lists the checks people added, oldest first.
func (s *Store) MonitorChecks(ctx context.Context) ([]MonitorCheck, error) {
	return queryAll(ctx, s.db, scanMonitorCheck, `SELECT id, name, address, skip_verify, alert, created_at FROM monitor_checks ORDER BY id`)
}

func scanMonitorCheck(row scanner) (MonitorCheck, error) {
	var c MonitorCheck
	var skip, alert int
	var created int64
	if err := row.Scan(&c.ID, &c.Name, &c.Address, &skip, &alert, &created); err != nil {
		return c, err
	}
	c.SkipVerify, c.Alert, c.Created = skip == 1, alert == 1, time.Unix(created, 0)
	return c, nil
}

// AddMonitorCheck adds someone's check and returns its ID.
func (s *Store) AddMonitorCheck(ctx context.Context, c MonitorCheck) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO monitor_checks (name, address, skip_verify, alert, created_at) VALUES (?, ?, ?, ?, ?)`,
		c.Name, c.Address, boolInt(c.SkipVerify), boolInt(c.Alert), time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RemoveMonitorCheck deletes someone's check (ErrNotFound if there's no
// such check).
func (s *Store) RemoveMonitorCheck(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM monitor_checks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
