package user

// Formal role constants (singular form). New code should always use these.
const (
	RoleAdministrator = "administrator"
	RoleUser          = "user"
	RoleViewer        = "viewer"
)

// legacyRoleMap maps legacy plural role names to their canonical singular form.
var legacyRoleMap = map[string]string{
	"administrators": RoleAdministrator,
	"users":          RoleUser,
	"viewers":        RoleViewer,
}

// AllRoles returns the canonical role names.
func AllRoles() []string {
	return []string{RoleAdministrator, RoleUser, RoleViewer}
}

// HasRole checks whether the given roles slice contains the target role.
// It accepts both canonical singular forms and legacy plural forms.
func HasRole(roles []string, target string) bool {
	for _, r := range roles {
		if r == target {
			return true
		}
		// Check if this is a legacy plural that maps to the target
		if canonical, ok := legacyRoleMap[r]; ok && canonical == target {
			return true
		}
	}
	return false
}

// IsValidRole returns true if the role is a known canonical role name.
func IsValidRole(role string) bool {
	for _, r := range AllRoles() {
		if r == role {
			return true
		}
	}
	return false
}
