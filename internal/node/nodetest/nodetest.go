// Package nodetest has the base of tests' fake servers (quality review
// T8). node's own tests can't import it (it imports node), so they embed
// their bareNode instead.
package nodetest

import (
	"context"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/nodeid"
)

// Base is a server (node.Node) that has nothing and does nothing: each
// method answers with its type's zero value, and it has none of the
// capabilities. A test's fake embeds it and defines only the methods it
// uses.
type Base struct{}

var _ node.Node = Base{}

func (Base) Identity() nodeid.Identity { return nodeid.Identity{} }
func (Base) Apply(node.Assignment)     {}
func (Base) Status() node.ApplyStatus  { return node.ApplyStatus{} }
func (Base) ServiceStates(context.Context) (map[string]string, error) {
	return nil, nil
}
func (Base) ServiceDetails(context.Context) (map[string]node.ServiceDetail, error) {
	return nil, nil
}
func (Base) BuiltIns(context.Context) ([]node.BuiltIn, error)      { return nil, nil }
func (Base) InstallStatus(context.Context, string) node.HookStatus { return node.HookStatus{} }
func (Base) RetryInstall(context.Context, string) error            { return nil }
func (Base) RunAction(context.Context, string, string, map[string]string) (string, error) {
	return "", nil
}
func (Base) RunScheduled(context.Context, string, string) error           { return nil }
func (Base) RevealSecret(context.Context, string, string) (string, error) { return "", nil }
func (Base) LatestActivity(string) (activity.Snapshot, bool)              { return activity.Snapshot{}, false }

func (Base) Backups() node.Backups     { return nil }
func (Base) Restores() node.Restores   { return nil }
func (Base) Updates() node.Updates     { return nil }
func (Base) Settings() node.Settings   { return nil }
func (Base) Media() node.Media         { return nil }
func (Base) Imports() node.Imports     { return nil }
func (Base) Mover() node.Mover         { return nil }
func (Base) Updater() node.Updater     { return nil }
func (Base) Disks() node.Disks         { return nil }
func (Base) Memory() node.Memory       { return nil }
func (Base) LeftData() node.LeftData   { return nil }
func (Base) History() node.History     { return nil }
func (Base) Linked() node.Linked       { return nil }
func (Base) GuestHost() node.GuestHost { return nil }
func (Base) Processor() node.Processor { return nil }
func (Base) Photos() node.Photos       { return nil }
