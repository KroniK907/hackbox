package ui_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/KroniK907/hackbox/internal/ui"
)

func TestAvatarSVGIsDeterministicAndSeeded(t *testing.T) {
	t.Parallel()
	a := string(ui.AvatarSVG("seed-one"))
	b := string(ui.AvatarSVG("seed-one"))
	c := string(ui.AvatarSVG("seed-two"))
	if a != b {
		t.Fatal("same seed produced different SVG")
	}
	if a == c {
		t.Fatal("different seeds produced the same SVG")
	}
	if !strings.Contains(a, "<svg") || strings.Contains(a, "<img") {
		t.Fatalf("avatar is not inline SVG: %s", a)
	}
	if ui.SeedColor("seed-one") == ui.SeedColor("seed-two") {
		t.Fatal("different seeds produced the same token color")
	}
	if ui.FaceColor("seed-one") == ui.TokenColor("seed-one") {
		t.Fatal("face and token used the same fill")
	}
	if ui.HairColor("seed-one") == ui.FaceColor("seed-one") {
		t.Fatal("hair and face used the same fill")
	}
	if strings.Contains(a, "L54 12 Q32 0") {
		t.Fatal("avatar still draws the old forehead plate")
	}
	if !strings.Contains(a, `viewBox="2 2 60 60"`) {
		t.Fatal("avatar viewBox is not the slightly padded crop")
	}
	if !strings.Contains(a, `r="19"`) {
		t.Fatal("avatar face is not the slightly smaller head")
	}
}

func TestAvatarPaletteAndHairVary(t *testing.T) {
	t.Parallel()
	faces := map[string]struct{}{}
	hairs := map[string]struct{}{}
	svgs := map[string]struct{}{}
	for i := 0; i < 48; i++ {
		seed := fmt.Sprintf("face-%d", i)
		faces[ui.FaceColor(seed)] = struct{}{}
		hairs[ui.HairColor(seed)] = struct{}{}
		svgs[string(ui.AvatarSVG(seed))] = struct{}{}
	}
	if len(faces) < 8 {
		t.Fatalf("face colors = %d, want a wide palette", len(faces))
	}
	if len(hairs) < 8 {
		t.Fatalf("hair colors = %d, want a wide palette", len(hairs))
	}
	if len(svgs) < 20 {
		t.Fatalf("avatar shapes = %d, want more hair and face mixes", len(svgs))
	}
}
