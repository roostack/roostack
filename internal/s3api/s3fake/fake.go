// Package s3fake is an in-memory S3 bucket that keeps versions, for tests
// of s3api and what uses it: puts, deletes (a delete marker, or erasing a
// version unless the key may not), copies of old versions, listing
// versions a page at a time, and the bucket's versioning and lifecycle
// answers. It doesn't check signatures.
package s3fake

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Bucket is one bucket.
type Bucket struct {
	mu sync.Mutex
	// Name is the bucket's name (in the path).
	Name string
	// Versioning is GetBucketVersioning's status ("Enabled", or "").
	Versioning string
	// Lifecycle is GetBucketLifecycleConfiguration's answer ("" for none).
	Lifecycle string
	// NoErase refuses deleting a version (AccessDenied), as a key without
	// s3:DeleteObjectVersion; NoLifecycle refuses reading the lifecycle.
	NoErase, NoLifecycle bool
	// PageSize is how many versions a listing returns at most (1000 if 0).
	PageSize int
	// MarkersFirst lists a page's delete markers before its versions, as
	// Backblaze does (docs/ASSUMPTIONS.md N9), rather than each object's
	// together as AWS does.
	MarkersFirst bool
	// Now is the time things happen (time.Now if nil).
	Now func() time.Time

	objects map[string][]*Version // newest first
	seq     int
	// Calls are the requests made, "METHOD key?query".
	Calls []string
}

// Version is one version of an object, or a delete marker.
type Version struct {
	ID       string
	Marker   bool
	Data     []byte
	Modified time.Time
}

func (b *Bucket) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// Put stores an object as if a client had (a new version).
func (b *Bucket) Put(key string, data []byte) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.add(key, &Version{Data: data})
}

// Hide adds a delete marker, as a client deleting by name would.
func (b *Bucket) Hide(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.add(key, &Version{Marker: true})
}

func (b *Bucket) add(key string, v *Version) string {
	if b.objects == nil {
		b.objects = map[string][]*Version{}
	}
	b.seq++
	v.ID, v.Modified = fmt.Sprintf("v%04d", b.seq), b.now().UTC().Truncate(time.Millisecond)
	if b.Versioning != "Enabled" {
		b.objects[key] = nil
	}
	b.objects[key] = append([]*Version{v}, b.objects[key]...)
	return v.ID
}

// Current is an object's newest content, and false if it's hidden or
// was never there.
func (b *Bucket) Current(key string) ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	vs := b.objects[key]
	if len(vs) == 0 || vs[0].Marker {
		return nil, false
	}
	return vs[0].Data, true
}

// Versions is how many versions (and markers) an object has.
func (b *Bucket) Versions(key string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.objects[key])
}

// ServeHTTP answers path-style requests for this bucket.
func (b *Bucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(path, "/")
	q := r.URL.Query()
	b.Calls = append(b.Calls, r.Method+" "+key+"?"+r.URL.RawQuery)
	if bucket != b.Name {
		fail(w, http.StatusNotFound, "NoSuchBucket")
		return
	}
	switch {
	case key == "" && q.Has("versioning"):
		reply(w, struct {
			XMLName xml.Name `xml:"VersioningConfiguration"`
			Status  string   `xml:"Status,omitempty"`
		}{Status: b.Versioning})
	case key == "" && q.Has("lifecycle"):
		switch {
		case b.NoLifecycle:
			fail(w, http.StatusForbidden, "AccessDenied")
		case b.Lifecycle == "":
			fail(w, http.StatusNotFound, "NoSuchLifecycleConfiguration")
		default:
			io.WriteString(w, b.Lifecycle)
		}
	case key == "" && q.Has("versions"):
		b.list(w, q)
	case r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source") != "":
		b.copy(w, key, r.Header.Get("X-Amz-Copy-Source"))
	case r.Method == http.MethodPut:
		data, _ := io.ReadAll(r.Body)
		id := b.add(key, &Version{Data: data})
		if b.Versioning == "Enabled" {
			w.Header().Set("X-Amz-Version-Id", id)
		}
	case r.Method == http.MethodDelete && q.Has("versionId"):
		if b.NoErase {
			fail(w, http.StatusForbidden, "AccessDenied")
			return
		}
		vs := b.objects[key]
		i := slices.IndexFunc(vs, func(v *Version) bool { return v.ID == q.Get("versionId") })
		if i < 0 {
			fail(w, http.StatusNotFound, "NoSuchVersion")
			return
		}
		b.objects[key] = slices.Delete(vs, i, i+1)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete:
		id := b.add(key, &Version{Marker: true})
		w.Header().Set("X-Amz-Version-Id", id)
		w.WriteHeader(http.StatusNoContent)
	default:
		fail(w, http.StatusNotImplemented, "NotImplemented")
	}
}

// copy makes an old version the newest (x-amz-copy-source with a
// versionId).
func (b *Bucket) copy(w http.ResponseWriter, key, source string) {
	src, err := url.PathUnescape(strings.TrimPrefix(source, "/"))
	if err != nil {
		fail(w, http.StatusBadRequest, "InvalidArgument")
		return
	}
	path, query, _ := strings.Cut(src, "?")
	bucket, srcKey, _ := strings.Cut(path, "/")
	qv, _ := url.ParseQuery(query)
	if bucket != b.Name {
		fail(w, http.StatusNotFound, "NoSuchBucket")
		return
	}
	for _, v := range b.objects[srcKey] {
		if v.ID == qv.Get("versionId") && !v.Marker {
			id := b.add(key, &Version{Data: v.Data})
			w.Header().Set("X-Amz-Version-Id", id)
			io.WriteString(w, "<CopyObjectResult></CopyObjectResult>")
			return
		}
	}
	fail(w, http.StatusNotFound, "NoSuchVersion")
}

// list answers ListObjectVersions, PageSize at a time.
func (b *Bucket) list(w http.ResponseWriter, q url.Values) {
	type item struct {
		XMLName      xml.Name
		Key          string `xml:"Key"`
		VersionID    string `xml:"VersionId"`
		IsLatest     bool   `xml:"IsLatest"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag,omitempty"`
		Size         int    `xml:"Size,omitempty"`
	}
	var all []item
	keys := make([]string, 0, len(b.objects))
	for k := range b.objects {
		if strings.HasPrefix(k, q.Get("prefix")) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		for i, v := range b.objects[k] {
			it := item{XMLName: xml.Name{Local: "Version"}, Key: k, VersionID: v.ID, IsLatest: i == 0,
				LastModified: v.Modified.Format("2006-01-02T15:04:05.000Z")}
			if v.Marker {
				it.XMLName.Local = "DeleteMarker"
			} else {
				sum := md5.Sum(v.Data)
				it.ETag, it.Size = `"`+hex.EncodeToString(sum[:])+`"`, len(v.Data)
			}
			all = append(all, it)
		}
	}
	// Start after the marker.
	if km := q.Get("key-marker"); km != "" {
		i := slices.IndexFunc(all, func(it item) bool { return it.Key == km && it.VersionID == q.Get("version-id-marker") })
		all = all[i+1:]
	}
	size := b.PageSize
	if n, _ := strconv.Atoi(q.Get("max-keys")); size == 0 || n > 0 && n < size {
		size = max(n, 1)
	}
	out := struct {
		XMLName       xml.Name `xml:"ListVersionsResult"`
		Name          string   `xml:"Name"`
		Truncated     bool     `xml:"IsTruncated"`
		NextKey       string   `xml:"NextKeyMarker,omitempty"`
		NextVersionID string   `xml:"NextVersionIdMarker,omitempty"`
		Items         []item
	}{Name: b.Name}
	if len(all) > size {
		all = all[:size]
		out.Truncated, out.NextKey, out.NextVersionID = true, all[size-1].Key, all[size-1].VersionID
	}
	if b.MarkersFirst {
		slices.SortStableFunc(all, func(a, b item) int {
			am, bm := a.XMLName.Local == "DeleteMarker", b.XMLName.Local == "DeleteMarker"
			switch {
			case am && !bm:
				return -1
			case bm && !am:
				return 1
			}
			return 0
		})
	}
	out.Items = all
	reply(w, out)
}

func reply(w http.ResponseWriter, v any) {
	data, _ := xml.Marshal(v)
	w.Write(data)
}

func fail(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message></Error>", code, code)
}
