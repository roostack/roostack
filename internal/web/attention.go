package web

import (
	"context"

	"roostack.cloud/roostack/internal/attention"
)

// What needs attention (SPEC.md §6.13): the rules are internal/attention's;
// the home page shows them, and the notifier emails them.

// AttentionLab is the lab as the attention rules read it, with this
// server's features as they are now (they're set as Roostack starts).
func (s *Server) AttentionLab() *attention.Lab {
	lab := &attention.Lab{
		Manager:        s.Manager,
		Guests:         s.Guests,
		Node:           s.Node,
		Services:       s.attentionServices,
		Setup:          s.Setup,
		DetectNetwork:  s.DetectNetwork,
		LabName:        s.labName(),
		Domain:         s.Domain,
		UIHost:         s.UIHost,
		OldAddress:     s.OldAddress,
		Maintenance:    s.Maintenance,
		RestorePending: s.RestorePending,
		Cloud:          s.Cloud,
		CloudRefusals:  s.CloudRefusals,
		Registry:       s.Registry,
		Libraries:      s.Libraries,
		DNS:            s.DNS,
		HostChecks:     &s.hostChecks,
		Blocks:         s.wafBlocks,
		Protected:      s.Protected,
		Attacks:        s.Attacks,
	}
	// Monitoring is a pointer: nil stays nil (an interface holding a nil
	// pointer isn't).
	if s.Monitor != nil {
		lab.Monitor = s.Monitor
	}
	return lab
}

// attentionServices are the lab's services as the rules see them.
func (s *Server) attentionServices(ctx context.Context) ([]attention.Service, error) {
	views, err := s.Services(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]attention.Service, len(views))
	for i, v := range views {
		out[i] = attention.Service{Name: v.Name, Title: v.Title, Server: v.Server, URLs: v.URLs,
			Failed: v.Status == StatusFailed, Waiting: v.Status == StatusWaiting, WasOn: v.WasOn, Held: v.Held}
	}
	return out, nil
}

// serverNode is a server with what the UI needs from it.
type serverNode struct {
	view serverRef
	node ServiceNode
}

// serverNodes lists this server, then the lab's other reachable ones.
func (s *Server) serverNodes() []serverNode {
	var out []serverNode
	if s.Node != nil {
		out = append(out, serverNode{serverRef{ID: s.Node.Identity().ID, Name: s.Node.Identity().Name, This: true}, s.Node})
	}
	servers, _ := s.Manager.Servers()
	for _, mc := range servers {
		if mc.This {
			continue
		}
		n := s.Manager.NodeByID(mc.ID)
		if n == nil || mc.Unreached() {
			continue // already flagged as unreachable
		}
		out = append(out, serverNode{serverRef{ID: mc.ID, Name: mc.Name}, n})
	}
	return out
}
