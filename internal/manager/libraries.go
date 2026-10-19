package manager

import (
	"context"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/store"
)

// Media libraries (SPEC.md §8.1, "Media libraries"): the controller keeps
// each server's libraries and sends them with the server's assignment;
// the server mounts them through its agent.

// LibraryStore keeps the servers' media libraries.
type LibraryStore interface {
	MediaLibraries(ctx context.Context) ([]store.MediaLibrary, error)
	SetMediaLibrary(ctx context.Context, l store.MediaLibrary) error
	DeleteMediaLibrary(ctx context.Context, serverID, name string) error
}

// librariesFor is a server's media libraries, as its agent mounts them.
func (m *Manager) librariesFor(ctx context.Context, serverID string) []agent.Library {
	if m.Libraries == nil {
		return nil
	}
	libs, err := m.Libraries.MediaLibraries(ctx)
	if err != nil {
		m.Log.Error("reading media libraries", "err", err)
		return nil
	}
	var out []agent.Library
	for _, l := range libs {
		if l.ServerID == serverID {
			out = append(out, agentLibrary(l))
		}
	}
	return out
}

// agentLibrary is a stored library as the agent mounts it.
func agentLibrary(l store.MediaLibrary) agent.Library {
	a := agent.Library{Name: l.Name, Drive: l.Drive, Sub: l.Sub, Folder: l.Folder}
	if l.OwnerUID != 0 {
		a.AsOwner = &agent.Owner{UID: l.OwnerUID, GID: l.OwnerGID, Name: l.OwnerName}
	}
	return a
}
