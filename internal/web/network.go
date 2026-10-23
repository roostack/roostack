package web

import (
	"net/http"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/setup"
	"roostack.cloud/roostack/internal/usererr"
)

// Where the lab is (SPEC.md §6.12): at home behind a router, or on the
// internet (a cloud server). It decides what "private" means (the home
// network, or only through an SSH tunnel), whether services start public,
// whether Roostack's own pages are public, and what the public access
// checklist asks for (router or provider firewall).

// network is where the lab is: the saved answer and a guess.
func (s *Server) network() setup.NetworkChoice {
	return s.Setup.Network(s.DetectNetwork)
}

// onInternet says the lab is on the internet, as saved (what Caddy and DNS
// follow; a guess changes nothing until the Owner confirms it).
func (s *Server) onInternet() bool {
	return s.Setup.Network(nil).Saved == config.NetworkInternet
}

// networkSubmit saves where the lab is (Settings → Network), then
// Roostack restarts to apply it.
func (s *Server) networkSubmit(w http.ResponseWriter, r *http.Request) {
	network := r.FormValue("network")
	tunnelOnly := network == config.NetworkInternet && r.FormValue("ui") == "tunnel"
	if err := s.Setup.SetNetwork(network, tunnelOnly); err != nil {
		if msg, ok := usererr.Message(err); ok {
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
	// Domain names Roostack's own address (UIHost), if the lab has one yet.
	Domain, UIHost string
	// Selected is the choice the form starts with.
	Selected string
}

// networkForm starts the choice at the guess in the wizard (suggest), but
// in Settings at what's in effect: with nothing saved yet that's at home,
// so a guess doesn't look like a saved setting.
func (s *Server) networkForm(r *http.Request, suggest bool) networkForm {
	f := networkForm{NetworkChoice: s.network()}
	f.Selected = f.Saved
	if suggest {
		f.Selected = f.Current()
	} else if f.Selected == "" {
		f.Selected = config.NetworkHome
	}
	if s.Domain != "" && s.domainDone(r) {
		f.Domain, f.UIHost = s.Domain, s.uiHost()
	}
	return f
}
