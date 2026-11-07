// Package fixtures embeds the test fixtures so that --self-test works
// from a built binary anywhere, not only from a repo checkout.
package fixtures

import "embed"

//go:embed all:basic all:malformed all:two all:chunked all:pi
var FS embed.FS
