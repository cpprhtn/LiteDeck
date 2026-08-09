package app

// Version is the release this binary was built from.
//
// It lives in Go rather than being read from wails.json at runtime because
// wails.json is build-tool configuration and is not embedded in the binary —
// the running app cannot see it. That means the two can drift, so
// version_test.go asserts they agree instead of trusting a comment to be read.
//
// Bumping a release means editing this line and wails.json's info.productVersion
// together, then tagging v<Version>.
const Version = "0.1.4-beta"
