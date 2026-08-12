package main

import (
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/options"
)

// The macOS application menu.
//
// It exists for one reason: on macOS, ⌘C and ⌘V are delivered by the Edit
// menu's key equivalents, not by the webview. An app with no menu has no Edit
// menu, and copy and paste are dead everywhere in the window — the file tree,
// the host editor, the Command Log. Nothing in the frontend can fix that from
// its side, because the keystroke never arrives.
//
// Windows and Linux need none of this: WebView2 and WebKitGTK bind Ctrl+C/V
// themselves, and handing them a menu would put a visible bar in the window
// that has nothing in it worth showing.
func appMenu() *menu.Menu {
	m := menu.NewMenu()
	// AppMenu carries About/Services/Hide/Quit, which is also where ⌘Q comes
	// from. WindowMenu restores ⌘M and ⌘W.
	m.Append(menu.AppMenu())
	m.Append(menu.EditMenu())
	m.Append(menu.WindowMenu())
	return m
}

func withMenu(o *options.App) { o.Menu = appMenu() }
