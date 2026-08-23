// Command litedeck is the desktop client.
//
// main.go sits at the repository root rather than under cmd/ because Wails v2
// builds the module root — `wails build` has no option to point elsewhere, and
// giving that up would cost the asset embedding and the .app/.dmg/NSIS
// packaging that §9 depends on. The probe CLI still lives under cmd/.
package main

import (
	"embed"
	"log"

	"github.com/cpprhtn/LiteDeck/internal/app"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

// frontend/dist is build output and is not committed; only a .gitkeep is, so
// that this embed pattern still resolves in a fresh clone (without it `go vet`,
// `go build` and `go test ./...` all fail before doing anything).
//
//go:embed all:frontend/dist
var assets embed.FS

func main() {
	requireAssets()

	a := app.New()

	opts := &options.App{
		Title:  "LiteDeck",
		Width:  1280,
		Height: 820,

		// Roomy enough that the sidebar, a table and the Command Log can all be
		// on screen at once — the layout §1.6's scenario assumes.
		MinWidth:  900,
		MinHeight: 600,

		// Opens filling the screen. Width and Height above stay as the size the
		// window returns to when un-maximised, and as the fallback wherever the
		// platform cannot honour this. The monitoring grid is the reason: it
		// lays panels out in as many columns as it is given, so on a 1280 window
		// a machine's resources arrive stacked two-and-two when the display
		// could have shown them in one row.
		//
		// Maximised rather than Fullscreen: fullscreen on macOS takes over a
		// space and hides the menu bar, which is a thing to ask for and not a
		// thing to be given.
		WindowStartState: options.Maximised,

		AssetServer: &assetserver.Options{Assets: assets},

		// Dropping local files onto the file explorer uploads them (§4.2).
		// Wails hands back absolute paths rather than file contents, which is
		// what makes a multi-gigabyte drop cost nothing until the transfer
		// actually starts.
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop: true,
			// Without this, dropping a file anywhere else in the window makes
			// the webview navigate away from the app and show the file.
			DisableWebViewDrop: true,
		},
		OnStartup:  a.Startup,
		OnShutdown: a.Shutdown,
		Bind:       []any{a},

		// Native title bar for v1: frameless windows mean reimplementing drag,
		// snap and maximise per OS, and GNOME's client-side decorations make
		// that especially messy. Revisit once the rest is stable.
		Mac: &mac.Options{
			TitleBar: mac.TitleBarDefault(),
			About: &mac.AboutInfo{
				Title: "LiteDeck " + app.Version,
				// The version is in the title rather than only in the bundle
				// metadata so it is one ⌘-click away when someone is writing a
				// bug report, instead of requiring Finder → Get Info.
				Message: "SSH 하나로 원격 서버를 내 컴퓨터의 GUI에서 다룹니다.",
			},
		},
	}
	withMenu(opts)

	if err := wails.Run(opts); err != nil {
		log.Fatalf("litedeck: %v", err)
	}
}
