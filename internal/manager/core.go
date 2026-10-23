package manager

import (
	"log/slog"
	"sync"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/pkgspec"
)

// Three features barely touch the rest of the controller, so each is a
// type of its own: migrating photos into Immich (Photos, photos.go),
// storage for other labs (Guests, guests.go) and changing the recovery key
// (KeyChanges, keychange.go). Each reaches the rest through core, a small
// interface the Manager implements, and the Manager makes each once, when
// it's first asked for.

// core is what those features use of the Manager: the lab's settings and
// log, its servers and installed services, and applying a change.
type core interface {
	labConfig() config.Lab
	logger() *slog.Logger
	// node is a server by ID ("" for the controller), nil if none; nameOf
	// its name, or the ID if it's gone.
	node(id string) node.Node
	nameOf(id string) string
	installedService(name string) (*pkgspec.Package, *instance.Instance, error)
	// Apply hands every server its latest assignment.
	Apply()
}

func (m *Manager) labConfig() config.Lab { return m.Lab }
func (m *Manager) logger() *slog.Logger  { return m.Log }

// features are the Manager's features with types of their own.
type features struct {
	once   sync.Once
	photos *Photos
	guests *Guests
	keys   *KeyChanges
}

func (m *Manager) features() *features {
	m.feats.once.Do(func() {
		m.feats.photos = &Photos{core: m}
		m.feats.guests = &Guests{core: m}
		m.feats.keys = &KeyChanges{core: m}
	})
	return &m.feats
}

// Photos is migrating photos into the lab's Immich services.
func (m *Manager) Photos() *Photos { return m.features().photos }

// Guests is storage for other labs.
func (m *Manager) Guests() *Guests { return m.features().guests }

// KeyChanges is changing the recovery key.
func (m *Manager) KeyChanges() *KeyChanges { return m.features().keys }
