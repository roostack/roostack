package web

import (
	"context"
	"log/slog"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/state"
)

// ServiceSource is what the dashboard's list of services is built from,
// besides the services themselves: what each server reports, and what
// the controller knows (*manager.Manager).
type ServiceSource interface {
	ServiceInfos(ctx context.Context) map[string]manager.ServiceInfo
	Withdrawals(ctx context.Context) map[string]*manager.Withdrawal
	AvailableUpdates(ctx context.Context) map[string]*manager.Update
	Updating(name string) *manager.ServiceUpdate
	Switching(service string) bool
	NodeByID(id string) node.Node
}

// ServiceViews builds the dashboard list from the installed services
// (desired state) and what src says of each, live.
func ServiceViews(ctx context.Context, lab config.Lab, services []state.Service, src ServiceSource, log *slog.Logger) []ServiceView {
	// Each server reports its own services (SPEC.md §10.3).
	infos := src.ServiceInfos(ctx)
	withdrawn := src.Withdrawals(ctx)
	updates := src.AvailableUpdates(ctx)
	var views []ServiceView
	for _, s := range services {
		v := ServiceView{Name: s.Name(), Title: s.Title(), Package: s.Package.Name, Local: s.Instance.Local, Held: s.Instance.Held}
		facts := ServiceFacts{Reported: StatusUnknown}
		if info, ok := infos[s.Name()]; ok {
			v.ServiceDetail, facts.Reported, v.Server, v.LastBackup = info.ServiceDetail, info.Status, info.Server, info.LastBackup
		}
		// Updates to newer versions (SPEC.md §7).
		if u := updates[s.Name()]; u != nil {
			v.Update = u.ToApp()
		}
		// What decides its status besides its server (ServiceStatus).
		w := withdrawn[s.Name()]
		facts.Stopped = w != nil && w.Stopped
		facts.Updating = src.Updating(s.Name()) != nil
		facts.AwaitingData, facts.Switching = s.Instance.AwaitingData, src.Switching(s.Name())
		facts.Held, facts.Waiting = s.Instance.Held != nil, s.Instance.Waiting
		v.Status = ServiceStatus(facts)
		if s.Instance.Waiting {
			// Its server is gone: it runs nowhere until restored (SPEC.md §10.10).
			v.Server = ""
			if n := src.NodeByID(s.Instance.Host); n != nil {
				v.WasOn = n.Identity().Name
			}
		}
		out, err := generate.Service(lab, s.Package, s.Instance)
		if err != nil {
			log.Error("generating service for status", "service", s.Name(), "err", err)
			v.Status = StatusFailed
			views = append(views, v)
			continue
		}
		for _, h := range out.Hosts {
			v.URLs = append(v.URLs, "https://"+h)
		}
		v.NoAddress = !lab.HasAddress() && len(s.Package.Routes) > 0
		views = append(views, v)
	}
	return views
}
