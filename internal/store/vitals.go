package store

import (
	"context"
	"time"
)

// ServerReading is one minute's reading of how a server is doing (SPEC.md
// §6.15.1): processors busy (0–100), memory and swap used and in all,
// swap read back and network in and out (bytes a second), and the hottest
// temperature (°C, 0 for none).
type ServerReading struct {
	Server              string
	At                  time.Time
	CPU                 float64
	MemUsed, MemTotal   int64
	SwapUsed, SwapTotal int64
	SwapIn              int64
	NetIn, NetOut       int64
	Temp                float64
}

// ServerHour is an hour's readings in summary: averages and peaks.
type ServerHour struct {
	Server                   string
	Hour                     time.Time
	Readings                 int
	CPU, CPUMax              float64
	Mem, MemMax, MemTotal    int64
	Swap, SwapMax, SwapTotal int64
	NetIn, NetInMax          int64
	NetOut, NetOutMax        int64
	Temp, TempMax            float64
}

// ServiceReading is a service's share of its server in one reading: its
// memory (bytes) and processor time (100 is one processor busy).
type ServiceReading struct {
	Server, Service string
	At              time.Time
	Memory          int64
	CPU             float64
}

// AddServerReading keeps a reading, adds it to its hour's summary, and
// keeps its services' shares.
func (s *Store) AddServerReading(ctx context.Context, r ServerReading, services []ServiceReading) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO server_readings (server, at, cpu, mem_used, mem_total, swap_used, swap_total,
		swap_in, net_in, net_out, temp) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.Server, r.At.Unix(), r.CPU, r.MemUsed, r.MemTotal,
		r.SwapUsed, r.SwapTotal, r.SwapIn, r.NetIn, r.NetOut, r.Temp); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO server_hours (server, hour, readings, cpu_sum, cpu_max, mem_sum, mem_max, mem_total,
		swap_sum, swap_max, swap_total, net_in_sum, net_in_max, net_out_sum, net_out_max, temp_sum, temp_max)
		VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (server, hour) DO UPDATE SET readings = readings + 1,
			cpu_sum = cpu_sum + excluded.cpu_sum, cpu_max = max(cpu_max, excluded.cpu_max),
			mem_sum = mem_sum + excluded.mem_sum, mem_max = max(mem_max, excluded.mem_max), mem_total = excluded.mem_total,
			swap_sum = swap_sum + excluded.swap_sum, swap_max = max(swap_max, excluded.swap_max), swap_total = excluded.swap_total,
			net_in_sum = net_in_sum + excluded.net_in_sum, net_in_max = max(net_in_max, excluded.net_in_max),
			net_out_sum = net_out_sum + excluded.net_out_sum, net_out_max = max(net_out_max, excluded.net_out_max),
			temp_sum = temp_sum + excluded.temp_sum, temp_max = max(temp_max, excluded.temp_max)`,
		r.Server, r.At.Truncate(time.Hour).Unix(), r.CPU, r.CPU, r.MemUsed, r.MemUsed, r.MemTotal, r.SwapUsed, r.SwapUsed, r.SwapTotal,
		r.NetIn, r.NetIn, r.NetOut, r.NetOut, r.Temp, r.Temp); err != nil {
		return err
	}
	for _, u := range services {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO service_readings (server, service, at, memory, cpu) VALUES (?, ?, ?, ?, ?)`,
			r.Server, u.Service, r.At.Unix(), u.Memory, u.CPU); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ServerReadings are a server's readings since a time, oldest first.
func (s *Store) ServerReadings(ctx context.Context, server string, since time.Time) ([]ServerReading, error) {
	return queryAll(ctx, s.db, func(row scanner) (ServerReading, error) {
		var r ServerReading
		var at int64
		err := row.Scan(&r.Server, &at, &r.CPU, &r.MemUsed, &r.MemTotal, &r.SwapUsed, &r.SwapTotal, &r.SwapIn, &r.NetIn, &r.NetOut, &r.Temp)
		r.At = time.Unix(at, 0)
		return r, err
	}, `SELECT server, at, cpu, mem_used, mem_total, swap_used, swap_total, swap_in, net_in, net_out, temp FROM server_readings
		WHERE server = ? AND at >= ? ORDER BY at`, server, since.Unix())
}

// ServerHours are a server's hourly summaries since a time, oldest first.
func (s *Store) ServerHours(ctx context.Context, server string, since time.Time) ([]ServerHour, error) {
	return queryAll(ctx, s.db, func(row scanner) (ServerHour, error) {
		var h ServerHour
		var hour int64
		var cpu, temp float64
		var mem, swap, in, out int64
		err := row.Scan(&h.Server, &hour, &h.Readings, &cpu, &h.CPUMax, &mem, &h.MemMax, &h.MemTotal, &swap, &h.SwapMax, &h.SwapTotal,
			&in, &h.NetInMax, &out, &h.NetOutMax, &temp, &h.TempMax)
		h.Hour = time.Unix(hour, 0)
		if n := h.Readings; n > 0 {
			h.CPU, h.Temp = cpu/float64(n), temp/float64(n)
			h.Mem, h.Swap, h.NetIn, h.NetOut = mem/int64(n), swap/int64(n), in/int64(n), out/int64(n)
		}
		return h, err
	}, `SELECT server, hour, readings, cpu_sum, cpu_max, mem_sum, mem_max, mem_total, swap_sum, swap_max, swap_total,
		net_in_sum, net_in_max, net_out_sum, net_out_max, temp_sum, temp_max FROM server_hours
		WHERE server = ? AND hour >= ? ORDER BY hour`, server, since.Truncate(time.Hour).Unix())
}

// ServiceReadings are the services' shares of a server since a time,
// oldest first.
func (s *Store) ServiceReadings(ctx context.Context, server string, since time.Time) ([]ServiceReading, error) {
	return queryAll(ctx, s.db, func(row scanner) (ServiceReading, error) {
		var u ServiceReading
		var at int64
		err := row.Scan(&u.Server, &u.Service, &at, &u.Memory, &u.CPU)
		u.At = time.Unix(at, 0)
		return u, err
	}, `SELECT server, service, at, memory, cpu FROM service_readings WHERE server = ? AND at >= ? ORDER BY at, service`,
		server, since.Unix())
}

// PruneServerReadings deletes readings and services' shares from before
// one time, and summaries from before another.
func (s *Store) PruneServerReadings(ctx context.Context, readingsBefore, hoursBefore time.Time) error {
	for _, q := range []struct {
		sql string
		t   time.Time
	}{
		{`DELETE FROM server_readings WHERE at < ?`, readingsBefore},
		{`DELETE FROM service_readings WHERE at < ?`, readingsBefore},
		{`DELETE FROM server_hours WHERE hour < ?`, hoursBefore},
	} {
		if _, err := s.db.ExecContext(ctx, q.sql, q.t.Unix()); err != nil {
			return err
		}
	}
	return nil
}
