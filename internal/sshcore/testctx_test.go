package sshcore

import (
	"context"
	"testing"
)

// testCtx is testing.T.Context() from Go 1.24, written out so the module can
// keep its stated floor of Go 1.22. Lowering that floor matters: Ubuntu 22.04
// — a distribution this project explicitly supports as a *server* — ships Go
// 1.18, and contributors on stock toolchains should not need a toolchain
// download to build.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}
