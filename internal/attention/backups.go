package attention

import (
	"context"
	"fmt"

	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/words"
)

// RetryText says when a failed backup is tried again (" Roostack will try
// again today 14:00."), or nothing.
func RetryText(r *backup.Retry) string {
	switch {
	case r == nil:
		return ""
	case r.Drive:
		return " It will back up there as soon as the drive is plugged in."
	}
	return " Roostack will try again " + words.When(r.At) + "."
}

// junk adds one item per backup with junk found: files the controller
// found in backups and couldn't read (SPEC.md §10.10, "Junk in a
// repository").
func (lab *Lab) junk(ctx context.Context, l *List) {
	for _, rep := range lab.Manager.JunkReports(ctx) {
		files, they, them := fmt.Sprintf("%d files", len(rep.Files)), "they", "them"
		if len(rep.Files) == 1 {
			files, they, them = "a file", "it", "it"
		}
		backups, hold := rep.Server+"'s backups", "hold"
		if rep.Copy {
			backups, hold = "the copy of "+rep.Server+"'s backups", "holds"
		}
		if rep.Removed() {
			l.add(Item{Key: "junk-removed:" + rep.ServerID + ":" + rep.Provider, Level: Warning,
				Text: "Roostack removed " + files + " it can't read from " + backups + " on " + rep.Where +
					": " + they + " stopped those backups working. Only " + OrList(rep.Writers) + " could have put " + them + " there.",
				Link: "/backups/junk", LinkText: "Details"})
			continue
		}
		l.add(Item{Key: "junk:" + rep.ServerID + ":" + rep.Provider, Level: Problem,
			Text: words.Capitalise(backups) + " on " + rep.Where + " " + hold + " " + files + " Roostack can't read, which " +
				words.Plural(len(rep.Files), "is stopping", "are stopping") + " those backups working, and Roostack couldn't remove " + them + ".",
			Link: "/backups/junk", LinkText: "What to do"})
	}
}

// OrList joins names as "a, b or c", or says "nobody" if there are none.
func OrList(names []string) string {
	if len(names) == 0 {
		return "nobody"
	}
	return words.Or(names)
}
