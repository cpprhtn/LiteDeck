package app

// Version is the release this binary was built from.
//
// It lives in Go rather than being read from wails.json at runtime because
// wails.json is build-tool configuration and is not embedded in the binary —
// the running app cannot see it. That means the two can drift, so
// version_test.go asserts they agree instead of trusting a comment to be read.
//
// Bumping a release means editing this line and wails.json's info.productVersion
// together, then tagging v<Version>. The tag is the third corner and is checked
// too: release.yml refuses to build a tag that disagrees with this constant,
// because the version a binary reports is the one that reaches the bug report.
const Version = "1.6.0"
