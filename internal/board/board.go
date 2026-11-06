package board

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"image/color"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Package board is the terminal board behind `tk board`. It knows nothing about
// backends: tk hands it a Store, so the same board can show any data tk manages.

type boardMode int

const (
	modeBoard boardMode = iota
	modeDetail
	modeInput
	modeConfirm
)

type boardColumn struct {
	name     string
	statuses []string
	primary  string // status a card gets when moved into this column
}

var baseColumns = []boardColumn{
	{"Todo", []string{"backlog", "todo"}, "todo"},
	{"In progress", []string{"in_progress", "blocked"}, "in_progress"},
	{"Done", []string{"done"}, "done"},
}
var cancelledColumn = boardColumn{"Cancelled", []string{"cancelled"}, "cancelled"}

type boardTick struct{}

const boardRefresh = 3 * time.Second

type boardModel struct {
	store         Store
	items         []issueDetail
	showCancelled bool
	col, row      int
	width, height int
	mode, back    boardMode       // back: where an input or confirm returns to
	sessionKeys   map[string]bool // issues an agent's tk session is open on
	flash         string
	help          bool

	detailKey string
	vp        viewport.Model

	input   textinput.Model
	area    textarea.Model
	useArea bool
	purpose string // "new", "comment", "title", "description"
	target  string // issue key the input or confirm applies to
}

func newBoardModel(st Store) *boardModel {
	ti := textinput.New()
	ti.CharLimit = 500
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.CharLimit = 10000
	m := &boardModel{store: st, input: ti, area: ta, width: 100, height: 30, vp: viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))}
	m.reload()
	return m
}

// Run shows the board until the user quits.
func Run(st Store) error {
	applyTheme(initialDark())
	_, err := tea.NewProgram(newBoardModel(st)).Run()
	return err
}

// initialDark is the theme to draw with before the terminal reports its
// background: TK_THEME=light|dark, else COLORFGBG (set by many terminals), else dark.
func initialDark() bool {
	switch strings.ToLower(os.Getenv("TK_THEME")) {
	case "light":
		return false
	case "dark":
		return true
	}
	if v := os.Getenv("COLORFGBG"); v != "" {
		parts := strings.Split(v, ";")
		if bg, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			return bg < 7 || bg == 8
		}
	}
	return true
}

// Init asks the terminal for its background colour without waiting for it (a
// terminal that never answers just keeps the initial theme) and starts the refresh tick.
func (m *boardModel) Init() tea.Cmd {
	if os.Getenv("TK_THEME") != "" {
		return tickBoard()
	}
	return tea.Batch(tea.RequestBackgroundColor, tickBoard())
}

func tickBoard() tea.Cmd {
	return tea.Tick(boardRefresh, func(time.Time) tea.Msg { return boardTick{} })
}

// ---- data ----

func (m *boardModel) columns() []boardColumn {
	if m.showCancelled {
		return append(append([]boardColumn{}, baseColumns...), cancelledColumn)
	}
	return baseColumns
}

func (m *boardModel) cards(col int) []issueDetail {
	var out []issueDetail
	c := m.columns()[col]
	for _, it := range m.items {
		if oneOf(it.Status, c.statuses) {
			out = append(out, it)
		}
	}
	return out
}

func (m *boardModel) selected() (issueDetail, bool) {
	cs := m.cards(m.col)
	if m.row < 0 || m.row >= len(cs) {
		return issueDetail{}, false
	}
	return cs[m.row], true
}

// current is the issue an action applies to: the open detail, else the selected card.
func (m *boardModel) current() (issueDetail, bool) {
	if m.mode == modeDetail || (m.back == modeDetail && m.detailKey != "") {
		for _, it := range m.items {
			if it.Identifier == m.detailKey {
				return it, true
			}
		}
	}
	return m.selected()
}

// reload re-reads the store, keeping the selection on the same issue when it moved.
func (m *boardModel) reload() {
	keep := ""
	if it, ok := m.selected(); ok {
		keep = it.Identifier
	}
	items, keys, err := m.store.Load()
	if err != nil {
		m.flash = "error: " + err.Error()
		return
	}
	m.items, m.sessionKeys = items, map[string]bool{}
	for _, k := range keys {
		m.sessionKeys[k] = true
	}
	if keep != "" {
		m.focus(keep)
	}
	m.clamp()
	if m.mode == modeDetail {
		m.renderDetail()
	}
}

// focus moves the selection to the issue with this key, if it is shown.
func (m *boardModel) focus(key string) {
	for c := range m.columns() {
		for r, it := range m.cards(c) {
			if it.Identifier == key {
				m.col, m.row = c, r
				return
			}
		}
	}
}

func (m *boardModel) clamp() {
	n := len(m.columns())
	if m.col >= n {
		m.col = n - 1
	}
	if m.col < 0 {
		m.col = 0
	}
	if k := len(m.cards(m.col)); m.row >= k {
		m.row = k - 1
	}
	if m.row < 0 {
		m.row = 0
	}
}

// ---- actions ----

func (m *boardModel) setStatus(key, status string) {
	if err := m.store.Update(key, "status", status); err != nil {
		m.flash = "error: " + err.Error()
		return
	}
	m.flash = key + " → " + status
	m.reload()
	m.focus(key)
}

func (m *boardModel) move(delta int) {
	it, ok := m.current()
	if !ok {
		return
	}
	cols := m.columns()
	from := 0
	for i, c := range cols {
		if oneOf(it.Status, c.statuses) {
			from = i
		}
	}
	to := from + delta
	if to < 0 || to >= len(baseColumns) { // moving into Cancelled goes through x (confirm)
		return
	}
	m.setStatus(it.Identifier, cols[to].primary)
}

func (m *boardModel) cyclePriority() {
	it, ok := m.current()
	if !ok {
		return
	}
	next := "medium"
	for i, p := range localPriorities {
		if p == it.Priority {
			next = localPriorities[(i+len(localPriorities)-1)%len(localPriorities)] // toward urgent
		}
	}
	if err := m.store.Update(it.Identifier, "priority", next); err != nil {
		m.flash = "error: " + err.Error()
		return
	}
	m.flash = it.Identifier + " priority " + next
	m.reload()
}

func (m *boardModel) startInput(purpose, target, value string, area bool) tea.Cmd {
	m.back, m.mode, m.purpose, m.target, m.useArea = m.mode, modeInput, purpose, target, area
	if area {
		m.area.SetValue(value)
		m.area.SetWidth(max(20, m.width-6))
		m.area.SetHeight(max(3, m.height/3))
		return m.area.Focus()
	}
	m.input.SetValue(value)
	m.input.CursorEnd()
	m.input.SetWidth(max(20, m.width-20))
	return m.input.Focus()
}

func (m *boardModel) submitInput() {
	m.input.Blur()
	m.area.Blur()
	value := strings.TrimSpace(m.input.Value())
	if m.useArea {
		value = strings.TrimSpace(m.area.Value())
	}
	m.mode = m.back
	var err error
	switch m.purpose {
	case "new":
		if value == "" {
			return
		}
		var key string
		if key, err = m.store.Create(value); err == nil {
			m.flash = key + " created"
			m.reload()
			m.focus(key)
			return
		}
	case "comment":
		if value == "" {
			return
		}
		if err = m.store.Comment(m.target, value); err == nil {
			m.flash = m.target + " comment added"
		}
	case "title":
		if err = m.store.Update(m.target, "title", value); err == nil {
			m.flash = m.target + " title saved"
		}
	case "description":
		if err = m.store.Update(m.target, "description", value); err == nil {
			m.flash = m.target + " description saved"
		}
	}
	if err != nil {
		m.flash = "error: " + err.Error()
	}
	m.reload()
}

func (m *boardModel) openDetail() {
	it, ok := m.selected()
	if !ok {
		return
	}
	m.mode, m.detailKey = modeDetail, it.Identifier
	m.renderDetail()
	m.vp.GotoTop()
}

// ---- update ----

func (m *boardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.vp.SetWidth(max(20, msg.Width-4))
		m.vp.SetHeight(max(5, msg.Height-6))
		if m.mode == modeDetail {
			m.renderDetail()
		}
		return m, nil
	case boardTick:
		if m.mode != modeInput { // never redraw under someone typing
			m.reload()
		}
		return m, tickBoard()
	case tea.BackgroundColorMsg:
		applyTheme(msg.IsDark())
		if m.mode == modeDetail {
			m.renderDetail()
		}
		return m, nil
	case tea.KeyPressMsg:
		return m, m.handleKey(msg)
	}
	return m, nil
}

func (m *boardModel) handleKey(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if key == "ctrl+c" {
		return tea.Quit
	}
	switch m.mode {
	case modeInput:
		switch {
		case key == "esc":
			m.input.Blur()
			m.area.Blur()
			m.mode, m.flash = m.back, "cancelled edit"
			return nil
		case key == "enter" && !m.useArea, key == "ctrl+s" && m.useArea:
			m.submitInput()
			return nil
		}
		var cmd tea.Cmd
		if m.useArea {
			m.area, cmd = m.area.Update(k)
		} else {
			m.input, cmd = m.input.Update(k)
		}
		return cmd
	case modeConfirm:
		m.mode = m.back
		if key == "y" || key == "Y" {
			if err := m.store.Cancel(m.target); err != nil {
				m.flash = "error: " + err.Error()
			} else {
				m.flash = m.target + " cancelled"
			}
			m.reload()
		} else {
			m.flash = "kept " + m.target
		}
		return nil
	}

	// keys shared by the board and the detail view
	if it, ok := m.current(); ok {
		switch key {
		case "c":
			return m.startInput("comment", it.Identifier, "", false)
		case "e":
			return m.startInput("title", it.Identifier, it.Title, false)
		case "d":
			return m.startInput("description", it.Identifier, it.Description, true)
		case "p":
			m.cyclePriority()
			return nil
		case ">", "L", "shift+right":
			m.move(1)
			return nil
		case "<", "H", "shift+left":
			m.move(-1)
			return nil
		case "x":
			if it.Status != "cancelled" {
				m.back, m.mode, m.target = m.mode, modeConfirm, it.Identifier
			}
			return nil
		}
	}
	switch key {
	case "q":
		if m.mode == modeDetail {
			m.mode = modeBoard
			return nil
		}
		return tea.Quit
	case "?":
		m.help = !m.help
		return nil
	case "r":
		m.reload()
		m.flash = "refreshed"
		return nil
	case "n":
		return m.startInput("new", "", "", false)
	}

	if m.mode == modeDetail {
		switch key {
		case "esc", "backspace", "left", "h":
			m.mode = modeBoard
			m.focus(m.detailKey)
			return nil
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(k)
		return cmd
	}

	switch key {
	case "left", "h":
		if m.col > 0 {
			m.col--
			m.clamp()
		}
	case "right", "l":
		if m.col < len(m.columns())-1 {
			m.col++
			m.clamp()
		}
	case "up", "k":
		if m.row > 0 {
			m.row--
		}
	case "down", "j":
		if m.row < len(m.cards(m.col))-1 {
			m.row++
		}
	case "enter":
		m.openDetail()
	case "a":
		m.showCancelled = !m.showCancelled
		m.clamp()
	}
	return nil
}

// ---- view ----

// Colours are chosen for the terminal's background; applyTheme rebuilds them
// when the terminal reports it.
var (
	cAccent, cMuted, cBorder, cAgent, cWarn, cDanger color.Color
	stMuted, stBold, stAccent, stAgent, stFlash      lipgloss.Style
	priorityC                                        map[string]color.Color
)

func applyTheme(dark bool) {
	ld := lipgloss.LightDark(dark)
	pick := func(light, darkHex string) color.Color { return ld(lipgloss.Color(light), lipgloss.Color(darkHex)) }
	cAccent = pick("#5B5BD6", "#9B9BFF")
	cMuted = pick("#6B6F76", "#8A8F98")
	cBorder = pick("#D0D3D9", "#3A3D44")
	cAgent = pick("#1F7A4D", "#4CC38A")
	cWarn = pick("#B35900", "#F5A524")
	cDanger = pick("#C4314B", "#F2555A")
	stMuted = lipgloss.NewStyle().Foreground(cMuted)
	stBold = lipgloss.NewStyle().Bold(true)
	stAccent = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	stAgent = lipgloss.NewStyle().Foreground(cAgent)
	stFlash = lipgloss.NewStyle().Foreground(cWarn)
	priorityC = map[string]color.Color{"urgent": cDanger, "high": cWarn, "medium": cAccent, "low": cMuted, "none": cMuted}
}

func init() { applyTheme(true) }

func truncate(s string, n int) string {
	r := []rune(s)
	if n <= 1 || len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func (m *boardModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m *boardModel) render() string {
	var b strings.Builder
	open := 0
	for _, it := range m.items {
		if !isClosed(it.Status) {
			open++
		}
	}
	head := stAccent.Render("tk board") + stMuted.Render(fmt.Sprintf("  local · %d open · %d total", open, len(m.items)))
	if len(m.sessionKeys) > 0 {
		var keys []string
		for k := range m.sessionKeys {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		head += stAgent.Render("  ● agent working on " + strings.Join(keys, ", "))
	}
	b.WriteString(head + "\n")
	if m.flash != "" {
		b.WriteString(stFlash.Render(truncate(m.flash, m.width-2)))
	}
	b.WriteString("\n")

	switch {
	case m.mode == modeDetail || (m.mode != modeBoard && m.back == modeDetail):
		b.WriteString(m.vp.View())
	default:
		b.WriteString(m.boardView())
	}
	b.WriteString("\n")
	b.WriteString(m.footer())
	return b.String()
}

func (m *boardModel) boardView() string {
	cols := m.columns()
	gap := 1
	colW := max(18, (m.width-gap*(len(cols)-1))/len(cols))
	avail := max(6, m.height-6) // header 2, footer 2, column title 2
	perCol := max(1, avail/5)   // a card is 4 lines plus a gap
	var rendered []string
	for ci, c := range cols {
		cs := m.cards(ci)
		title := fmt.Sprintf("%s %d", c.name, len(cs))
		ts := stBold
		if ci == m.col && m.mode == modeBoard {
			ts = stAccent
		}
		lines := []string{ts.Render(truncate(title, colW)), ""}
		off := 0
		if ci == m.col && m.row >= perCol {
			off = m.row - perCol + 1
		}
		for ri := off; ri < len(cs) && ri < off+perCol; ri++ {
			lines = append(lines, m.card(cs[ri], colW, ci == m.col && ri == m.row && m.mode == modeBoard))
		}
		if len(cs) == 0 {
			lines = append(lines, stMuted.Render("  —"))
		} else if more := len(cs) - off - perCol; more > 0 {
			lines = append(lines, stMuted.Render(fmt.Sprintf("  ↓ %d more", more)))
		}
		rendered = append(rendered, lipgloss.NewStyle().Width(colW).Render(strings.Join(lines, "\n")))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, joinWithGap(rendered, gap)...)
}

func joinWithGap(cols []string, gap int) []string {
	out := make([]string, 0, len(cols)*2)
	for i, c := range cols {
		if i > 0 {
			out = append(out, strings.Repeat(" ", gap))
		}
		out = append(out, c)
	}
	return out
}

func (m *boardModel) card(it issueDetail, w int, sel bool) string {
	inner := w - 4
	pc := priorityC[it.Priority]
	if pc == nil {
		pc = cMuted
	}
	line1 := stMuted.Render(it.Identifier) + "  " + lipgloss.NewStyle().Foreground(pc).Render("● "+it.Priority)
	line2 := truncate(it.Title, inner)
	var meta []string
	if m.sessionKeys[it.Identifier] {
		meta = append(meta, stAgent.Render("● agent working"))
	}
	if it.Cost.Events > 0 {
		meta = append(meta, stMuted.Render(shortCost(it.Cost)))
	}
	line3 := strings.Join(meta, stMuted.Render(" · "))
	border := cBorder
	if sel {
		border = cAccent
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Width(w-2).Padding(0, 1).
		Render(line1 + "\n" + line2 + "\n" + line3)
}

func shortCost(t costTotals) string {
	switch {
	case t.Unpriced == t.Events:
		return "unpriced"
	case t.Unpriced > 0:
		return fmt.Sprintf("~$%.2f+?", t.Cents/100)
	default:
		return fmt.Sprintf("~$%.2f", t.Cents/100)
	}
}

func (m *boardModel) renderDetail() {
	var it issueDetail
	found := false
	for _, x := range m.items {
		if x.Identifier == m.detailKey {
			it, found = x, true
		}
	}
	if !found {
		m.vp.SetContent(stMuted.Render(m.detailKey + " not found"))
		return
	}
	w := max(20, m.vp.Width())
	wrap := lipgloss.NewStyle().Width(w)
	var b strings.Builder
	b.WriteString(stMuted.Render(it.Identifier) + "  " + stBold.Render(it.Title) + "\n")
	meta := []string{it.Status, "priority " + it.Priority, "cost " + it.CostText}
	if m.sessionKeys[it.Identifier] {
		meta = append(meta, stAgent.Render("● agent working"))
	}
	b.WriteString(stMuted.Render(strings.Join(meta, " · ")) + "\n\n")
	if it.Description != "" {
		b.WriteString(wrap.Render(it.Description) + "\n\n")
	} else {
		b.WriteString(stMuted.Render("No description (d to add)") + "\n\n")
	}
	b.WriteString(stBold.Render(fmt.Sprintf("Activity %d", len(it.Comments))) + "\n")
	if len(it.Comments) == 0 {
		b.WriteString(stMuted.Render("  nothing yet") + "\n")
	}
	for i := len(it.Comments) - 1; i >= 0; i-- { // newest first
		c := it.Comments[i]
		who := stAgent.Render("agent")
		if c.Author == "you" {
			who = stAccent.Render("you")
		}
		kind := c.Kind
		switch c.Kind {
		case "handoff":
			kind = lipgloss.NewStyle().Foreground(cWarn).Render("handoff")
		case "done":
			kind = stAgent.Render("done")
		case "cancelled":
			kind = lipgloss.NewStyle().Foreground(cDanger).Render("cancelled")
		}
		b.WriteString(fmt.Sprintf("\n%s · %s · %s\n", who, kind, stMuted.Render(localTime(c.CreatedAt))))
		b.WriteString(lipgloss.NewStyle().Width(w-2).PaddingLeft(2).Render(c.Body) + "\n")
	}
	m.vp.SetContent(b.String())
}

func (m *boardModel) footer() string {
	switch m.mode {
	case modeInput:
		labels := map[string]string{"new": "New issue title", "comment": "Comment on " + m.target,
			"title": "Title of " + m.target, "description": "Description of " + m.target}
		if m.useArea {
			return stBold.Render(labels[m.purpose]) + stMuted.Render("  (ctrl+s save · esc cancel)") + "\n" + m.area.View()
		}
		return stBold.Render(labels[m.purpose]+": ") + m.input.View() + stMuted.Render("  enter save · esc cancel")
	case modeConfirm:
		return lipgloss.NewStyle().Foreground(cDanger).Render("Cancel "+m.target+"?") + stMuted.Render("  y yes · any other key no")
	}
	if m.help {
		return stMuted.Render("←→/hl column  ↑↓/jk card  enter details  n new  c comment  e title  d description\n" +
			"p priority  </> move column  x cancel  a show cancelled  r refresh  q back/quit  ? hide help")
	}
	if m.mode == modeDetail {
		return stMuted.Render("esc back · c comment · e title · d description · p priority · </> move · x cancel · ↑↓ scroll · ? help")
	}
	return stMuted.Render("enter details · n new · c comment · </> move · p priority · x cancel · a cancelled · ? help · q quit")
}

// localTime shows a stored UTC timestamp in the developer's own time zone.
func localTime(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	t = t.Local()
	if y, mo, d := t.Date(); y == time.Now().Year() && mo == time.Now().Month() && d == time.Now().Day() {
		return "today " + t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}
