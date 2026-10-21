package store

import (
	"context"
	"time"
)

// MediaLibrary is one of a server's media libraries (SPEC.md §8.1, "Media
// libraries"): a folder on a drive (Drive, its filesystem's UUID, and Sub,
// the folder on it; DriveName to show) or a folder on the server (Folder),
// read as its owner when OwnerUID is set.
type MediaLibrary struct {
	ServerID  string
	Name      string // its folder's name, in services too
	Title     string // what pages call it
	Drive     string
	DriveName string
	Sub       string
	Folder    string
	OwnerUID  int
	OwnerGID  int
	OwnerName string
	// ShareType ("smb" or "nfs"), ShareServer, SharePath and ShareUser
	// make it a network share; its password isn't kept here.
	ShareType, ShareServer, SharePath, ShareUser string
	// PhotosFor is the Immich it's kept for, as a place people migrate
	// photos from (SPEC.md §8.5): no service reads it.
	PhotosFor string
	Created   time.Time
}

// MediaLibraries lists every server's media libraries, by server and name.
func (s *Store) MediaLibraries(ctx context.Context) ([]MediaLibrary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_id, name, title, drive, drive_name, sub, folder,
		owner_uid, owner_gid, owner_name, share_type, share_server, share_path, share_user, photos_for, created_at
		FROM media_libraries ORDER BY server_id, title, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MediaLibrary
	for rows.Next() {
		var l MediaLibrary
		var created int64
		if err := rows.Scan(&l.ServerID, &l.Name, &l.Title, &l.Drive, &l.DriveName, &l.Sub, &l.Folder,
			&l.OwnerUID, &l.OwnerGID, &l.OwnerName, &l.ShareType, &l.ShareServer, &l.SharePath, &l.ShareUser, &l.PhotosFor,
			&created); err != nil {
			return nil, err
		}
		l.Created = time.Unix(created, 0)
		out = append(out, l)
	}
	return out, rows.Err()
}

// SetMediaLibrary adds a media library, or changes one of its name on its
// server.
func (s *Store) SetMediaLibrary(ctx context.Context, l MediaLibrary) error {
	if l.Created.IsZero() {
		l.Created = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO media_libraries (server_id, name, title, drive, drive_name, sub, folder,
		owner_uid, owner_gid, owner_name, share_type, share_server, share_path, share_user, photos_for, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(server_id, name) DO UPDATE SET title = excluded.title, drive = excluded.drive,
		drive_name = excluded.drive_name, sub = excluded.sub, folder = excluded.folder, owner_uid = excluded.owner_uid,
		owner_gid = excluded.owner_gid, owner_name = excluded.owner_name, share_type = excluded.share_type,
		share_server = excluded.share_server, share_path = excluded.share_path, share_user = excluded.share_user,
		photos_for = excluded.photos_for`,
		l.ServerID, l.Name, l.Title, l.Drive, l.DriveName, l.Sub, l.Folder, l.OwnerUID, l.OwnerGID, l.OwnerName,
		l.ShareType, l.ShareServer, l.SharePath, l.ShareUser, l.PhotosFor, l.Created.Unix())
	return err
}

// DeleteMediaLibrary removes a media library (ErrNotFound if there's none).
func (s *Store) DeleteMediaLibrary(ctx context.Context, serverID, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM media_libraries WHERE server_id = ? AND name = ?`, serverID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
