package admin

import (
	"fmt"

	"github.com/bboxsys/platform/user"
)

// validateRoles rejects any role name that is not a canonical role.
func validateRoles(roles []string) error {
	for _, r := range roles {
		if !user.IsValidRole(r) {
			return fmt.Errorf("invalid role %q; valid roles are %v", r, user.AllRoles())
		}
	}
	return nil
}
