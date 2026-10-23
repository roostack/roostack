package web

import (
	"fmt"
	"html/template"
	"path"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/errpage"
	"roostack.cloud/roostack/internal/labrestore"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/monitor"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/photoimport"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/words"
	"roostack.cloud/roostack/units"
)

// templateFuncs are the functions every template can call. It's a
// function, not a variable, because the components among them render
// templates that need these functions themselves (components.go).
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		// Components (components.go, docs/DESIGN.md).
		"notice":        notice,
		"noticeList":    noticeList,
		"status":        status,
		"badge":         badge,
		"logo":          logo,
		"back":          back,
		"help":          helpLink,
		"confirm":       confirmButton,
		"confirmSubmit": confirmSubmit,
		"copyBox":       copyBox,
		"activity":      activityBlock,
		"icon":          icon,
		"server":        serverChip,
		"builtinIcon":   builtinIcon,
		"uptimeBar":     uptimeBar,
		"checkState":    checkState,
		"segmented":     segmented,
		"choice":        segChoice,
		"folderCounts":  folderCounts,
		"pickerDialog":  pickerDialog,
		"brandMark":     func() template.HTML { return brandMark },
		// errorScene NAME ID: an error scene (internal/errpage), its wash's
		// SVG ID made unique on the page by ID.
		"errorScene": func(name, id string) template.HTML { return template.HTML(errpage.Scene(name, id)) },

		// Words, times and sizes, and how pages look.
		"when": words.When,
		// daysText and certWarning are monitoring's (monitor.go).
		"daysText":    monitor.DaysText,
		"certWarning": func(st monitor.Status) string { return st.CertWarningText(time.Now()) },
		// retryText says when a failed backup is tried again (" Roostack tries
		// again today 14:00."), or nothing.
		"retryText": attention.RetryText,
		"stale":     manager.BackupStale,
		// firstLine and otherLines split an error: restic's own first line,
		// then what follows (a stack trace, at worst), shown under Details.
		"firstLine":  words.FirstLine,
		"otherLines": otherLines,
		// shownRepo is a backup's address without its password.
		"shownRepo": labrestore.Shown,
		// date is a day, e.g. 28 September 2027.
		"date": func(t time.Time) string { return t.Local().Format("2 January 2006") },
		// has says whether a list has an item.
		"has": func(list []string, item string) bool { return slices.Contains(list, item) },
		// joinAnd lists names for people: "Films, TV and Music".
		"joinAnd": words.And,
		// list makes a slice, e.g. {{range list "light" "dark"}}.
		"list": func(items ...string) []string { return items },
		"size": units.Bytes,
		"gb":   func(b int64) int64 { return b / units.GB },
		// image is a container image without its digest (shown in full on
		// hover): docker.io/vaultwarden/server:1.34.3.
		"image": func(ref string) string { name, _, _ := strings.Cut(ref, "@"); return name },
		// shortCommit is a git commit's first 12 characters, or all of it
		// if it's shorter (an index can say anything there).
		"shortCommit": func(c string) string {
			if r := []rune(c); len(r) > 12 {
				return string(r[:12])
			}
			return c
		},
		// percent shows progress, or nothing if it's unknown (-1).
		"percent": func(p int) string {
			if p < 0 {
				return ""
			}
			return fmt.Sprintf("%d%%", p)
		},
		// palette is the built-in theme (internal/web/static/css/themes).
		"palette": func() string { return DefaultPalette },
		"themes":  func() []Theme { return Themes },
		"bytes":   func(n uint64) string { return units.Bytes(int64(n)) },
		// count is a count with thousands separators ("1,284").
		"count":    units.Count,
		"idleText": idleText,
		// initial is a name's first letter, capitalised: the person's avatar
		// in the sidebar.
		"initial": func(name string) string {
			for _, r := range name {
				return strings.ToUpper(string(r))
			}
			return "?"
		},
		// roleName shows a role as people say it ("Owner").
		"roleName": words.Capitalise,
		// capitalise starts a word with a capital ("running" → "Running").
		"capitalise": words.Capitalise,
		// whenParts is a time as when shows it, split into its day and its
		// clock time ("today", "02:00"), for a big figure.
		"whenParts": func(t time.Time) [2]string {
			w := words.When(t)
			if i := strings.LastIndex(w, " "); i > 0 {
				return [2]string{w[:i], w[i+1:]}
			}
			return [2]string{"", w}
		},
		// sentences splits text after its first sentence: the attention
		// items' bold line, and the rest under it ("" if there's no more).
		"sentences": firstSentence,
		// trimScheme shows a web address without its https:// (in tables).
		"trimScheme": func(u string) string {
			return strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
		},

		// Installing (install.go, SPEC.md §6.3.3).
		"took": tookText,
		"add1": func(i int) int { return i + 1 },
		"lowerFirst": func(s string) string {
			if s == "" {
				return s
			}
			return strings.ToLower(s[:1]) + s[1:]
		},
		// slowStep: the install's current step has run for longer than usual.
		"slowStep": func(p *node.InstallProgress) bool {
			s := p.Step()
			return !s.Started.IsZero() && time.Since(s.Started) > longStep
		},

		// A package's facts on its install page (install.go).
		"licenceName":  licenceName,
		"licenceURL":   licenceURL,
		"databaseName": databaseName,
		"pathBase":     path.Base,
		"pathDir":      path.Dir,

		// Media libraries (media.go).
		"dialogFor": dialogFor,
		// join lists strings with a separator, e.g. services reading a library.
		"join": strings.Join,

		// Migrating photos into Immich (photos.go, SPEC.md §8.5).
		"kindTitle":   photoimport.KindTitle,
		"photoTotals": photoTotals,
		"choicesText": choicesText,
		"leftOutText": leftOutText,
		"sentPhotos":  func(r *node.PhotoRun) int64 { return r.Migrated() },
		"immichMore": func(before, after node.PhotoAccount) int64 {
			return after.Photos + after.Videos - before.Photos - before.Videos
		},
		// roughDuration is how long a run took, roughly ("25 minutes").
		"roughDuration":    func(from, to time.Time) string { return words.RoughDuration(to.Sub(from)) },
		"add":              func(a, b int64) int64 { return a + b },
		"sub":              func(a, b int64) int64 { return max(0, a-b) },
		"runPercent":       func(r *node.PhotoRun) int { return photoProgressOf(r).Percent },
		"runTimeLeft":      func(r *node.PhotoRun) string { return photoProgressOf(r).TimeLeft },
		"migrationResult":  migrationResult,
		"libraryWhereText": libraryWhereText,

		// Public access (public.go).
		"publicState": func(state string) string { return publicState[state] },

		// Service updates (serviceupdates.go, SPEC.md §7).
		"appVersion": manager.AppVersion,
		// sentence ends an error's text with a full stop.
		"sentence":         func(s string) string { return strings.TrimRight(strings.TrimSpace(s), ".") + "." },
		"revision":         func(v string) string { return strings.TrimPrefix(v[len(pkgspec.Upstream(v)):], "-roostack.") },
		"updateKind":       updateKindText,
		"updatePolicyText": updatePolicyText,

		// Single sign-on (sso.go).
		// offText says what turning single sign-on off does, for its confirm.
		"offText": func(c *ssoCard) string {
			if c.Wall {
				return "Anyone who can reach " + c.Title + " will be able to use it again without signing in."
			}
			return "People will sign in with " + c.Title + "'s own passwords again; accounts it made at single sign-on have none, so they'd need one set in " + c.Title + "."
		},
	}
}

// firstSentence splits text into its first sentence and the rest: at
// the first ". ", "? " or "! " followed by a capital letter.
func firstSentence(text string) [2]string {
	for i := 0; i+2 < len(text); i++ {
		if strings.ContainsRune(".?!", rune(text[i])) && text[i+1] == ' ' && text[i+2] >= 'A' && text[i+2] <= 'Z' {
			return [2]string{text[:i+1], text[i+2:]}
		}
	}
	return [2]string{text, ""}
}

// otherLines is what follows text's first line (words.FirstLine),
// or "": restic's stack trace after its error, at worst, which has no
// break points and widens a phone's page, so it's shown under Details.
func otherLines(text string) string {
	_, rest, _ := strings.Cut(text, "\n")
	return strings.TrimSpace(rest)
}
