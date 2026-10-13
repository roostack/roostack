package auth

import "testing"

func TestRoles(t *testing.T) {
	cases := []struct {
		role string
		p    Permission
		want bool
	}{
		{RoleOwner, ManageUsers, true},
		{RoleOwner, ManageHost, true},
		{RoleAdmin, ManageServices, true},
		{RoleAdmin, ManageHost, false},
		{RoleAdmin, ManageUsers, false},
		{RoleMember, ViewDashboard, true},
		{RoleMember, ManageServices, false},
		{"bogus", ViewDashboard, false},
	}
	for _, c := range cases {
		if got := Can(c.role, c.p); got != c.want {
			t.Errorf("Can(%s, %d) = %v, want %v", c.role, c.p, got, c.want)
		}
	}
}
