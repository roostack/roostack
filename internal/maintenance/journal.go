package maintenance

import (
	"encoding/json"
	"errors"
	"os"

	"roostack.cloud/roostack/internal/fsutil"
)

// FileJournal stores interrupted job names in a small JSON file, so they
// resume after the reboot that interrupted them.
type FileJournal struct{ Path string }

// Load returns the saved job names (none if the file doesn't exist).
func (j FileJournal) Load() ([]string, error) {
	data, err := os.ReadFile(j.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	return names, json.Unmarshal(data, &names)
}

// Save replaces the saved job names; an empty list removes the file.
func (j FileJournal) Save(names []string) error {
	if len(names) == 0 {
		err := os.Remove(j.Path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := json.Marshal(names)
	if err != nil {
		return err
	}
	return fsutil.WritePrivate(j.Path, data)
}
