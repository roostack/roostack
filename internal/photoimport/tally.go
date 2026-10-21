package photoimport

import (
	"bufio"
	"encoding/json"
	"io"
	"path"
	"strings"
)

// immich-go logs an event for every file as a JSON line, its message the
// event's name (internal/fileevent at v0.32.0). Roostack counts them
// itself: what was sent, what Immich had, what was left out and why, and
// what failed, with names.
const (
	evImage       = "discovered image"
	evVideo       = "discovered video"
	evUnsupported = "discovered unsupported file"
	evUploaded    = "uploaded successfully"
	evUpgraded    = "server asset upgraded"
	evDuplicate   = "server has duplicate"
	evBetter      = "discarded server better"
	evBanned      = "discarded banned"
	evFiltered    = "discarded filtered"
	evLocalDup    = "discarded local duplicate"
	evNotSelected = "discarded not selected"
	evFailed      = "upload failed"
	evServerError = "server error"
	evFileAccess  = "file access error"
	evIncomplete  = "incomplete processing"
	evNoMetadata  = "missing metadata"
	evAlbum       = "added to album"
	evStacked     = "stacked"
	evTagged      = "tagged"
)

// maxNamed is how many files of each kind of problem are kept by name.
const maxNamed = 200

// Tally is what immich-go reported, file by file.
type Tally struct {
	// Found are the photos and videos it found.
	Found int64 `json:"found"`
	// Sent were uploaded (Upgraded replaced a smaller copy on Immich).
	Sent     int64 `json:"sent"`
	Upgraded int64 `json:"upgraded,omitempty"`
	// Had are already in the account (by checksum), or better there.
	Had int64 `json:"had"`
	// LeftOut are left out as chosen (the trash, a partner's photos),
	// Duplicates are the same photo twice in the export, Clutter files
	// immich-go always leaves out (.DS_Store and the like).
	LeftOut    int64 `json:"left_out"`
	Duplicates int64 `json:"duplicates,omitempty"`
	Clutter    int64 `json:"clutter,omitempty"`
	// Unsupported can't be migrated (a .MP motion file), with names in
	// Unreadable.
	Unsupported int64     `json:"unsupported,omitempty"`
	Unreadable  []Problem `json:"unreadable,omitempty"`
	// Failed didn't make it; Errors names them, with why.
	Failed int64     `json:"failed"`
	Errors []Problem `json:"errors,omitempty"`
	// NoDate came without a sidecar, so without Google's date.
	NoDate    int64     `json:"no_date,omitempty"`
	NoDates   []Problem `json:"no_dates,omitempty"`
	Skipped   []Problem `json:"skipped,omitempty"`
	InAlbums  int64     `json:"in_albums,omitempty"`
	Stacked   int64     `json:"stacked,omitempty"`
	Tagged    int64     `json:"tagged,omitempty"`
	SentBytes int64     `json:"sent_bytes,omitempty"`
	// Lines counts the log's lines read, so reading carries on from
	// there.
	Lines int64 `json:"lines"`
}

// Problem is a file and what happened to it.
type Problem struct {
	File string `json:"file"`
	Why  string `json:"why,omitempty"`
}

// Done is how many found have reached an end (sent, had, left out or
// failed): the progress bar.
func (t Tally) Done() int64 {
	return t.Sent + t.Upgraded + t.Had + t.LeftOut + t.Duplicates + t.Failed
}

// logLine is the part of a log line Roostack reads.
type logLine struct {
	Msg   string `json:"msg"`
	File  string `json:"file"`
	Error string `json:"error"`
	Size  int64  `json:"size"`
	// Some events say why in a "reason" field.
	Reason string `json:"reason"`
}

// Read adds the log's lines from r to the tally (all of r: call it with
// the log from where the last read ended, Lines in).
func (t *Tally) Read(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		t.Lines++
		var l logLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue // its banner and anything else that isn't an event
		}
		t.add(l)
	}
	return sc.Err()
}

func (t *Tally) add(l logLine) {
	why := strings.TrimSpace(l.Error + " " + l.Reason)
	switch l.Msg {
	case evImage, evVideo:
		t.Found++
	case evUploaded:
		t.Sent++
		t.SentBytes += l.Size
	case evUpgraded:
		t.Upgraded++
	case evDuplicate, evBetter:
		t.Had++
	case evFiltered, evNotSelected:
		t.LeftOut++
		t.Skipped = keep(t.Skipped, Problem{l.File, why})
	case evLocalDup:
		t.Duplicates++
	case evBanned:
		t.Clutter++
	case evUnsupported:
		if !isMedia(l.File) {
			t.Clutter++ // Takeout's own files: album metadata, its index page
			break
		}
		t.Unsupported++
		t.Unreadable = keep(t.Unreadable, Problem{l.File, "Immich can't read this kind of file"})
	case evFailed, evServerError, evFileAccess, evIncomplete:
		t.Failed++
		t.Errors = keep(t.Errors, Problem{l.File, why})
	case evNoMetadata:
		t.NoDate++
		t.NoDates = keep(t.NoDates, Problem{File: l.File})
	case evAlbum:
		t.InAlbums++
	case evStacked:
		t.Stacked++
	case evTagged:
		t.Tagged++
	}
}

func keep(list []Problem, p Problem) []Problem {
	if len(list) >= maxNamed {
		return list
	}
	return append(list, p)
}

// isMedia says whether a logged file looks like a photo or video (not a
// sidecar, an album's metadata or Takeout's index page), by its name after
// the archive's ("part.zip:Takeout/…/IMG_1.JPG").
func isMedia(file string) bool {
	ext := strings.ToLower(path.Ext(file))
	return ext != "" && ext != ".json" && ext != ".html" && ext != ".htm" && ext != ".txt" && ext != ".csv" && ext != ".xmp"
}
