package cloud

import (
	"context"
	"errors"
	"net/http"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/sleep"
)

// Cloud's nudge (SPEC.md §6.16): Cloud says which service registry index
// is the newest it has seen, and the lab fetches the registry at once
// when that's newer than its own, so a security advisory reaches it in
// minutes rather than within the hour. Cloud gains no authority by it:
// the lab fetches the signed index from the registry and checks it as
// always. A relayed lab hears it in the relay's answer (RefreshRelay);
// another linked lab asks for it (WatchNews).

// NewsEvery is how often a linked lab that isn't relayed asks Cloud
// what's new.
const NewsEvery = 5 * time.Minute

// OnRegistry sets what's called with the newest registry index's serial
// Cloud has seen, each time Cloud says it.
func (s *Service) OnRegistry(fn func(serial int64)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registryNews = fn
}

// heardRegistry passes on what Cloud said of the registry.
func (s *Service) heardRegistry(serial int64) {
	s.mu.Lock()
	fn := s.registryNews
	s.mu.Unlock()
	if fn != nil && serial > 0 {
		fn(serial)
	}
}

// WatchNews asks Cloud what's new every so often while the lab is linked
// and not relayed, until ctx ends. A Cloud from before it (404) is asked
// again a day later; one that can't be reached, at the next turn.
func (s *Service) WatchNews(ctx context.Context, every time.Duration) {
	wait := every
	for {
		if !sleep.For(ctx, wait) {
			return
		}
		wait = every
		s.mu.Lock()
		ask := s.st.Linked && s.st.Relay == nil
		s.mu.Unlock()
		if !ask {
			continue
		}
		var news cloudapi.News
		err := s.client.do(ctx, true, http.MethodGet, cloudapi.PathLabNews, nil, &news)
		var e *Error
		switch {
		case errors.As(err, &e) && e.Status == http.StatusNotFound:
			wait = checkEvery
		case err != nil:
			s.log.Debug("Roostack Cloud: asking what's new", "err", err)
		default:
			s.heardRegistry(news.Registry)
		}
	}
}
