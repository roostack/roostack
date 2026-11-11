package store

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"time"
)

// What a device's traffic reaches, besides the lab's services (SPEC.md
// §10.11).
const (
	ReachLab        = "lab"        // the lab's services only
	ReachHome       = "home"       // and the home network's other devices
	ReachEverything = "everything" // all its traffic goes through the lab
)

// VPNDevice is a person's device on the lab's VPN: its own address, and
// the public key of a key pair its person's browser made (Roostack never
// holds the private key).
type VPNDevice struct {
	ID     int64
	UserID int64
	// Username and Role are its person's.
	Username, Role string
	Name           string
	PublicKey      string
	Address        string
	Reach          string
	// Pages: it reaches Roostack's pages (an Owner's or Admin's); SSH:
	// it reaches SSH (an Owner's).
	Pages, SSH bool
	Created    time.Time
}

// ErrDeviceLimit means the person has as many devices as the Owner allows.
var ErrDeviceLimit = errors.New("that's as many devices as the lab allows each person")

// vpnServerAddress is the address of the server devices connect to, and
// devices get the ones after it (trust.DevicesRange).
var vpnServerAddress = netip.MustParseAddr("100.96.128.1")

const vpnDeviceColumns = `d.id, d.user_id, u.username, u.role, d.name, d.public_key, d.address, d.reach, d.pages, d.ssh, d.created_at`

func scanVPNDevice(row scanner) (VPNDevice, error) {
	var d VPNDevice
	var created int64
	err := row.Scan(&d.ID, &d.UserID, &d.Username, &d.Role, &d.Name, &d.PublicKey, &d.Address, &d.Reach, &d.Pages, &d.SSH, &created)
	d.Created = time.Unix(created, 0)
	return d, err
}

// VPNDevices lists every device, by person and name.
func (s *Store) VPNDevices(ctx context.Context) ([]VPNDevice, error) {
	return queryAll(ctx, s.db, scanVPNDevice, `SELECT `+vpnDeviceColumns+` FROM vpn_devices d JOIN users u ON u.id = d.user_id
		ORDER BY u.username, d.name, d.id`)
}

// UserVPNDevices lists one person's devices, by name.
func (s *Store) UserVPNDevices(ctx context.Context, userID int64) ([]VPNDevice, error) {
	return queryAll(ctx, s.db, scanVPNDevice, `SELECT `+vpnDeviceColumns+` FROM vpn_devices d JOIN users u ON u.id = d.user_id
		WHERE d.user_id = ? ORDER BY d.name, d.id`, userID)
}

// AddVPNDevice adds a device with the next free address, unless its
// person already has limit devices (0: no limit).
func (s *Store) AddVPNDevice(ctx context.Context, d VPNDevice, limit int) (VPNDevice, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM vpn_devices WHERE user_id = ?`, d.UserID).Scan(&n); err != nil {
		return d, err
	}
	if limit > 0 && n >= limit {
		return d, ErrDeviceLimit
	}
	used := map[netip.Addr]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT address FROM vpn_devices`)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			rows.Close()
			return d, err
		}
		if addr, err := netip.ParseAddr(a); err == nil {
			used[addr] = true
		}
	}
	rows.Close()
	addr := vpnServerAddress.Next()
	for used[addr] {
		addr = addr.Next()
	}
	if !netip.MustParsePrefix("100.96.128.0/17").Contains(addr) {
		return d, errors.New("the lab's VPN has no addresses left")
	}
	d.Address = addr.String()
	if d.Created.IsZero() {
		d.Created = time.Now()
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO vpn_devices (user_id, name, public_key, address, reach, pages, ssh, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, d.UserID, d.Name, d.PublicKey, d.Address, d.Reach, d.Pages, d.SSH, d.Created.Unix())
	if err != nil {
		return d, err
	}
	d.ID, _ = res.LastInsertId()
	return d, tx.Commit()
}

// VPNDevice is one device.
func (s *Store) VPNDevice(ctx context.Context, id int64) (VPNDevice, error) {
	d, err := scanVPNDevice(s.db.QueryRowContext(ctx, `SELECT `+vpnDeviceColumns+`
		FROM vpn_devices d JOIN users u ON u.id = d.user_id WHERE d.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// SetVPNDevice changes a device's name, key, what it reaches, Roostack's
// pages and SSH.
func (s *Store) SetVPNDevice(ctx context.Context, d VPNDevice) error {
	res, err := s.db.ExecContext(ctx, `UPDATE vpn_devices SET name = ?, public_key = ?, reach = ?, pages = ?, ssh = ? WHERE id = ?`,
		d.Name, d.PublicKey, d.Reach, d.Pages, d.SSH, d.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteVPNDevice removes a device.
func (s *Store) DeleteVPNDevice(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM vpn_devices WHERE id = ?`, id)
	return err
}
