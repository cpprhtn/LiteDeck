//go:build !darwin

package main

import "github.com/wailsapp/wails/v2/pkg/options"

// No menu off macOS: see menu_darwin.go for why one is needed there and not
// here. A menu set on Windows or Linux is a bar drawn in the window.
func withMenu(*options.App) {}
