package sso

import (
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/generate"
)

// The generator and Caddy keep their own copies of the paths, groups and
// cookie (so they needn't import this package and its database): they
// must match.
func TestPathsMatch(t *testing.T) {
	for _, p := range [][2]string{
		{generate.SSOPathDiscovery, PathDiscovery}, {generate.SSOPathAuthorize, PathAuthorize},
		{generate.SSOPathToken, PathToken}, {generate.SSOPathUserInfo, PathUserInfo},
		{generate.SSOUserGroup, UserGroup}, {generate.SSOAdminGroup, AdminGroup},
		{caddy.WallCheckPath, PathCheck}, {caddy.WallCookie, HostCookie},
	} {
		if p[0] != p[1] {
			t.Errorf("%q != %q", p[0], p[1])
		}
	}
	if !strings.HasPrefix(PathCallback, caddy.WallCallbackPath+"/") {
		t.Errorf("callback %q isn't under %q", PathCallback, caddy.WallCallbackPath)
	}
}
