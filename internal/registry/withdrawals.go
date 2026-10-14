package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// severity orders withdrawal levels: none, notice, warning, stop.
func (w *Withdrawal) severity() int {
	if w == nil {
		return 0
	}
	switch w.Level {
	case LevelNotice:
		return 1
	case LevelWarning:
		return 2
	}
	return 3
}

// Withdrawn is the registry's withdrawal of a version a lab has installed
// (SPEC.md §8.3.4): the version's own or the whole package's, whichever
// is more severe; nil when neither is withdrawn. file is the lab's copy of
// the package file (SPEC.md §8.3.1), and only counts if it's the file the
// index names: a package of your own that shares a registry package's
// name and version isn't the registry's to withdraw.
func (ix *Index) Withdrawn(name, version string, file []byte) *Withdrawal {
	if ix == nil {
		return nil
	}
	p := ix.Packages[name]
	if p == nil {
		return nil
	}
	v := p.Versions[version]
	if v == nil || sum(file) != v.SHA256 {
		return nil
	}
	if p.Withdrawn.severity() > v.Withdrawn.severity() {
		return p.Withdrawn
	}
	return v.Withdrawn
}

// Index is the newest index the lab has, as the last fetch wrote it (nil
// before the first). It was checked against its signature when it was
// fetched; this reads the controller's own copy, so it's parsed and
// checked for sense, not verified again.
func (f *Fetcher) Index() (*Index, error) {
	path := filepath.Join(f.Dir, IndexFile)
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f.ixMu.Lock()
	defer f.ixMu.Unlock()
	if f.ix != nil && fi.ModTime().Equal(f.ixTime) && fi.Size() == f.ixSize {
		return f.ix, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ix Index
	if err := json.Unmarshal(data, &ix); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := ix.Check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	f.ix, f.ixTime, f.ixSize = &ix, fi.ModTime(), fi.Size()
	return f.ix, nil
}

// indexCache is the parsed index, kept while the file is unchanged.
type indexCache struct {
	ix     *Index
	ixTime time.Time
	ixSize int64
}

// Updates says whether the registry's versions of a package can update a
// service running this version, whose package file is file (SPEC.md §7):
// the registry has the package, and the version is either its own (the
// same SHA-256) or no longer in the index. A package of your own that
// shares a registry version's name and version, with other contents,
// isn't the registry's to update.
func (ix *Index) Updates(name, version string, file []byte) bool {
	if ix == nil || ix.Packages[name] == nil {
		return false
	}
	v := ix.Packages[name].Versions[version]
	return v == nil || sum(file) == v.SHA256
}
