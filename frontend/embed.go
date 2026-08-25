// Package frontend embeds the built web UI so a single binary can serve it.
//
// The embed lives here, not at the repo root, because a //go:embed directive
// can only reach files inside its own package directory — and cmd/litedeck-server
// is two levels up from frontend/dist. The desktop binary keeps its own root
// embed (main.go); this one is for the server binary. A committed dist/.gitkeep
// makes both resolve in a fresh clone before `npm run build` has run.
package frontend

import "embed"

//go:embed all:dist
var Assets embed.FS
