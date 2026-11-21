//go:build adminui

package admin

import (
	"embed"
	"io/fs"
)

// Built with `go build -tags adminui` after `npm run build` in ui/.
//
//go:embed all:ui/dist
var uiDist embed.FS

// UIFS returns the embedded admin SPA, or nil when the binary was built
// without the ui tag.
func UIFS() fs.FS {
	sub, err := fs.Sub(uiDist, "ui/dist")
	if err != nil {
		return nil
	}
	return sub
}
