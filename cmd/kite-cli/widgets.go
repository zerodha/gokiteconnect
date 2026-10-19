package main

import (
	"image/color"
	"math"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// panel draws body in a rounded box of w×h cells with title set into the top
// border and info at its right. A focused panel's border takes the accent.
func panel(title, info, body string, w, h int, focused bool) string {
	if w < 4 || h < 2 {
		return ""
	}
	border := lipgloss.NewStyle().Foreground(colorBorder)
	titleStyle := styleHeader
	if focused {
		border = lipgloss.NewStyle().Foreground(colorAccent)
		titleStyle = styleAccent
	}
	inner := w - 2
	label := ""
	if title != "" {
		label = " " + titleStyle.Render(title) + " "
	}
	label = ansi.Truncate(label, max(inner-2, 0), "…")
	right := ""
	if info != "" {
		right = " " + info + " "
	}
	fill := inner - 1 - ansi.StringWidth(label) - ansi.StringWidth(right) - 1
	if fill < 1 {
		right = ""
		fill = inner - 1 - ansi.StringWidth(label) - 1
	}
	var out strings.Builder
	out.WriteString(border.Render("╭─") + label + border.Render(strings.Repeat("─", max(fill, 0))) + right + border.Render("─╮"))
	side := border.Render("│")
	for _, line := range fitLines(body, inner, h-2) {
		out.WriteString("\n" + side + line + side)
	}
	out.WriteString("\n" + border.Render("╰"+strings.Repeat("─", inner)+"╯"))
	return out.String()
}

// textPanel is a panel of word-wrapped text with a column of padding.
func textPanel(title, info, text string, w, h int, focused bool) string {
	wrapped := ansi.Wordwrap(text, max(w-4, 1), "")
	wrapped = ansi.Hardwrap(wrapped, max(w-4, 1), true)
	lines := strings.Split(wrapped, "\n")
	for index, line := range lines {
		lines[index] = " " + line
	}
	return panel(title, info, strings.Join(lines, "\n"), w, h, focused)
}

// fitLines cuts or pads body to exactly h lines of w cells.
func fitLines(body string, w, h int) []string {
	var lines []string
	if body != "" {
		lines = strings.Split(body, "\n")
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	for index, line := range lines {
		lines[index] = padRight(ansi.Truncate(line, w, "…"), w)
	}
	return lines
}

func padRight(text string, w int) string {
	if gap := w - ansi.StringWidth(text); gap > 0 {
		return text + strings.Repeat(" ", gap)
	}
	return text
}

func alignText(text string, w int, alignment align) string {
	text = ansi.Truncate(text, w, "…")
	gap := w - ansi.StringWidth(text)
	if gap <= 0 {
		return text
	}
	switch alignment {
	case alignRight:
		return strings.Repeat(" ", gap) + text
	case alignCenter:
		return strings.Repeat(" ", gap/2) + text + strings.Repeat(" ", gap-gap/2)
	}
	return text + strings.Repeat(" ", gap)
}

// splitWidth divides total cells in the given proportions.
func splitWidth(total int, weights ...int) []int {
	sum := 0
	for _, weight := range weights {
		sum += weight
	}
	sizes := make([]int, len(weights))
	used := 0
	for index, weight := range weights {
		sizes[index] = total * weight / max(sum, 1)
		used += sizes[index]
	}
	sizes[len(sizes)-1] += total - used
	return sizes
}

type align int

const (
	alignLeft align = iota
	alignRight
	alignCenter
)

// column is a grid column: fixed width, or a share of the width left over
// when flex is set. In a grid with groups, group says which side of the
// table the column belongs to.
type column struct {
	title string
	width int
	flex  int
	align align
	group int
}

// cell is one value. A bar fills that fraction of the cell behind its text,
// from the right when barRight is set.
type cell struct {
	text     string
	style    lipgloss.Style
	bar      float64
	barColor color.Color
	barRight bool
}

func textCell(text string, style lipgloss.Style) cell {
	return cell{text: text, style: style}
}

// row is a grid row: its cells, what it refers to (refs holds one reference
// per group), whether it can be selected and its background.
type row struct {
	cells []cell
	ref   any
	refs  []any
	skip  bool
	bg    color.Color
}

// grid is a scrolling, selectable table with styled cells. With groups, the
// columns form sides (a chain's calls and puts) and Left and Right move the
// selection between them.
type grid struct {
	columns []column
	rows    []row
	cursor  int
	offset  int
	groups  int
	group   int
	// empty is shown, already styled, when there are no rows.
	empty string
	// centerNext scrolls the cursor to the middle at the next render.
	centerNext bool
	// height is the number of rows the last render showed; x, y and starts
	// place them on screen for mouse clicks.
	height int
	x, y   int
	w      int
	starts []int
}

// setRows replaces the rows, keeping the cursor's position.
func (g *grid) setRows(rows []row) {
	g.rows = rows
	g.cursor = min(g.cursor, max(len(rows)-1, 0))
	g.settle(1)
}

// settle moves the cursor off an unselectable row, looking in direction
// first, and onto a group the row has a reference for.
func (g *grid) settle(direction int) {
	if len(g.rows) == 0 {
		g.cursor = 0
		return
	}
	for _, step := range []int{direction, -direction} {
		for index := g.cursor; index >= 0 && index < len(g.rows); index += step {
			if !g.rows[index].skip {
				g.cursor = index
				g.settleGroup()
				return
			}
		}
	}
}

func (g *grid) settleGroup() {
	if g.groups == 0 || len(g.rows) == 0 {
		return
	}
	refs := g.rows[g.cursor].refs
	if g.group < len(refs) && refs[g.group] != nil {
		return
	}
	for group, ref := range refs {
		if ref != nil {
			g.group = group
			return
		}
	}
}

// selected is the row under the cursor.
func (g *grid) selected() (row, bool) {
	if g.cursor < 0 || g.cursor >= len(g.rows) || g.rows[g.cursor].skip {
		return row{}, false
	}
	return g.rows[g.cursor], true
}

// selectedRef is the selected row's reference, or with groups the selected
// side's.
func (g *grid) selectedRef() any {
	selected, ok := g.selected()
	if !ok {
		return nil
	}
	if g.groups > 0 {
		if g.group < len(selected.refs) {
			return selected.refs[g.group]
		}
		return nil
	}
	return selected.ref
}

// selectWhere moves the cursor to the first row match accepts.
func (g *grid) selectWhere(match func(row) bool) bool {
	for index, candidate := range g.rows {
		if !candidate.skip && match(candidate) {
			g.cursor = index
			g.settleGroup()
			return true
		}
	}
	return false
}

func (g *grid) move(delta int) {
	if len(g.rows) == 0 {
		return
	}
	direction := 1
	if delta < 0 {
		direction = -1
	}
	g.cursor = min(max(g.cursor+delta, 0), len(g.rows)-1)
	g.settle(direction)
}

// update handles navigation keys, reporting whether it used the key.
func (g *grid) update(key string) bool {
	page := max(g.height-1, 1)
	switch key {
	case "up", "k":
		g.move(-1)
	case "down", "j":
		g.move(1)
	case "pgup", "ctrl+u":
		g.move(-page)
	case "pgdown", "ctrl+d":
		g.move(page)
	case "home", "g":
		g.cursor = 0
		g.settle(1)
	case "end", "G":
		g.cursor = max(len(g.rows)-1, 0)
		g.settle(-1)
	case "left", "right":
		if g.groups == 0 {
			return false
		}
		step := 1
		if key == "left" {
			step = -1
		}
		if selected, ok := g.selected(); ok {
			for next := g.group + step; next >= 0 && next < g.groups; next += step {
				if next < len(selected.refs) && selected.refs[next] != nil {
					g.group = next
					break
				}
			}
		}
	default:
		return false
	}
	return true
}

// wheel scrolls the selection with the mouse wheel.
func (g *grid) wheel(button tea.MouseButton) {
	switch button {
	case tea.MouseWheelUp:
		g.move(-3)
	case tea.MouseWheelDown:
		g.move(3)
	}
}

// click selects the row (and side) at a screen position, reporting whether
// the position is on a row and whether that row was already selected.
func (g *grid) click(x, y int) (hit, again bool) {
	if y < g.y || y >= g.y+g.height || x < g.x || x >= g.x+g.w {
		return false, false
	}
	index := g.offset + y - g.y
	if index >= len(g.rows) || g.rows[index].skip {
		return false, false
	}
	group := g.group
	if g.groups > 0 {
		for column, start := range g.starts {
			if x-g.x >= start && column < len(g.columns) {
				group = g.columns[column].group
			}
		}
		if refs := g.rows[index].refs; group >= len(refs) || refs[group] == nil {
			group = g.group
		}
	}
	again = index == g.cursor && group == g.group
	g.cursor, g.group = index, group
	g.settleGroup()
	return true, again
}

func (g *grid) widths(w int) []int {
	widths := make([]int, len(g.columns))
	fixed, flex := 1+len(g.columns), 0
	for index, column := range g.columns {
		widths[index] = column.width
		fixed += column.width
		flex += column.flex
	}
	if flex == 0 {
		return widths
	}
	spare := max(w-fixed, 0)
	given := 0
	last := -1
	for index, column := range g.columns {
		if column.flex == 0 {
			continue
		}
		share := spare * column.flex / flex
		widths[index] = max(column.width, share)
		given += share
		last = index
	}
	if last >= 0 && spare > given {
		widths[last] += spare - given
	}
	return widths
}

// view renders the header and as many rows as fit in h lines of w cells.
func (g *grid) view(w, h int, focused bool) string {
	if h <= 0 {
		return ""
	}
	widths := g.widths(w)
	g.starts = g.starts[:0]
	position := 1
	header := " "
	for index, column := range g.columns {
		g.starts = append(g.starts, position)
		position += widths[index] + 1
		header += styleHeader.Render(alignText(column.title, widths[index], column.align)) + " "
	}
	lines := []string{header}
	body := h - 1
	g.height = body
	g.w = w
	if len(g.rows) == 0 {
		if g.empty != "" {
			lines = append(lines, " "+g.empty)
		}
		return strings.Join(lines, "\n")
	}
	if g.centerNext {
		g.offset = g.cursor - body/2
		g.centerNext = false
	}
	if g.cursor < g.offset {
		g.offset = g.cursor
	}
	if g.cursor >= g.offset+body {
		g.offset = g.cursor - body + 1
	}
	g.offset = min(max(g.offset, 0), max(len(g.rows)-body, 0))
	for index := g.offset; index < len(g.rows) && index < g.offset+body; index++ {
		lines = append(lines, g.renderRow(g.rows[index], widths, index == g.cursor, focused))
	}
	return strings.Join(lines, "\n")
}

func (g *grid) renderRow(r row, widths []int, current, focused bool) string {
	base := lipgloss.NewStyle()
	if r.bg != nil {
		base = base.Background(r.bg)
	}
	selection := colorSelection
	if !focused {
		selection = lipgloss.Color("#23232E")
	}
	// Only the focused table shows its selection.
	current = current && focused
	whole := current && g.groups == 0 && !r.skip
	gap := base
	if whole {
		gap = gap.Background(selection)
	}
	var line strings.Builder
	line.WriteString(gap.Render(" "))
	for index, column := range g.columns {
		var c cell
		if index < len(r.cells) {
			c = r.cells[index]
		}
		style := c.style
		if r.bg != nil {
			style = style.Background(r.bg)
		}
		if whole || (current && !r.skip && g.groups > 0 && column.group == g.group && focused) {
			style = style.Background(selection).Bold(true)
		}
		line.WriteString(drawCell(c, style, widths[index], column.align))
		line.WriteString(gap.Render(" "))
	}
	return line.String()
}

// drawCell aligns a cell's text in width cells, over its bar when it has
// one.
func drawCell(c cell, style lipgloss.Style, width int, alignment align) string {
	text := alignText(c.text, width, alignment)
	length := 0
	if c.bar > 0 {
		length = min(max(int(math.Round(c.bar*float64(width))), 1), width)
	}
	if length == 0 || c.barColor == nil {
		return style.Render(text)
	}
	runes := []rune(text)
	if len(runes) != width {
		return style.Render(text)
	}
	barStyle := style.Background(c.barColor)
	if c.barRight {
		split := width - length
		return style.Render(string(runes[:split])) + barStyle.Render(string(runes[split:]))
	}
	return barStyle.Render(string(runes[:length])) + style.Render(string(runes[length:]))
}

// fieldKind is what a form field holds.
type fieldKind int

const (
	fieldInput fieldKind = iota
	fieldSelect
	fieldToggle
	fieldNote
)

// numeric restricts what an input accepts.
type numeric int

const (
	anyText numeric = iota
	wholeNumber
	decimalNumber
)

type field struct {
	kind     fieldKind
	label    string
	input    textinput.Model
	numeric  numeric
	options  []string
	index    int
	on       bool
	note     string
	hidden   bool
	disabled bool
}

func (f *field) value() string {
	switch f.kind {
	case fieldInput:
		return strings.TrimSpace(f.input.Value())
	case fieldSelect:
		if f.index >= 0 && f.index < len(f.options) {
			return f.options[f.index]
		}
	}
	return ""
}

func (f *field) focusable() bool {
	return !f.hidden && !f.disabled && f.kind != fieldNote
}

var fieldBackground = lipgloss.Color("#23232E")

func newInput(value string, width int, password bool) textinput.Model {
	input := textinput.New()
	input.Prompt = ""
	input.SetValue(value)
	input.SetWidth(width)
	if password {
		input.EchoMode = textinput.EchoPassword
		input.EchoCharacter = '•'
	}
	focused := lipgloss.NewStyle().Foreground(colorText).Background(fieldBackground)
	input.SetStyles(textinput.Styles{
		Focused: textinput.StyleState{Text: focused, Placeholder: focused.Foreground(colorFaint)},
		Blurred: textinput.StyleState{Text: styleMuted, Placeholder: styleFaint},
		Cursor:  textinput.CursorStyle{Color: colorAccent, Blink: false},
	})
	return input
}

// form is a column of labelled fields over a row of buttons. Up and Down (or
// Tab) move between fields, skipping hidden and disabled ones; Left and
// Right change a select's option; Space flips a toggle; Enter moves on, or
// presses the focused button.
type form struct {
	fields     []*field
	buttons    []string
	focus      int
	labelWidth int
	inputWidth int
	message    string
}

func newForm(labelWidth, inputWidth int, buttons ...string) *form {
	return &form{labelWidth: labelWidth, inputWidth: inputWidth, buttons: buttons}
}

func (f *form) addInput(label, value string, kind numeric, password bool) *field {
	added := &field{kind: fieldInput, label: label, input: newInput(value, f.inputWidth, password), numeric: kind}
	f.fields = append(f.fields, added)
	return added
}

func (f *form) addSelect(label string, options []string, index int) *field {
	added := &field{kind: fieldSelect, label: label, options: options, index: index}
	f.fields = append(f.fields, added)
	return added
}

func (f *form) addToggle(label string, on bool) *field {
	added := &field{kind: fieldToggle, label: label, on: on}
	f.fields = append(f.fields, added)
	return added
}

func (f *form) addNote(label, note string) *field {
	added := &field{kind: fieldNote, label: label, note: note}
	f.fields = append(f.fields, added)
	return added
}

func (f *form) field(label string) *field {
	for _, candidate := range f.fields {
		if candidate.label == label {
			return candidate
		}
	}
	return &field{}
}

// focusField focuses the field with label.
func (f *form) focusField(label string) {
	for index, candidate := range f.fields {
		if candidate.label == label {
			f.focus = index
		}
	}
	f.syncFocus()
}

// focusFirst focuses the first field that takes input.
func (f *form) focusFirst() {
	f.focus = -1
	f.step(1)
}

func (f *form) slots() int {
	return len(f.fields) + len(f.buttons)
}

func (f *form) focusableAt(index int) bool {
	if index < len(f.fields) {
		return f.fields[index].focusable()
	}
	return index < f.slots()
}

// step moves the focus to the next field or button that can take it.
func (f *form) step(delta int) {
	count := f.slots()
	if count == 0 {
		return
	}
	next := f.focus
	for range count {
		next = ((next+delta)%count + count) % count
		if f.focusableAt(next) {
			f.focus = next
			break
		}
	}
	f.syncFocus()
}

// syncFocus focuses the focused input and blurs the rest, moving off a
// field that was hidden or disabled meanwhile.
func (f *form) syncFocus() {
	if f.focus >= 0 && f.focus < len(f.fields) && !f.fields[f.focus].focusable() {
		f.step(1)
		return
	}
	for index, candidate := range f.fields {
		if candidate.kind != fieldInput {
			continue
		}
		if index == f.focus {
			candidate.input.Focus()
		} else {
			candidate.input.Blur()
		}
	}
}

func (f *form) current() *field {
	if f.focus >= 0 && f.focus < len(f.fields) {
		return f.fields[f.focus]
	}
	return nil
}

// button is the focused button's index, or -1 on a field.
func (f *form) button() int {
	if f.focus >= len(f.fields) {
		return f.focus - len(f.fields)
	}
	return -1
}

// update applies a key. It returns the index of the button pressed, or -1,
// and whether a value changed.
func (f *form) update(msg tea.KeyPressMsg) (pressed int, changed bool) {
	current := f.current()
	switch key := msg.String(); key {
	case "up", "shift+tab":
		f.step(-1)
		return -1, false
	case "down", "tab":
		f.step(1)
		return -1, false
	case "enter":
		if button := f.button(); button >= 0 {
			return button, false
		}
		f.step(1)
		return -1, false
	case "left", "right":
		delta := 1
		if key == "left" {
			delta = -1
		}
		switch {
		case f.button() >= 0:
			next := f.button() + delta
			if next >= 0 && next < len(f.buttons) {
				f.focus = len(f.fields) + next
			}
			return -1, false
		case current != nil && current.kind == fieldSelect && len(current.options) > 0:
			current.index = ((current.index+delta)%len(current.options) + len(current.options)) % len(current.options)
			return -1, true
		case current != nil && current.kind == fieldToggle:
			current.on = !current.on
			return -1, true
		}
	case "space":
		switch {
		case current != nil && current.kind == fieldToggle:
			current.on = !current.on
			return -1, true
		case current != nil && current.kind == fieldSelect && len(current.options) > 0:
			current.index = (current.index + 1) % len(current.options)
			return -1, true
		case f.button() >= 0:
			return f.button(), false
		}
	}
	if current == nil || current.kind != fieldInput {
		return -1, false
	}
	if !accepts(current.numeric, current.input.Value(), msg.Text) {
		return -1, false
	}
	before := current.input.Value()
	current.input, _ = current.input.Update(msg)
	return -1, current.input.Value() != before
}

// accepts reports whether typing text into value keeps it a number of the
// kind the field takes.
func accepts(kind numeric, value, text string) bool {
	if kind == anyText || text == "" {
		return true
	}
	for _, r := range text {
		switch {
		case r >= '0' && r <= '9':
		case r == '.' && kind == decimalNumber && !strings.Contains(value, "."):
		default:
			return false
		}
	}
	return true
}

// view renders the fields and buttons in width cells.
func (f *form) view(width int) string {
	var lines []string
	valueWidth := max(width-f.labelWidth-3, 8)
	for index, current := range f.fields {
		if current.hidden {
			continue
		}
		focused := index == f.focus
		label := styleMuted.Render(padRight(current.label, f.labelWidth))
		marker := "  "
		if focused {
			label = styleAccent.Render(padRight(current.label, f.labelWidth))
			marker = styleAccent.Render("▸ ")
		}
		var value string
		switch current.kind {
		case fieldInput:
			switch {
			case current.disabled:
				value = styleFaint.Render(padRight("—", f.inputWidth))
			case focused:
				value = lipgloss.NewStyle().Background(fieldBackground).Render(padRight(current.input.View(), f.inputWidth+1))
			default:
				shown := current.input.Value()
				if current.input.EchoMode == textinput.EchoPassword {
					shown = strings.Repeat("•", len([]rune(shown)))
				}
				value = lipgloss.NewStyle().Foreground(colorText).Background(lipgloss.Color("#1C1C24")).Render(padRight(shown, f.inputWidth+1))
			}
		case fieldSelect:
			option := current.value()
			switch {
			case current.disabled:
				// A fixed choice shows without the arrows that change it.
				value = "  " + styleMuted.Render(option)
			case focused:
				value = styleAccent.Render("‹ ") + styleBold.Render(option) + styleAccent.Render(" ›")
			default:
				value = styleFaint.Render("‹ ") + styleText.Render(option) + styleFaint.Render(" ›")
			}
		case fieldToggle:
			box := styleFaint.Render("[ ]")
			if current.on {
				box = styleGreen.Render("[✓]")
			}
			value = box
		case fieldNote:
			wrapped := strings.Split(ansi.Wordwrap(current.note, valueWidth, ""), "\n")
			for line, text := range wrapped {
				if line == 0 {
					lines = append(lines, marker+label+" "+styleMuted.Render(text))
				} else {
					lines = append(lines, strings.Repeat(" ", f.labelWidth+3)+styleMuted.Render(text))
				}
			}
			continue
		}
		lines = append(lines, marker+label+" "+value)
	}
	lines = append(lines, "")
	var buttons []string
	for index, label := range f.buttons {
		style := lipgloss.NewStyle().Padding(0, 2).Background(lipgloss.Color("#2A2D37")).Foreground(colorText)
		if f.button() == index {
			style = style.Background(colorAccent).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
		}
		buttons = append(buttons, style.Render(label))
	}
	lines = append(lines, alignText(strings.Join(buttons, "  "), width, alignCenter))
	if f.message != "" {
		lines = append(lines, "", ansi.Wordwrap(f.message, width, ""))
	}
	return strings.Join(lines, "\n")
}

// modal frames body as a dialog of width cells, sized to its content.
func modal(title, info, body string, width int) string {
	inner := width - 4
	lines := strings.Split(body, "\n")
	for index, line := range lines {
		lines[index] = " " + padRight(ansi.Truncate(line, inner, "…"), inner) + " "
	}
	content := append([]string{""}, lines...)
	content = append(content, "")
	return panel(title, info, strings.Join(content, "\n"), width, len(content)+2, true)
}

// composite dims background and centres foreground over it.
func composite(background, foreground string, w, h int) string {
	lines := fitLines(background, w, h)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#3B3F4A"))
	for index, line := range lines {
		lines[index] = dim.Render(ansi.Strip(line))
	}
	fw, fh := lipgloss.Width(foreground), lipgloss.Height(foreground)
	x, y := max((w-fw)/2, 0), max((h-fh)/2, 0)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(strings.Join(lines, "\n")),
		lipgloss.NewLayer(foreground).X(x).Y(y).Z(1),
	).Render()
}

// menuItem is an entry of a menu overlay, chosen by Enter or its key.
type menuItem struct {
	key   string
	label string
	run   func(*app) tea.Cmd
}

// menu is a list overlay: a picker of users, lists or expiries, or the
// order menu.
type menu struct {
	title  string
	info   string
	items  []menuItem
	cursor int
	width  int
	footer string
	// body is shown above the items.
	body string
	// keyFold matches item keys regardless of case for these keys.
	keyFold string
}

func (m *menu) update(a *app, msg tea.KeyPressMsg) tea.Cmd {
	switch key := msg.String(); key {
	case "esc", "q", "Q":
		a.closeOverlay()
		return nil
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
		return nil
	case "down", "j":
		m.cursor = min(m.cursor+1, len(m.items)-1)
		return nil
	case "home":
		m.cursor = 0
		return nil
	case "end":
		m.cursor = len(m.items) - 1
		return nil
	case "enter":
		return m.choose(a, m.cursor)
	default:
		for index, item := range m.items {
			matches := item.key != "" && key == item.key
			if !matches && item.key != "" && strings.Contains(m.keyFold, item.key) {
				matches = strings.EqualFold(key, item.key)
			}
			if matches {
				return m.choose(a, index)
			}
		}
	}
	return nil
}

func (m *menu) choose(a *app, index int) tea.Cmd {
	if index < 0 || index >= len(m.items) {
		return nil
	}
	a.closeOverlay()
	return m.items[index].run(a)
}

func (m *menu) view(_ *app, w, h int) string {
	width := min(max(m.width, 30), w-2)
	visible := max(min(len(m.items), h-8), 1)
	first := min(max(m.cursor-visible/2, 0), max(len(m.items)-visible, 0))
	var lines []string
	if m.body != "" {
		lines = append(lines, m.body, "")
	}
	for index := first; index < len(m.items) && index < first+visible; index++ {
		text := padRight(" "+m.items[index].label, width-4)
		if index == m.cursor {
			text = lipgloss.NewStyle().Background(colorSelection).Bold(true).Render(text)
		}
		lines = append(lines, text)
	}
	if m.footer != "" {
		lines = append(lines, "", styleFaint.Render(m.footer))
	}
	return modal(m.title, m.info, strings.Join(lines, "\n"), width)
}
