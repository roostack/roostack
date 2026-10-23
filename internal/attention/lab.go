package attention

import (
	"context"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/cloud"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/monitor"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/public"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/setup"
	"roostack.cloud/roostack/internal/store"
)

// Lab is what the rules read: the lab's manager and servers, and the
// features Roostack has of the lab. Roostack sets its features as it
// starts, so a Lab is made afresh for each use (web.Server.AttentionLab).
// A nil feature is one this server doesn't have, and says nothing.
type Lab struct {
	Manager Manager
	// Guests is storage for other labs (manager.Guests).
	Guests Guests
	// Node is this server.
	Node Node
	// Services lists the lab's services and how each is.
	Services func(ctx context.Context) ([]Service, error)
	// Setup is the setup steps and where the lab is; DetectNetwork
	// guesses where from this server's addresses (nil guesses nothing).
	Setup         Setup
	DetectNetwork func() string
	// The lab's settings (config.Lab): what people call it, its address,
	// where Roostack's own pages are, its previous address while that
	// still works, and its restart policy.
	LabName, Domain, UIHost string
	OldAddress              *config.OldAddress
	Maintenance             config.Maintenance
	// RestorePending says a restored lab's services' databases are still
	// being put back.
	RestorePending func() bool
	Cloud          Cloud
	Registry       Registry
	Libraries      Libraries
	DNS            DNS
	Monitor        Monitor
	// HostChecks are the servers' checks for changes Roostack didn't
	// make, kept for a while (the Updates pages share them).
	HostChecks *HostChecks
	// For the weekly summary: whether CrowdSec is running on the public
	// addresses, what it saw over a while, and the requests AppSec
	// refused this week (false: CrowdSec didn't answer).
	Protected func(ctx context.Context) bool
	Attacks   func(ctx context.Context, since time.Duration) (public.Attacks, error)
	Blocks    func(ctx context.Context) ([]public.Block, bool)
}

// Manager is what the rules ask the lab's manager (internal/manager).
type Manager interface {
	Servers() ([]manager.Server, error)
	// NodeByID is a server by ID, and NodeFor the one a service runs on
	// (nil if none).
	NodeByID(id string) node.Node
	NodeFor(service string) node.Node
	EndingStorages(ctx context.Context) []manager.EndingStorage
	LostMainBackup(ctx context.Context, serverID string) (store.LostBackup, bool)
	JunkReports(ctx context.Context) []manager.JunkReport
	PublicHosts() []string
	// PublicPauses is what's paused (SPEC.md §6.16), and Advisories the
	// registry's security advisories affecting each service.
	PublicPauses() (manager.PublicPauses, error)
	Advisories(ctx context.Context) map[string][]*manager.ServiceAdvisory
	RoostackAdvisories(ctx context.Context) []*manager.ServiceAdvisory
	Edge(ctx context.Context) (manager.EdgeServer, bool)
	RelayOn() bool
	Withdrawals(ctx context.Context) map[string]*manager.Withdrawal
	FailedMoves(ctx context.Context) map[string]manager.FailedMove
	AvailableUpdates(ctx context.Context) map[string]*manager.Update
	Updating(name string) *manager.ServiceUpdate
	LastUpdate(ctx context.Context, name string) *manager.UpdateRecord
}

// Guests is what the rules ask about storage for other labs
// (manager.Guests): the server that stores other labs' backups, and
// those labs.
type Guests interface {
	GuestServer(ctx context.Context) string
	StorageGuests(ctx context.Context) ([]manager.GuestView, error)
}

// Node is what the rules ask a server (internal/node).
type Node interface {
	Identity() nodeid.Identity
	InstallStatus(ctx context.Context, name string) manager.HookStatus
	Backups() node.Backups
	Updates() node.Updates
	Settings() node.Settings
	Disks() node.Disks
}

// Setup is the setup steps, and where the lab is (internal/setup).
type Setup interface {
	Progress(ctx context.Context) (setup.Progress, error)
	Network(detect func() string) setup.NetworkChoice
}

// Cloud is the lab's link to Roostack Cloud (internal/cloud).
type Cloud interface {
	Status() cloud.Status
	RelayName() (string, *cloudapi.RelayDomain)
	RelayStanding() (turnedOff time.Time, suspended string)
	RelayPaused() time.Time
}

// Registry keeps the catalog up to date with the service registry.
type Registry interface {
	On() bool
	Status() registry.Status
}

// Libraries are the media libraries (manager).
type Libraries interface {
	ServiceLibraries(ctx context.Context, service string) (manager.ServiceLibraryChoice, error)
}

// DNS is the lab's DNS records (internal/dnsrecords).
type DNS interface {
	Status() dnsrecords.Status
}

// Monitor is monitoring (internal/monitor).
type Monitor interface {
	Statuses() []monitor.Status
}

// Service is one of the lab's services, as the rules see it.
type Service struct {
	Name, Title string
	// Server names the server it runs on, and URLs are its addresses.
	Server string
	URLs   []string
	// Failed: it stopped working (or Roostack couldn't work out its
	// files). Waiting: its server is gone, and it waits to be restored;
	// WasOn names that server.
	Failed, Waiting bool
	WasOn           string
	// Held is its hold after a restore or move that didn't finish (nil if
	// none).
	Held *instance.Hold
}

// Server names one of the lab's servers in what the rules say, and links
// to its pages.
type Server struct {
	ID, Name string
	// This is the server serving Roostack's pages.
	This bool
}

// Label is how a sentence starts with the server: "This server", or its
// name.
func (m Server) Label() string {
	if m.This || m.Name == "" {
		return "This server"
	}
	return m.Name
}

// InSentence is how a sentence names the server after its first word:
// "this server", or its name.
func (m Server) InSentence() string {
	if m.This || m.Name == "" {
		return "this server"
	}
	return m.Name
}

// on names another server in a message ("" for this one).
func (m Server) on() string {
	if m.This {
		return ""
	}
	return " on " + m.Name
}

// backups and updates are the server's Backups and Updates pages.
func (m Server) backups() string { return m.path("/backups", "/backups") }
func (m Server) updates() string { return m.path("/settings/updates", "/updates") }

func (m Server) path(this, other string) string {
	if m.This {
		return this
	}
	return "/settings/servers/" + m.ID + other
}

// serverNode is a server with what's asked of it.
type serverNode struct {
	view Server
	node Node
}

// servers lists this server, then the lab's other servers that answer.
func (lab *Lab) servers() []serverNode {
	var out []serverNode
	if lab.Node != nil {
		id := lab.Node.Identity()
		out = append(out, serverNode{Server{ID: id.ID, Name: id.Name, This: true}, lab.Node})
	}
	servers, _ := lab.Manager.Servers()
	for _, mc := range servers {
		if mc.This {
			continue
		}
		n := lab.Manager.NodeByID(mc.ID)
		if n == nil || mc.Unreached() {
			continue // already flagged as unreachable
		}
		out = append(out, serverNode{Server{ID: mc.ID, Name: mc.Name}, n})
	}
	return out
}

// network is where the lab is: the saved answer and a guess.
func (lab *Lab) network() setup.NetworkChoice {
	return lab.Setup.Network(lab.DetectNetwork)
}

// onInternet says the lab is on the internet, as saved.
func (lab *Lab) onInternet() bool {
	return lab.Setup.Network(nil).Saved == config.NetworkInternet
}
