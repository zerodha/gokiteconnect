package main

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// A palette is the theme's colours. The dark one is a low-contrast base with a
// violet accent, green and red for gains and losses, and cyan for identifiers;
// the light one keeps those hues, deepened to read on a light background, and
// lightens the surfaces so dark text stays legible on them.
type palette struct {
	text, muted, faint, border            color.Color
	accent, key, cyan, green, red, yellow color.Color
	blue                                  color.Color
	selection, idleSelection              color.Color
	itm, spot, atm, callBar, putBar       color.Color
	ink, onAccent, onPill                 color.Color
	statusBar, statusMsg                  color.Color
	field, blurredField, button, dim      color.Color
}

var darkPalette = palette{
	text: lipgloss.Color("#E4E4E7"), muted: lipgloss.Color("#9CA3AF"), faint: lipgloss.Color("#5B6170"), border: lipgloss.Color("#3A3F4B"),
	accent: lipgloss.Color("#7D56F4"), key: lipgloss.Color("#F25D94"), cyan: lipgloss.Color("#7DCFFF"), green: lipgloss.Color("#73D9A0"),
	red: lipgloss.Color("#FF7A85"), yellow: lipgloss.Color("#F5C76E"), blue: lipgloss.Color("#82AAFF"),
	selection: lipgloss.Color("#2E2A4F"), idleSelection: lipgloss.Color("#23232E"),
	itm: lipgloss.Color("#45412C"), spot: lipgloss.Color("#16323F"), atm: lipgloss.Color("#B8860B"),
	callBar: lipgloss.Color("#1f4d2b"), putBar: lipgloss.Color("#5c2323"),
	ink: lipgloss.Color("#16161E"), onAccent: lipgloss.Color("#FFFFFF"), onPill: lipgloss.Color("#16161E"),
	statusBar: lipgloss.Color("#1E1F2B"), statusMsg: lipgloss.Color("#2B2D3D"),
	field: lipgloss.Color("#23232E"), blurredField: lipgloss.Color("#1C1C24"), button: lipgloss.Color("#2A2D37"), dim: lipgloss.Color("#3B3F4A"),
}

var lightPalette = palette{
	text: lipgloss.Color("#1F2329"), muted: lipgloss.Color("#555B68"), faint: lipgloss.Color("#8A8F9C"), border: lipgloss.Color("#C3C7D1"),
	accent: lipgloss.Color("#6B46E5"), key: lipgloss.Color("#D6336C"), cyan: lipgloss.Color("#0A76A0"), green: lipgloss.Color("#13834A"),
	red: lipgloss.Color("#CF2F3F"), yellow: lipgloss.Color("#986500"), blue: lipgloss.Color("#2F5BCC"),
	selection: lipgloss.Color("#E4DCFF"), idleSelection: lipgloss.Color("#ECECF1"),
	itm: lipgloss.Color("#FBF0C8"), spot: lipgloss.Color("#D9EFF8"), atm: lipgloss.Color("#E8B23A"),
	callBar: lipgloss.Color("#BDE8CC"), putBar: lipgloss.Color("#F6C6CA"),
	ink: lipgloss.Color("#16161E"), onAccent: lipgloss.Color("#FFFFFF"), onPill: lipgloss.Color("#FFFFFF"),
	statusBar: lipgloss.Color("#ECECF1"), statusMsg: lipgloss.Color("#DCDCE6"),
	field: lipgloss.Color("#E8E8EE"), blurredField: lipgloss.Color("#F2F2F5"), button: lipgloss.Color("#E2E3EA"), dim: lipgloss.Color("#BFC3CC"),
}

// The current palette's colours and styles, set by usePalette.
var (
	colorText, colorMuted, colorFaint, colorBorder           color.Color
	colorAccent, colorKey, colorCyan, colorGreen, colorRed   color.Color
	colorYellow, colorBlue                                   color.Color
	colorSelection, colorIdleSelection                       color.Color
	colorITM, colorSpot, colorATM, colorCallBar, colorPutBar color.Color
	colorInk                                                 color.Color
	colorOnAccent, colorOnPill                               color.Color
	colorStatusBar, colorStatusMsg                           color.Color
	colorField, colorBlurredField, colorButton, colorDim     color.Color

	styleText, styleMuted, styleFaint, styleBold            lipgloss.Style
	styleCyan, styleGreen, styleRed, styleYellow, styleBlue lipgloss.Style
	styleKey, styleAccent, styleTitle, styleHeader          lipgloss.Style
	styleATM, styleNoTrade                                  lipgloss.Style
)

func init() { usePalette(darkPalette) }

// choosePalette picks the palette the -theme flag names; auto asks dark
// whether the terminal's background is dark.
func choosePalette(theme string, dark func() bool) (palette, error) {
	switch strings.ToLower(strings.TrimSpace(theme)) {
	case "", "auto":
		if !dark() {
			return lightPalette, nil
		}
		return darkPalette, nil
	case "dark":
		return darkPalette, nil
	case "light":
		return lightPalette, nil
	}
	return palette{}, fmt.Errorf("unknown theme %q: use auto, light or dark", theme)
}

// usePalette switches the theme. Rows and inputs keep the styles they were
// built with, so it runs before the program starts.
func usePalette(p palette) {
	colorText, colorMuted, colorFaint, colorBorder = p.text, p.muted, p.faint, p.border
	colorAccent, colorKey, colorCyan, colorGreen, colorRed = p.accent, p.key, p.cyan, p.green, p.red
	colorYellow, colorBlue = p.yellow, p.blue
	colorSelection, colorIdleSelection = p.selection, p.idleSelection
	colorITM, colorSpot, colorATM, colorCallBar, colorPutBar = p.itm, p.spot, p.atm, p.callBar, p.putBar
	colorInk, colorOnAccent, colorOnPill = p.ink, p.onAccent, p.onPill
	colorStatusBar, colorStatusMsg = p.statusBar, p.statusMsg
	colorField, colorBlurredField, colorButton, colorDim = p.field, p.blurredField, p.button, p.dim

	styleText = lipgloss.NewStyle().Foreground(colorText)
	styleMuted = lipgloss.NewStyle().Foreground(colorMuted)
	styleFaint = lipgloss.NewStyle().Foreground(colorFaint)
	styleBold = lipgloss.NewStyle().Foreground(colorText).Bold(true)
	styleCyan = lipgloss.NewStyle().Foreground(colorCyan)
	styleGreen = lipgloss.NewStyle().Foreground(colorGreen)
	styleRed = lipgloss.NewStyle().Foreground(colorRed)
	styleYellow = lipgloss.NewStyle().Foreground(colorYellow)
	styleBlue = lipgloss.NewStyle().Foreground(colorBlue)
	styleKey = lipgloss.NewStyle().Foreground(colorKey).Bold(true)
	styleAccent = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleTitle = lipgloss.NewStyle().Foreground(colorText).Bold(true)
	styleHeader = lipgloss.NewStyle().Foreground(colorMuted).Bold(true)
	styleATM = lipgloss.NewStyle().Background(colorATM).Foreground(colorInk).Bold(true)
	styleNoTrade = lipgloss.NewStyle().Foreground(colorFaint)
}

// signStyle colours a value by its sign: green above zero, red below.
func signStyle(sign int) lipgloss.Style {
	switch {
	case sign > 0:
		return styleGreen
	case sign < 0:
		return styleRed
	}
	return styleMuted
}

func signed(value float64) string {
	return signStyle(sign(value)).Render(formatSigned(value))
}

func quantityStyle(quantity int) lipgloss.Style {
	switch {
	case quantity > 0:
		return styleGreen
	case quantity < 0:
		return styleRed
	}
	return styleFaint
}

func instrumentTypeStyle(instrumentType string) lipgloss.Style {
	switch instrumentType {
	case "CE":
		return styleGreen
	case "PE":
		return styleRed
	case "FUT":
		return styleYellow
	}
	return styleBlue
}

// pill renders text on a coloured background, for modes and badges.
func pill(text string, background, foreground color.Color) string {
	return lipgloss.NewStyle().Background(background).Foreground(foreground).Bold(true).Padding(0, 1).Render(text)
}

func sidePill(side string) string {
	if side == "SELL" {
		return pill(side, colorRed, colorOnPill)
	}
	return pill(side, colorGreen, colorOnPill)
}

// bindings lists key bindings as "key", "description" pairs for the keys
// modal.
func bindings(pairs ...string) []string { return pairs }

// Status messages carry their own colour.
func statusInfo(text string) string { return styleMuted.Render(text) }
func statusOK(text string) string   { return styleGreen.Render("✔ ") + styleText.Render(text) }
func statusWarn(text string) string { return styleYellow.Render(text) }
func statusErr(label, text string) string {
	return lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render(label) + " " + styleRed.Render(text)
}

// withBackground paints bg behind text of w cells, keeping its styled
// spans: each reset inside it restores bg, so the bar has no gaps.
func withBackground(text string, bg color.Color, w int) string {
	paint := lipgloss.NewStyle().Background(bg).Render(" ")
	open, _, _ := strings.Cut(paint, " ")
	if open == "" {
		return text + strings.Repeat(" ", max(w-ansi.StringWidth(text), 0))
	}
	for _, reset := range []string{"\x1b[m", "\x1b[0m"} {
		text = strings.ReplaceAll(text, reset, reset+open)
	}
	return open + text + strings.Repeat(" ", max(w-ansi.StringWidth(text), 0)) + "\x1b[m"
}
