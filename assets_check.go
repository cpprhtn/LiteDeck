//go:build !bindings

package main

import "log"

// requireAssets refuses to start a binary with no frontend in it.
//
// frontend/dist is build output and is not committed — only a .gitkeep is, so
// that the embed pattern in main.go still resolves in a fresh clone. The cost of
// that placeholder is that `go build` now succeeds against an empty asset FS and
// produces a binary whose window comes up blank with nothing on screen to
// explain why. That is the failure mode this project treats as a bug, so it is
// reported instead.
//
// The !bindings tag matters: `wails build` generates its JS bindings by
// compiling the app with -tags bindings and running the result, and it does that
// *before* building the frontend — so at that moment dist legitimately holds
// nothing but the placeholder. Without the tag this check kills every build.
func requireAssets() {
	if _, err := assets.Open("frontend/dist/index.html"); err != nil {
		log.Fatal("litedeck: 프론트엔드 애셋이 바이너리에 없습니다. " +
			"`go build` 는 frontend/dist 를 만들지 않습니다 — `wails build` 또는 `wails dev` 를 쓰세요.")
	}
}
