package frontend

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed index.html app.js style.css expiry.js
var assets embed.FS

// Handler serves the UI from the binary, independent of the working directory.
func Handler() http.Handler {
	files, err := fs.Sub(assets, ".")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/ui/", http.FileServer(http.FS(files)))
}
