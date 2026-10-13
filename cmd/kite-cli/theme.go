package main

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The palette: a dark, low-contrast base with a violet accent, green and red
// for gains and losses, and cyan for identifiers.
var (
	colorText      = lipgloss.Color("#E4E4E7")
	colorMuted     = lipgloss.Color("#9CA3AF")
	colorFaint     = lipgloss.Color("#5B6170")
	colorBorder    = lipgloss.Color("#3A3F4B")
	colorAccent    = lipgloss.Color("#7D56F4")
	colorKey       = lipgloss.Color("#F25D94")
	colorCyan      = lipgloss.Color("#7DCFFF")
	colorGreen     = lipgloss.Color("#73D9A0")
	colorRed       = lipgloss.Color("#FF7A85")
	colorYellow    = lipgloss.Color("#F5C76E")
	colorBlue      = lipgloss.Color("#82AAFF")
	colorSelection = lipgloss.Color("#2E2A4F")
	colorITM       = lipgloss.Color("#45412C")
	colorSpot      = lipgloss.Color("#16323F")
	colorCallBar   = lipgloss.Color("#1f4d2b")
	colorPutBar    = lipgloss.Color("#5c2323")
	colorInk       = lipgloss.Color("#16161E")
	// The statusline's bar, and its message segment a shade lighter.
	colorStatusBar = lipgloss.Color("#1E1F2B")
	colorStatusMsg = lipgloss.Color("#2B2D3D")
)

var (
	styleText   = lipgloss.NewStyle().Foreground(colorText)
	styleMuted  = lipgloss.NewStyle().Foreground(colorMuted)
	styleFaint  = lipgloss.NewStyle().Foreground(colorFaint)
	styleBold   = lipgloss.NewStyle().Foreground(colorText).Bold(true)
	styleCyan   = lipgloss.NewStyle().Foreground(colorCyan)
	styleGreen  = lipgloss.NewStyle().Foreground(colorGreen)
	styleRed    = lipgloss.NewStyle().Foreground(colorRed)
	styleYellow = lipgloss.NewStyle().Foreground(colorYellow)
	styleBlue   = lipgloss.NewStyle().Foreground(colorBlue)
	styleKey    = lipgloss.NewStyle().Foreground(colorKey).Bold(true)
	styleAccent = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	styleTitle  = lipgloss.NewStyle().Foreground(colorText).Bold(true)
	styleHeader = lipgloss.NewStyle().Foreground(colorMuted).Bold(true)
)

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
		return pill(side, colorRed, colorInk)
	}
	return pill(side, colorGreen, colorInk)
}

// help renders key bindings as "key description" pairs: the key in pink and
// the description muted.
func help(pairs ...string) string {
	var parts []string
	for index := 0; index+1 < len(pairs); index += 2 {
		parts = append(parts, styleKey.Render(pairs[index])+" "+styleMuted.Render(pairs[index+1]))
	}
	return strings.Join(parts, styleFaint.Render(" · "))
}

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
