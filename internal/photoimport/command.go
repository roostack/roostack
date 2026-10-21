package photoimport

import (
	"fmt"
	"path"
	"strconv"
)

// Choices are what the person chose to migrate (step 5, "What to
// migrate"). Roostack's suggestions are DefaultChoices.
type Choices struct {
	// Takeout only: photos in Google's trash, a partner's photos shared
	// with them, and albums without a name.
	Trash    bool `json:"trash"`
	Partner  bool `json:"partner"`
	Untitled bool `json:"untitled"`
	// People: Google's names for the people in a photo, as tags
	// (people/Anna); Immich finds faces itself.
	People bool `json:"people"`
	// Stacks: bursts, RAW with JPEG and HEIC with JPEG shown as one.
	Stacks bool `json:"stacks"`
	// FolderAlbums (a folder only): each folder becomes an album.
	FolderAlbums bool `json:"folder_albums"`
}

// DefaultChoices are Roostack's suggestions (docs/IMMICH-IMPORT.md §8):
// not the trash (deleted on purpose), not a partner's photos (in a
// family lab the partner migrates their own, and Immich's partner
// sharing shows them), not albums without a name (Google's own clutter).
func DefaultChoices() Choices {
	return Choices{People: true, Stacks: true, FolderAlbums: true}
}

// Run is one run of immich-go.
type Run struct {
	Kind    string
	Choices Choices
	// Server is Immich's address from immich-go's container, on the
	// service's own network (http://roostack-immich-server:2283).
	Server string
	// Source is where the export is inside the container; Parts are a
	// Takeout's zips in it (immich-go reads them all in one run).
	Source string
	Parts  []string
	// Log is immich-go's JSON log, inside the container.
	Log string
	// DryRun checks without sending anything (step 6).
	DryRun bool
	// Tasks is how many it sends at once: a few, so a small server
	// keeps up and immich-go's memory stays bounded (#1395).
	Tasks int
}

// KeyEnv is the environment variable immich-go reads the key from, so
// it's never on a command line; Roostack passes it from a Podman secret.
const KeyEnv = "IMMICH_GO_UPLOAD_API_KEY"

// Command is immich-go's arguments for a run (v0.32.0's flags,
// docs/commands/upload.md). The key isn't among them: see KeyEnv.
func Command(r Run) ([]string, error) {
	sub := map[string]string{KindGoogle: "from-google-photos", KindFolder: "from-folder"}[r.Kind]
	if sub == "" {
		return nil, fmt.Errorf("no kind of export called %q", r.Kind)
	}
	args := []string{"upload", sub,
		"--server=" + r.Server,
		"--no-ui",
		"--log-type=JSON",
		"--log-file=" + r.Log,
		// Pausing Immich's jobs needs an administrator's key, which
		// Roostack doesn't ask for: Immich carries on making thumbnails
		// as photos arrive (docs/IMMICH-IMPORT.md §6).
		"--pause-immich-jobs=false",
		// One unreadable file mustn't end a six-hour run: each error is
		// counted and named in the summary.
		"--on-errors=continue",
		"--concurrent-tasks=" + strconv.Itoa(max(1, min(r.Tasks, 20))),
		"--manage-burst=" + pick(r.Choices.Stacks, "Stack", "NoStack"),
		"--manage-raw-jpeg=" + pick(r.Choices.Stacks, "StackCoverRaw", "NoStack"),
		"--manage-heic-jpeg=" + pick(r.Choices.Stacks, "StackCoverHeic", "NoStack"),
	}
	if r.DryRun {
		args = append(args, "--dry-run")
	}
	switch r.Kind {
	case KindGoogle:
		if len(r.Parts) == 0 {
			return nil, fmt.Errorf("no zip files to read")
		}
		args = append(args,
			"--include-archived=true",
			// A photo whose sidecar is missing (or can't be matched) comes
			// with the date in the file rather than not at all: immich-go
			// leaves them out by default, which lost one in the VM test.
			"--include-unmatched=true",
			"--include-trashed="+strconv.FormatBool(r.Choices.Trash),
			"--include-partner="+strconv.FormatBool(r.Choices.Partner),
			"--include-untitled-albums="+strconv.FormatBool(r.Choices.Untitled),
			"--people-tag="+strconv.FormatBool(r.Choices.People),
			"--sync-albums=true",
			"--takeout-tag=true",
			"--")
		for _, p := range r.Parts {
			args = append(args, path.Join(r.Source, p))
		}
	case KindFolder:
		args = append(args,
			"--recursive=true",
			"--folder-as-album="+pick(r.Choices.FolderAlbums, "FOLDER", "NONE"),
			"--session-tag=true",
			"--", r.Source)
	}
	return args, nil
}

func pick(on bool, yes, no string) string {
	if on {
		return yes
	}
	return no
}
