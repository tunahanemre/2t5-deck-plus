// Package docs embeds the one dependency list the editor, the README and the
// website all share (dependencies.json). Edit the JSON, nothing else.
package docs

import _ "embed"

// DependenciesJSON is docs/dependencies.json, byte for byte.
//
//go:embed dependencies.json
var DependenciesJSON []byte
