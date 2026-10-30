package cloud

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/cloudapi"
)

// What serves the lab's public addresses (SPEC.md §6.11, "Roostack
// Cloud's names and relay"): the lab tells Cloud each address that comes
// through its roost.ac name or the relay, with the package that serves it
// (PublicHosts), whenever they change and after each check-in. Cloud
// answers with those it won't carry, a service from one of the Owner's
// own packages on a roost.ac name, or one the registry flags as needing
// approval, for an account it hasn't approved; the lab keeps those
// private, and says why. It's an honest lab's say: a changed Roostack
// could say anything, and Cloud's outside checks are the real check.

// publicState is what the lab last told Cloud about its public addresses.
type publicState struct {
	// Sent are the addresses as last told, which Cloud answered.
	Sent []cloudapi.PublicHost `json:"sent"`
	// At is when Cloud answered.
	At time.Time `json:"at"`
	// Unsupported: Cloud is from before PublicHosts (it answered 404),
	// and carries every address.
	Unsupported bool `json:"unsupported,omitempty"`
}

// policyState says a name state is one of Cloud's refusals for what serves
// an address (PublicAnswer), rather than for the name itself.
func policyState(state string) bool {
	return state == cloudapi.NameOwnPackage || state == cloudapi.NameNeedsApproval
}

// SendPublic tells Cloud what serves the lab's public addresses (Public),
// if they've changed since it last answered: after an apply that may have
// changed them. A Cloud from before it answers 404, and the lab carries on
// without it, quietly.
func (s *Service) SendPublic(ctx context.Context) error { return s.sendPublic(ctx, false) }

// sendPublic is SendPublic; force tells Cloud even when nothing changed
// (each check-in, so Cloud's record never drifts for long).
func (s *Service) sendPublic(ctx context.Context, force bool) error {
	if s.Public == nil {
		return nil
	}
	hosts := sortedHosts(s.Public())
	if len(hosts) > cloudapi.MaxPublicHosts {
		hosts = hosts[:cloudapi.MaxPublicHosts]
	}
	if hosts == nil {
		hosts = []cloudapi.PublicHost{} // none, rather than unknown
	}
	s.publicMu.Lock()
	defer s.publicMu.Unlock()
	s.mu.Lock()
	linked, p := s.st.Linked, s.st.Public
	s.mu.Unlock()
	if !linked || !force && p != nil && slices.Equal(p.Sent, hosts) {
		return nil
	}
	var answer cloudapi.PublicAnswer
	err := s.client.do(ctx, true, http.MethodPut, cloudapi.PathLabPublic, cloudapi.PublicHosts{Hosts: hosts}, &answer)
	var e *Error
	unsupported := errors.As(err, &e) && e.Status == http.StatusNotFound
	if err != nil && !unsupported {
		return err
	}
	s.mu.Lock()
	before := s.refusals()
	s.st.Public = &publicState{Sent: hosts, At: s.now(), Unsupported: unsupported}
	s.st.Names = withPolicy(s.st.Names, answer.Names)
	changed := !slices.Equal(before, s.refusals()) || p == nil || !slices.Equal(p.Sent, hosts) || p.Unsupported != unsupported
	err = s.save()
	s.mu.Unlock()
	if changed && s.PublicChanged != nil {
		s.PublicChanged()
	}
	return err
}

// PublicVerdict is how Cloud stands on a public address and what serves
// it: refused is why it won't carry it (nil: it will); known says Cloud
// has answered for it as it is, or it doesn't decide (the lab isn't
// linked, or Cloud is from before PublicHosts). Until it's known, a lab
// keeps an address Cloud may refuse private.
func (s *Service) PublicVerdict(h cloudapi.PublicHost) (refused *cloudapi.NameStatus, known bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.st.Public
	if !s.st.Linked {
		return nil, true
	}
	if p == nil {
		return nil, false
	}
	if p.Unsupported {
		return nil, true
	}
	if !slices.Contains(p.Sent, h) {
		return nil, false
	}
	for _, n := range s.st.Names {
		if n.Host == h.Host && policyState(n.State) {
			return &n, true
		}
	}
	return nil, true
}

// refusals are the addresses Cloud won't carry for what serves them, as
// "host state", sorted, to see whether an answer changed them (s.mu held).
func (s *Service) refusals() []string {
	var out []string
	for _, n := range s.st.Names {
		if policyState(n.State) {
			out = append(out, n.Host+" "+n.State)
		}
	}
	slices.Sort(out)
	return out
}

// setNames takes Cloud's list of the lab's names that don't work (a
// check-in's, the relay's), telling PublicChanged when its refusals for
// what serves an address changed: an account approved, say (s.mu held;
// the caller calls it once it's let go, when told to).
func (s *Service) setNames(names []cloudapi.NameStatus) (publicChanged bool) {
	before := s.refusals()
	s.st.Names = names
	return !slices.Equal(before, s.refusals())
}

// publicChanged calls PublicChanged, if set and changed (s.mu not held).
func (s *Service) publicChanged(changed bool) {
	if changed && s.PublicChanged != nil {
		s.PublicChanged()
	}
}

// withPolicy is names with Cloud's refusals for what serves an address
// replaced by those in answer.
func withPolicy(names, answer []cloudapi.NameStatus) []cloudapi.NameStatus {
	names = slices.DeleteFunc(slices.Clone(names), func(n cloudapi.NameStatus) bool { return policyState(n.State) })
	for _, n := range answer {
		if policyState(n.State) {
			names = append(names, n)
		}
	}
	return names
}

// sortedHosts is hosts sorted by address, for comparing with what Cloud
// was told.
func sortedHosts(hosts []cloudapi.PublicHost) []cloudapi.PublicHost {
	hosts = slices.Clone(hosts)
	slices.SortFunc(hosts, func(a, b cloudapi.PublicHost) int { return strings.Compare(a.Host, b.Host) })
	return slices.CompactFunc(hosts, func(a, b cloudapi.PublicHost) bool { return a.Host == b.Host })
}
