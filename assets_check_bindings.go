//go:build bindings

package main

// During binding generation the frontend has not been built yet, so an empty
// asset FS is the expected state rather than a fault. See assets_check.go.
func requireAssets() {}
