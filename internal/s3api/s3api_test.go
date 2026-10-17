package s3api_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"roostack.cloud/roostack/internal/s3api"
	"roostack.cloud/roostack/internal/s3api/s3fake"
)

func client(t *testing.T, b *s3fake.Bucket) *s3api.Client {
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	return &s3api.Client{Endpoint: srv.URL, Bucket: b.Name, AccessKey: "id", SecretKey: "secret"}
}

// A probe shows a key that can't erase old versions: deleting by name
// hides, erasing a version is refused, and copying an old version back
// brings it back.
func TestVersions(t *testing.T) {
	b := &s3fake.Bucket{Name: "backups", Versioning: "Enabled", NoErase: true, PageSize: 2}
	c := client(t, b)
	ctx := context.Background()
	if v, err := c.Versioning(ctx); err != nil || v != "Enabled" {
		t.Fatalf("versioning = %q, %v", v, err)
	}
	// Names are escaped the way signatures want them.
	const key = "lab/a file+(1).txt"
	first, err := c.Put(ctx, key, []byte("one"))
	if err != nil || first == "" {
		t.Fatalf("put = %q, %v", first, err)
	}
	c.Put(ctx, key, []byte("two"))
	if err := c.Delete(ctx, key, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Current(key); ok {
		t.Error("deleting by name hides")
	}
	if err := c.Delete(ctx, key, first); !errors.Is(err, s3api.ErrAccessDenied) {
		t.Errorf("erasing a version: %v, want access denied", err)
	}
	if err := c.CopyBack(ctx, key, first); err != nil {
		t.Fatal(err)
	}
	if data, ok := b.Current(key); !ok || string(data) != "one" {
		t.Errorf("after copying back: %q, %t", data, ok)
	}
	// Listed newest first, across pages (two a page).
	c.Put(ctx, "lab/other", []byte("x"))
	c.Put(ctx, "elsewhere", []byte("y"))
	var got []string
	err = c.Versions(ctx, "lab/", func(v s3api.Version) error {
		kind := "v"
		if v.DeleteMarker {
			kind = "m"
		}
		if v.Latest {
			kind += "*"
		}
		got = append(got, v.Key+" "+kind)
		return nil
	})
	want := []string{key + " v*", key + " m", key + " v", key + " v", "lab/other v*"}
	if err != nil || len(got) != len(want) {
		t.Fatalf("versions = %v, %v", got, err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("version %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// The shortest enabled rule for old versions that reaches the folder
// counts; none keeps them for good.
func TestKeepsOld(t *testing.T) {
	b := &s3fake.Bucket{Name: "backups", Versioning: "Enabled"}
	c := client(t, b)
	ctx := context.Background()
	if d, err := c.KeepsOld(ctx, "roostack/lab/"); err != nil || d != 0 {
		t.Errorf("no rules: %d, %v", d, err)
	}
	b.Lifecycle = `<LifecycleConfiguration>
	  <Rule><Status>Enabled</Status><Filter><Prefix>roostack/</Prefix></Filter>
	    <NoncurrentVersionExpiration><NoncurrentDays>45</NoncurrentDays></NoncurrentVersionExpiration></Rule>
	  <Rule><Status>Enabled</Status><Filter><Prefix>photos/</Prefix></Filter>
	    <NoncurrentVersionExpiration><NoncurrentDays>1</NoncurrentDays></NoncurrentVersionExpiration></Rule>
	  <Rule><Status>Disabled</Status><Filter></Filter>
	    <NoncurrentVersionExpiration><NoncurrentDays>2</NoncurrentDays></NoncurrentVersionExpiration></Rule>
	  <Rule><Status>Enabled</Status><Filter><Tag><Key>t</Key><Value>v</Value></Tag></Filter>
	    <NoncurrentVersionExpiration><NoncurrentDays>3</NoncurrentDays></NoncurrentVersionExpiration></Rule>
	  <Rule><Status>Enabled</Status><Filter><Prefix></Prefix></Filter>
	    <AbortIncompleteMultipartUpload><DaysAfterInitiation>7</DaysAfterInitiation></AbortIncompleteMultipartUpload></Rule>
	</LifecycleConfiguration>`
	if d, err := c.KeepsOld(ctx, "roostack/lab/"); err != nil || d != 45 {
		t.Errorf("rules: %d, %v; want 45", d, err)
	}
	b.Lifecycle = `<LifecycleConfiguration><Rule><Status>Enabled</Status><Prefix></Prefix>
	  <NoncurrentVersionExpiration><NoncurrentDays>7</NoncurrentDays></NoncurrentVersionExpiration></Rule></LifecycleConfiguration>`
	if d, _ := c.KeepsOld(ctx, "roostack/lab/"); d != 7 {
		t.Errorf("a rule for the whole bucket (old form): %d, want 7", d)
	}
	b.NoLifecycle = true
	if _, err := c.KeepsOld(ctx, "roostack/lab/"); !errors.Is(err, s3api.ErrAccessDenied) {
		t.Errorf("not allowed to read: %v", err)
	}
}
