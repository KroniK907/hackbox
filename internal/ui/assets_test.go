package ui_test

import (
	"strings"
	"testing"

	"github.com/KroniK907/hackbox/internal/ui"
)

func TestStaticPathBustsCache(t *testing.T) {
	t.Parallel()
	got := ui.StaticPath("live.css")
	if got != "/static/live.css?v="+ui.AssetVersion {
		t.Fatalf("StaticPath = %q", got)
	}
	if !strings.Contains(got, "?v=") {
		t.Fatal("static path missing version query")
	}
}
