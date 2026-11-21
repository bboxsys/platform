//go:build !adminui

package admin

import "io/fs"

// UIFS returns nil: this binary was built without the adminui tag, so the admin
// server exposes only the API.
func UIFS() fs.FS {
	return nil
}
