package main

import (
	"fmt"
	"image/color"
	"strings"
	"testing"
)

func TestChoosePalette(t *testing.T) {
	for _, test := range []struct {
		theme string
		dark  bool
		want  palette
	}{
		{"auto", true, darkPalette},
		{"auto", false, lightPalette},
		{"", false, lightPalette},
		{"Light", true, lightPalette},
		{"dark", false, darkPalette},
	} {
		got, err := choosePalette(test.theme, func() bool { return test.dark })
		if err != nil || got != test.want {
			t.Errorf("choosePalette(%q, dark %v) = %v, %v", test.theme, test.dark, got.text, err)
		}
	}
	if _, err := choosePalette("solarized", func() bool { return true }); err == nil {
		t.Error("an unknown theme was accepted")
	}
}

// sgrColors are the escape parameters a palette's colours render as, in
// the foreground and the background, leaving out those both palettes share.
func sgrColors(p, except palette) []string {
	var colors []string
	shared := except.colors()
	for index, c := range p.colors() {
		if c == shared[index] {
			continue
		}
		r, g, b, _ := c.RGBA()
		rgb := fmt.Sprintf("2;%d;%d;%dm", r>>8, g>>8, b>>8)
		colors = append(colors, "38;"+rgb, "48;"+rgb)
	}
	return colors
}

func TestTheLightPaletteLeavesNoDarkColours(t *testing.T) {
	usePalette(lightPalette)
	t.Cleanup(func() { usePalette(darkPalette) })
	dark := sgrColors(darkPalette, lightPalette)
	check := func(h *harness, screen string) {
		t.Helper()
		rendered := h.a.render()
		for _, sgr := range dark {
			if strings.Contains(rendered, sgr) {
				t.Errorf("the %s screen renders the dark colour %q", screen, sgr)
			}
		}
	}

	store, quotes := niftyMarket()
	h := newHarness(t, explorerDeps(t, store, quotes, nil, &fakePlacer{}))
	h.waitFor("users", func() bool { return len(h.a.users.users) == 1 })
	check(h, "users")
	openExplorer(h)
	check(h, "instruments")

	deps, _, _ := basketDeps(t)
	h = newHarness(t, deps)
	h.waitFor("users", func() bool { return len(h.a.users.users) == 1 })
	h.run(h.a.openBaskets())
	h.press("n")
	check(h, "baskets")
}

func (p palette) colors() []color.Color {
	return []color.Color{
		p.text, p.muted, p.faint, p.border, p.accent, p.key, p.cyan, p.green, p.red, p.yellow, p.blue,
		p.selection, p.idleSelection, p.itm, p.spot, p.atm, p.callBar, p.putBar, p.ink, p.onAccent, p.onPill,
		p.statusBar, p.statusMsg, p.field, p.blurredField, p.button, p.dim,
	}
}
