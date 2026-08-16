package app

import (
	"encoding/base64"
	"path"
	"strings"
	"testing"
)

// What a file is gets decided by its bytes, not its name.
//
// Both directions matter. A screenshot somebody saved as `.log` should still
// draw, and — the direction that bites — a tarball named `.png` must never be
// handed to an <img> tag on the strength of four characters.

func TestSniffMIMEIgnoresTheFileName(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 13}
	gzip := []byte{0x1f, 0x8b, 0x08, 0, 0, 0, 0, 0, 0, 3}

	if got := sniffMIME(png); got != "image/png" {
		t.Errorf("PNG bytes sniffed as %q", got)
	}
	if got := sniffMIME(gzip); strings.HasPrefix(got, "image/") {
		t.Errorf("gzip bytes sniffed as an image (%q) — an <img> would be fed a tarball", got)
	}
}

// SVG is a document, not a picture: it can carry script, and this viewer shows
// files from a server the user may not control. It has to fall through to the
// inert hex path.
func TestSVGIsNotTreatedAsAnImage(t *testing.T) {
	svg := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg">` +
		`<script>alert(1)</script></svg>`)
	if got := sniffMIME(svg); strings.HasPrefix(got, "image/") {
		t.Errorf("SVG sniffed as %q; it would be rendered as markup", got)
	}
}

// Plain text reaching the preview means the editor turned it away for size. It
// must not claim to be an image either.
func TestTextIsNotAnImage(t *testing.T) {
	if got := sniffMIME([]byte("worker_processes 1;\n")); strings.HasPrefix(got, "image/") {
		t.Errorf("text sniffed as %q", got)
	}
}

// The preview against a real SFTP server (§4.2).
//
// The unit tests above cover the sniffing; this covers the part that reads
// actual bytes off a server and decides what crosses the boundary.
func TestPreviewFileOverSFTP(t *testing.T) {
	a := connectedApp(t)
	dir := scratchDir(t, a, "litedeck-preview")

	// A one-pixel PNG, byte for byte. Written through the terminal because the
	// text write path would mangle it.
	const b64png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	png := path.Join(dir, "dot.png")
	conn, err := a.mgr.Conn("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(testCtx(t), "sh", "-c",
		"printf %s '"+b64png+"' | base64 -d > "+png); err != nil {
		t.Skipf("could not write the fixture image: %v", err)
	}

	got, err := a.PreviewFile("fixture", png)
	if err != nil {
		t.Fatalf("PreviewFile: %v", err)
	}
	if got.Kind != "image" || got.MIME != "image/png" {
		t.Errorf("kind=%q mime=%q, want an image/png", got.Kind, got.MIME)
	}
	if got.Truncated {
		t.Error("an image small enough to load was reported as truncated")
	}
	if got.Data != b64png {
		t.Errorf("the bytes did not survive the round trip:\n got %s\nwant %s", got.Data, b64png)
	}

	// And a file the editor also refuses, but which is not a picture: it comes
	// back as a bounded hex-able prefix rather than nothing at all.
	blob := path.Join(dir, "blob.bin")
	if _, err := conn.Exec(testCtx(t), "sh", "-c",
		"head -c 100000 /dev/zero | tr '\\0' 'A' > "+blob+" && printf 'x\\0y' >> "+blob); err != nil {
		t.Skipf("could not write the fixture blob: %v", err)
	}
	bin, err := a.PreviewFile("fixture", blob)
	if err != nil {
		t.Fatalf("PreviewFile on a blob: %v", err)
	}
	if bin.Kind != "binary" {
		t.Errorf("kind = %q, want binary", bin.Kind)
	}
	if !bin.Truncated {
		t.Error("a 100 KB blob was not reported as truncated")
	}
	if raw, err := base64.StdEncoding.DecodeString(bin.Data); err != nil {
		t.Errorf("Data is not base64: %v", err)
	} else if len(raw) != hexPreviewBytes {
		t.Errorf("preview carried %d bytes, want %d", len(raw), hexPreviewBytes)
	}
	// The size on screen must be the file's, not the prefix's.
	if bin.Size < 100000 {
		t.Errorf("Size = %d, want the whole file's length", bin.Size)
	}
}
