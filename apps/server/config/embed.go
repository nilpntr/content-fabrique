// Package configfiles embeds the versioned config of the server (config/routing and config/pricing), so a process finds
// it wherever it runs from: the worker's agent runtime reads the routing profiles and the price table from here.
package configfiles

import (
	"embed"
	"io/fs"
)

//go:embed routing/*.yaml pricing/*.yaml
var files embed.FS

// Routing is the directory of the routing profiles and the model catalogue (what llm.NewRouter takes).
func Routing() fs.FS { return sub("routing") }

// Pricing is the directory of the price tables (what llm.LoadPriceTable takes).
func Pricing() fs.FS { return sub("pricing") }

func sub(dir string) fs.FS {
	s, err := fs.Sub(files, dir)
	if err != nil {
		panic(err) // the directories are embedded above
	}
	return s
}
