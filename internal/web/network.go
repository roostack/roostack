package web

import (
	"net/http"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/setup"
)

// Where the lab is (SPEC.md §6.12): at home behind a router, or on the
// internet (a cloud server). It decides what "private" means (the home
// network, or only through an SSH tunnel), whether services start public,
// whether Roostack's own pages are public, and what the public access
// checklist asks for (router or provider firewall).

// network is where the lab is: the saved answer and a guess.
func (s *Server) network() setup.NetworkChoice {
	if s.Setup == nil {
		return setup.NetworkChoice{}
	}
	return s.Setup.Network(s.DetectNetwork)
}

// onInternet says the lab is on the internet, as saved (what Caddy and DNS
// follow; a guess changes nothing until the Owner confirms it).
func (s *Server) onInternet() bool {
	return s.Setup != nil && s.Setup.Network(nil).Saved == config.NetworkInternet
}

// networkSubmit saves where the lab is (Settings → Network), then
// Roostack restarts to apply it.
func (s *Server) networkSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Setup == nil {
		http.NotFound(w, r)
		return
	}
	network := r.FormValue("network")
	tunnelOnly := network == config.NetworkInternet && r.FormValue("ui") == "tunnel"
	if err := s.Setup.SetNetwork(network, tunnelOnly); err != nil {
		if msg, ok := userMessage(err); ok {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		s.serverError(w, err)
		return
	}
	detail := map[string]string{config.NetworkHome: "at home", config.NetworkInternet: "on the internet"}[network]
	if network == config.NetworkInternet {
		detail += map[bool]string{true: "; Roostack's pages through an SSH tunnel only", false: "; Roostack's pages public"}[tunnelOnly]
	}
	s.Store.Audit(r.Context(), currentUser(r), "network", "lab is "+detail)
	redirect(w, r, "/restarting")
}

// networkForm is the "where is this lab" choice (the network-choice
// template), in the wizard's first step and Settings → Network.
type networkForm struct {
	setup.NetworkChoice
	// Domain names Roostack's own address, if the lab has one yet.
	Domain string
}

func (s *Server) networkForm(r *http.Request) networkForm {
	f := networkForm{NetworkChoice: s.network()}
	if s.Domain != "" && s.Setup != nil && s.domainDone(r) {
		f.Domain = s.Domain
	}
	return f
}
