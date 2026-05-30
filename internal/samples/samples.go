// Package samples holds the appendix vendor webhook payloads as embedded JSON
// fixtures, so tests exercise real-world examples instead of inlining large string
// literals. Importable by any test in the module.
package samples

import (
	"embed"
	"io/fs"
)

//go:embed *.json
var fsys embed.FS

// Read returns the named fixture, e.g. Read("maersk_in_transit.json").
func Read(name string) ([]byte, error) {
	return fsys.ReadFile(name)
}

// Names lists the available fixture filenames.
func Names() []string {
	names, _ := fs.Glob(fsys, "*.json")
	return names
}
