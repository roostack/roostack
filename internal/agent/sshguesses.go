package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// SSH password guesses (SPEC.md §6.10 item 3): the agent follows sshd's
// log and counts failed password logins, an hour at a time, so a page can
// say how many there were in the last day without searching the journal.
// A server open to the internet logs tens of thousands a day.

// guessDay is how far back the count reaches.
const guessDay = 24 * time.Hour

// failedRe matches sshd's line for a failed password. The account name is
// the guesser's own and may hold anything, " from " included, so the
// address is the last "from" before the end that sshd itself writes.
var failedRe = regexp.MustCompile(`^Failed (?:password|keyboard-interactive/pam) for .* from (\S+) port \d+ ssh2$`)

// acceptedFromRe matches sshd's line for any successful login: the
// account is a real one, so it holds no spaces.
var acceptedFromRe = regexp.MustCompile(`^Accepted \S+ for \S+ from (\S+) port \d+ ssh2`)

// sshGuesses counts this server's guesses.
var sshGuesses guessLog

type guessLog struct {
	mu sync.Mutex
	// ready: the last day has been read, so the count is whole.
	ready bool
	// guessed, if set, hears of each guess (sshBlocks.failed), and in of
	// each successful login (sshBlocks.loggedIn).
	guessed func(at time.Time, addr netip.Addr, now time.Time)
	in      func(at time.Time, addr netip.Addr)
	hours   map[int64]*guessHour // by hours since 1970
}

type guessHour struct {
	n     int
	addrs map[netip.Addr]int
}

// reset forgets every guess, before the last day is read again.
func (g *guessLog) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hours, g.ready = nil, false
}

// add counts one guess from addr at a time.
func (g *guessLog) add(at time.Time, addr netip.Addr) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hours == nil {
		g.hours = map[int64]*guessHour{}
	}
	h := g.hours[at.Unix()/3600]
	if h == nil {
		h = &guessHour{addrs: map[netip.Addr]int{}}
		g.hours[at.Unix()/3600] = h
	}
	h.n++
	h.addrs[addr.Unmap()]++
}

// day returns the guesses in the last day and how many addresses they
// came from, and whether the count is whole yet. Hours older than a day
// are dropped.
func (g *guessLog) day(now time.Time) (guesses, addrs int, ready bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	from := now.Add(-guessDay).Unix() / 3600
	seen := map[netip.Addr]bool{}
	for hour, h := range g.hours {
		if hour <= from {
			delete(g.hours, hour)
			continue
		}
		guesses += h.n
		for a := range h.addrs {
			seen[a] = true
		}
	}
	return guesses, len(seen), g.ready
}

// line counts one of sshd's lines, if it's a failed password, and passes
// on a successful login.
func (g *guessLog) line(message string, at time.Time) {
	if m := acceptedFromRe.FindStringSubmatch(message); m != nil {
		if addr, err := netip.ParseAddr(m[1]); err == nil && g.in != nil {
			g.in(at, addr)
		}
		return
	}
	m := failedRe.FindStringSubmatch(message)
	if m == nil {
		return
	}
	if addr, err := netip.ParseAddr(m[1]); err == nil {
		g.add(at, addr)
		if g.guessed != nil {
			g.guessed(at, addr, time.Now())
		}
	}
}

// FollowSSHLog counts SSH password guesses until ctx ends: it reads the
// last day of sshd's log once, then each line as journald writes it. If
// journalctl stops, it starts again a little later from the last line it
// read.
func FollowSSHLog(ctx context.Context, log *slog.Logger) {
	if _, err := os.Stat(sshdBin); err != nil {
		return // no SSH server
	}
	sshBlocks.log = log
	if err := sshBlocks.load(); err != nil {
		log.Warn("reading which addresses were unblocked from SSH", "err", err)
	}
	sshGuesses.guessed, sshGuesses.in = sshBlocks.failed, sshBlocks.loggedIn
	cursor, read := "", false
	var readAt time.Time
	for {
		var err error
		follow := read
		if !follow {
			// Again from the start after a failed read, so nothing counts
			// twice.
			sshGuesses.reset()
			readAt = time.Now()
			since := "@" + strconv.FormatInt(readAt.Add(-guessDay).Unix(), 10)
			if cursor, err = sshGuesses.journal(ctx, "", false, "--since", since); err == nil {
				read = true
				sshGuesses.mu.Lock()
				sshGuesses.ready = true
				sshGuesses.mu.Unlock()
			}
		} else {
			// From the last line read, or from when the day was read if it
			// had none.
			from := []string{"--since", "@" + strconv.FormatInt(readAt.Unix(), 10)}
			if cursor != "" {
				from = []string{"--after-cursor", cursor}
			}
			cursor, err = sshGuesses.journal(ctx, cursor, true, from...)
		}
		if ctx.Err() != nil {
			return
		}
		if err == nil && !follow {
			continue // the last day is read: follow from there
		}
		log.Warn("reading sshd's log for password guesses: journalctl stopped, starting it again in a minute", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

// journal runs journalctl over sshd's failed passwords and successful
// logins, following or not, passes each to line, and returns the cursor of
// the last line it read.
func (g *guessLog) journal(ctx context.Context, cursor string, follow bool, from ...string) (string, error) {
	args := []string{"_UID=0"}
	for _, exe := range sshdExes {
		args = append(args, "_EXE="+exe)
	}
	args = append(args, from...)
	args = append(args, "--grep", "^(Failed (password|keyboard-interactive/pam)|Accepted [a-z-]+) for ",
		"--output", "json", "--output-fields", "MESSAGE", "--no-pager", "--quiet")
	if follow {
		args = append(args, "--follow")
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return cursor, err
	}
	if err := cmd.Start(); err != nil {
		return cursor, err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var e struct {
			Message string `json:"MESSAGE"`
			At      string `json:"__REALTIME_TIMESTAMP"`
			Cursor  string `json:"__CURSOR"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		usec, _ := strconv.ParseInt(e.At, 10, 64)
		g.line(e.Message, time.UnixMicro(usec))
		cursor = e.Cursor
	}
	err = cmd.Wait()
	var exit *exec.ExitError
	if !follow && errors.As(err, &exit) && exit.ExitCode() == 1 {
		err = nil // --grep found nothing
	}
	return cursor, err
}
