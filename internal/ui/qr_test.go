package ui_test

import (
	"strings"
	"testing"

	"github.com/KroniK907/hackbox/internal/ui"
)

func TestQRCodeSVGEncodesContent(t *testing.T) {
	t.Parallel()
	svg, err := ui.QRCodeSVG("http://192.168.10.24:8654/")
	if err != nil {
		t.Fatal(err)
	}
	markup := string(svg)
	if !strings.Contains(markup, "<svg") || !strings.Contains(markup, "http://192.168.10.24:8654/") {
		t.Fatalf("qr svg = %s", markup)
	}
	empty, err := ui.QRCodeSVG("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(empty), "<svg") {
		t.Fatalf("empty qr = %s", empty)
	}
}
