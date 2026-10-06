package auth

import (
	"strings"
	"testing"
)

// The guarantee a device session rests on: whatever the person's role, a token held by a phone is refused
// every action the matrix marks UserOnly -- members, invitations, license and API and browser keys, source
// maps, the fleet, backend updates, query limits, disk space, support access and own-account management.
//
// Written against the matrix rather than a list of endpoints so that a UserOnly action added later is
// covered the day it is added, which is exactly when nobody would think to extend a list of URLs.
func TestADeviceSessionIsRefusedEveryUserOnlyAction(t *testing.T) {
	dev := &Principal{Kind: KindDevice, UserID: "u1", Email: "owner@example.com",
		OrgID: "o1", TenantID: "t1", Role: RoleOwner}
	browser := &Principal{Kind: KindSession, UserID: "u1", Email: "owner@example.com",
		OrgID: "o1", TenantID: "t1", Role: RoleOwner}

	var checked int
	for action, perm := range matrix {
		if !perm.UserOnly {
			continue
		}
		checked++
		if err := Allow(dev, perm); err == nil {
			t.Errorf("%s: a device session was allowed a UserOnly action", action)
		} else if !strings.Contains(err.Error(), "device session") {
			t.Errorf("%s: refused with %q, which does not tell the caller a device cannot do this", action, err)
		}
		// The same person in a browser must still be able to: the restriction is on the envelope, not the user.
		if err := Allow(browser, perm); err != nil {
			t.Errorf("%s: the owner in a browser was refused: %v", action, err)
		}
	}
	if checked == 0 {
		t.Fatal("no UserOnly actions found: the matrix moved and this test now proves nothing")
	}
}

// A device is not a lesser role, it is a different envelope: everything its user's role allows that is not
// UserOnly stays allowed, or the mobile console could not read the telemetry it exists to show.
func TestADeviceSessionKeepsTheRoleItsUserHas(t *testing.T) {
	for action, perm := range matrix {
		if perm.UserOnly {
			continue
		}
		dev := &Principal{Kind: KindDevice, UserID: "u1", OrgID: "o1", TenantID: "t1", Role: RoleOwner}
		if err := Allow(dev, perm); err != nil {
			t.Errorf("%s: an owner's device was refused a non-UserOnly action: %v", action, err)
		}
		// And it is still bounded by that role: a viewer's phone is a viewer.
		if perm.Min == RoleOwner {
			viewer := &Principal{Kind: KindDevice, UserID: "u2", OrgID: "o1", TenantID: "t1", Role: RoleViewer}
			if err := Allow(viewer, perm); err == nil {
				t.Errorf("%s: a viewer's device was allowed an owner action", action)
			}
		}
	}
}
