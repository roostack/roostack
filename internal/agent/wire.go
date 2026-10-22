package agent

import (
	"fmt"
	"net/http"
)

// The agent's API as both sides see it: each call's request and answer,
// which the client sends and the server decodes. The daemon and the agent
// are installed together, from one package, but an upgrade replaces and
// restarts them one at a time, so for a moment an old daemon can call a new
// agent: the JSON names stay as they are.
//
// Values a call takes from its URL (a service's or a library's name) are
// fields the JSON leaves out: the server fills them in from the path
// (fromURL), and Validate checks every request before the host sees it.

// Answer is a call's answer when it has nothing else to say: ok, or the
// error.
type Answer struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Health is GET /v1/health's answer.
type Health struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
}

// TimezoneRequest sets the host's time zone (PUT /v1/timezone).
type TimezoneRequest struct {
	Timezone string `json:"timezone"`
}

func (r TimezoneRequest) Validate() error {
	if !ValidTimezone(r.Timezone) {
		return fmt.Errorf("invalid time zone %q", r.Timezone)
	}
	return nil
}

func (r TimezoneRequest) logAttrs() []any { return []any{"timezone", r.Timezone} }

// UpdatesRequest runs OS updates (POST /v1/updates/run). Its body is
// optional: a Series also moves Roostack to that release series, once
// the Owner has accepted it (SPEC.md §7).
type UpdatesRequest struct {
	Series string `json:"series,omitempty"`
}

func (r UpdatesRequest) Validate() error {
	if r.Series != "" && !ValidSeries(r.Series) {
		return fmt.Errorf("invalid release series %q", r.Series)
	}
	return nil
}

// UpdatesMessage is one line of POST /v1/updates/run's answer, which is
// newline-delimited JSON: a Line of output as it arrives, then one that's
// Done, with the Error if the updates failed.
type UpdatesMessage struct {
	Line  *string `json:"line,omitempty"`
	Done  bool    `json:"done,omitempty"`
	Error string  `json:"error,omitempty"`
}

// InterfaceRequest names one of Roostack's WireGuard interfaces, in the
// URL's query (GET and DELETE /v1/wireguard): Roostack's network by
// default.
type InterfaceRequest struct {
	Interface string `json:"-"`
}

func (r *InterfaceRequest) fromURL(req *http.Request) {
	r.Interface = req.URL.Query().Get("interface")
	if r.Interface == "" {
		r.Interface = WireGuardInterface
	}
}

func (r InterfaceRequest) Validate() error {
	if !ValidInterface(r.Interface) {
		return fmt.Errorf("invalid interface %q", r.Interface)
	}
	return nil
}

func (r InterfaceRequest) logAttrs() []any { return []any{"interface", r.Interface} }

// WireGuardApplied is PUT /v1/wireguard's answer: the interface's public
// key.
type WireGuardApplied struct {
	OK        bool   `json:"ok"`
	PublicKey string `json:"public_key"`
}

func (wg WireGuard) logAttrs() []any {
	return []any{"interface", wg.Iface(), "address", wg.Address, "peers", len(wg.Peers)}
}

// UseDriveRequest readies a drive for backups (POST /v1/drives/use): one
// of the IDs Drives listed.
type UseDriveRequest struct {
	ID string `json:"id"`
}

func (r UseDriveRequest) logAttrs() []any { return []any{"id", r.ID} }

func (u UsedDrive) logAttrs() []any { return []any{"dir", u.Dir, "uuid", u.UUID} }

// EraseDriveRequest erases a disk for backups (POST /v1/drives/erase): one
// that Drives listed as Erasable.
type EraseDriveRequest struct {
	Disk  string `json:"disk"`
	Label string `json:"label"`
}

func (r EraseDriveRequest) logAttrs() []any { return []any{"disk", r.Disk, "label", r.Label} }

// mountLibrary is PUT /v1/libraries/{name}'s request: a Library, named by
// the URL.
type mountLibrary struct{ Library }

func (r *mountLibrary) fromURL(req *http.Request) { r.Name = req.PathValue("name") }

func (r mountLibrary) Validate() error {
	if !ValidLibraryName(r.Name) {
		return fmt.Errorf("%q can't name a library: lower-case letters, digits and -", printable(r.Name))
	}
	return nil
}

// logAttrs says where a library is, never a share's password.
func (l Library) logAttrs() []any {
	attrs := []any{"name", l.Name, "drive", l.Drive, "sub", l.Sub, "folder", l.Folder, "as_owner", l.AsOwner != nil}
	if l.Share != nil {
		attrs = append(attrs, "share", l.Share.Type+" "+l.Share.Server)
	}
	return attrs
}

// LibraryRequest names a media library in the URL (DELETE
// /v1/libraries/{name}).
type LibraryRequest struct {
	Name string `json:"-"`
}

func (r *LibraryRequest) fromURL(req *http.Request) { r.Name = req.PathValue("name") }

func (r LibraryRequest) Validate() error {
	if !ValidLibraryName(r.Name) {
		return fmt.Errorf("%q isn't a library's name", printable(r.Name))
	}
	return nil
}

func (r LibraryRequest) logAttrs() []any { return []any{"name", r.Name} }

// ServiceRequest names a service in the URL (DELETE
// /v1/library-views/{service} and /v1/imports/{service}).
type ServiceRequest struct {
	Service string `json:"-"`
}

func (r *ServiceRequest) fromURL(req *http.Request) { r.Service = req.PathValue("service") }

func (r ServiceRequest) Validate() error { return checkService(r.Service) }

func (r ServiceRequest) logAttrs() []any { return []any{"service", r.Service} }

// checkService refuses a service's name that isn't one (serviceName).
func checkService(service string) error {
	if !serviceName.MatchString(service) {
		return fmt.Errorf("%q isn't a service's name", printable(service))
	}
	return nil
}

// ViewsRequest sets the media libraries a service may read (PUT
// /v1/library-views/{service}), and those in its second view folder (At:
// an old folder's name → a library it reads, SPEC.md §8.4).
type ViewsRequest struct {
	Service   string            `json:"-"`
	Libraries []string          `json:"libraries"`
	At        map[string]string `json:"at,omitempty"`
}

func (r *ViewsRequest) fromURL(req *http.Request) { r.Service = req.PathValue("service") }

func (r ViewsRequest) Validate() error {
	if err := checkService(r.Service); err != nil {
		return err
	}
	reads := map[string]bool{}
	for _, l := range r.Libraries {
		if !ValidLibraryName(l) {
			return fmt.Errorf("%q isn't a library's name", printable(l))
		}
		reads[l] = true
	}
	for name, l := range r.At {
		if !atName.MatchString(name) {
			return fmt.Errorf("%q can't name a folder of libraries", printable(name))
		}
		if !reads[l] {
			return fmt.Errorf("%s doesn't read the library %q", r.Service, printable(l))
		}
	}
	return nil
}

func (r ViewsRequest) logAttrs() []any {
	return []any{"service", r.Service, "libraries", r.Libraries, "at", r.At}
}

// ShareLoginRequest keeps an SMB share's login for the library the URL
// names (PUT /v1/shares/{name}/login).
type ShareLoginRequest struct {
	Name string `json:"-"`
	Share
}

func (r *ShareLoginRequest) fromURL(req *http.Request) { r.Name = req.PathValue("name") }

func (r ShareLoginRequest) Validate() error {
	if !ValidLibraryName(r.Name) {
		return fmt.Errorf("%q can't name a library", printable(r.Name))
	}
	return nil
}

// logAttrs never has the password.
func (r ShareLoginRequest) logAttrs() []any {
	return []any{"library", r.Name, "server", r.Server, "username", r.Username}
}

// ImportRequest mounts the place to bring data into the service the URL
// names from (PUT /v1/imports/{service}, SPEC.md §8.4).
type ImportRequest struct {
	Service string `json:"-"`
	Library
}

func (r *ImportRequest) fromURL(req *http.Request) { r.Service = req.PathValue("service") }

func (r ImportRequest) Validate() error { return checkService(r.Service) }

func (r ImportRequest) logAttrs() []any {
	return append([]any{"service", r.Service}, r.Library.logAttrs()...)
}

// PhotosRequest names a person's export for migrating photos into a
// service, in the URL (PUT and DELETE /v1/photos/{service}/{who}, SPEC.md
// §8.5); PUT mounts the Library.
type PhotosRequest struct {
	Service string `json:"-"`
	Who     string `json:"-"`
	Library
}

func (r *PhotosRequest) fromURL(req *http.Request) {
	r.Service, r.Who = req.PathValue("service"), req.PathValue("who")
}

func (r PhotosRequest) Validate() error {
	if err := checkService(r.Service); err != nil {
		return err
	}
	if !whoName.MatchString(r.Who) {
		return fmt.Errorf("%q isn't an account's name", printable(r.Who))
	}
	return nil
}

func (r PhotosRequest) logAttrs() []any {
	attrs := []any{"service", r.Service, "who", r.Who}
	if r.Drive != "" || r.Folder != "" || r.Share != nil { // mounting, not forgetting
		attrs = append(attrs, r.Library.logAttrs()...)
	}
	return attrs
}

// Mounted is where a place to read from was mounted (PUT /v1/imports and
// /v1/photos).
type Mounted struct {
	Path string `json:"path"`
}

func (m Mounted) logAttrs() []any { return []any{"at", m.Path} }

// BackupFolder is a backup folder (POST /v1/backup-folder): the one to
// prepare, and in the answer, the roostack folder made in it.
type BackupFolder struct {
	Dir string `json:"dir"`
}

func (f BackupFolder) logAttrs() []any { return []any{"dir", f.Dir} }

// SSHRequest turns the SSH server's keys-only settings on or off (PUT
// /v1/ssh).
type SSHRequest struct {
	KeysOnly bool `json:"keys_only"`
}

func (r SSHRequest) logAttrs() []any { return []any{"keys_only", r.KeysOnly} }
