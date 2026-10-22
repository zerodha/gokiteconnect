package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/devshoe/gokiteconnect/credentials"
)

const (
	fieldUserID         = "User ID"
	fieldProfile        = "Profile"
	fieldChatID         = "Telegram chat ID"
	fieldPassword       = "Password"
	fieldTOTP           = "TOTP secret"
	fieldAPIKey         = "API key"
	fieldAPISecret      = "API secret"
	fieldAuthentication = "Authentication"
	fieldSubscription   = "Data subscription"
)

var authenticationModes = []string{"Web session", "API session"}

// usersScreen lists the stored Kite accounts with the selected one's
// session and, with sudo on, its secrets.
type usersScreen struct {
	grid   grid
	users  []credentials.Credentials
	status string
	sudo   bool
}

func newUsersScreen() *usersScreen {
	return &usersScreen{
		grid: grid{
			columns: []column{
				{title: "USER ID", width: 10},
				{title: "NAME", width: 12, flex: 2},
				{title: "EMAIL", width: 12, flex: 3},
				{title: "BROKER", width: 9},
				{title: "MODE", width: 5},
				{title: "CHAT ID", width: 12},
				{title: "LAST LOGIN", width: 15},
			},
			empty: styleMuted.Render("No users yet · press A to add your first Kite account"),
		},
		status: statusInfo("Loading users…"),
	}
}

func (s *usersScreen) selected() (credentials.Credentials, bool) {
	if user, ok := s.grid.selectedRef().(credentials.Credentials); ok {
		return user, true
	}
	return credentials.Credentials{}, false
}

// setUsers fills the table, keeping selectID (or the selected user) selected.
func (s *usersScreen) setUsers(users []credentials.Credentials, selectID string) {
	if selectID == "" {
		if user, ok := s.selected(); ok {
			selectID = user.UserID
		}
	}
	s.users = users
	rows := make([]row, 0, len(users))
	for _, user := range users {
		mode, modeStyle := "WEB", styleCyan
		if user.IsAPIUser {
			mode, modeStyle = "API", styleKey
		}
		lastLogin := "—"
		if !user.LastLogin.IsZero() {
			lastLogin = user.LastLogin.Local().Format("02 Jan 06 15:04")
		}
		rows = append(rows, row{ref: user, cells: []cell{
			textCell(user.UserID, styleCyan),
			textCell(fallback(user.UserName, "—"), styleText),
			textCell(fallback(user.Email, "—"), styleText),
			textCell(fallback(user.Broker, "—"), styleBlue),
			textCell(mode, modeStyle),
			textCell(fallback(user.TelegramChatID, "—"), styleYellow),
			textCell(lastLogin, styleMuted),
		}})
	}
	s.grid.setRows(rows)
	if selectID != "" {
		s.grid.selectWhere(func(r row) bool {
			user, _ := r.ref.(credentials.Credentials)
			return strings.EqualFold(user.UserID, selectID)
		})
	}
}

func sessionReady(user credentials.Credentials) bool {
	if user.IsAPIUser {
		return user.AccessToken != ""
	}
	return user.EncToken != ""
}

func (s *usersScreen) detail() string {
	user, ok := s.selected()
	if !ok {
		return styleYellow.Bold(true).Render("No users yet") + "\n\nPress " + styleKey.Render("A") + " to add your first Kite account."
	}
	mode := "Web session"
	if user.IsAPIUser {
		mode = "API session"
	}
	session := styleGreen.Bold(true).Render("READY")
	if !sessionReady(user) {
		session = styleYellow.Bold(true).Render("LOGIN NEEDED")
	}
	lastLogin := "Never"
	if !user.LastLogin.IsZero() {
		lastLogin = user.LastLogin.Local().Format(time.RFC822)
	}
	line := func(label, value string) string {
		return styleMuted.Render(padRight(label, 9)) + value
	}
	lines := []string{
		styleCyan.Bold(true).Render(user.UserID),
		"",
		line("Name", styleText.Render(fallback(user.UserName, "—"))),
		line("Email", styleText.Render(fallback(user.Email, "—"))),
		line("Broker", styleBlue.Render(fallback(user.Broker, "—"))),
		"",
		line("Mode", styleText.Render(mode)),
		line("Session", session),
		line("Chat ID", styleYellow.Render(fallback(user.TelegramChatID, "—"))),
		"",
		line("Last in", styleBlue.Render(lastLogin)),
		"",
	}
	if !s.sudo {
		lines = append(lines, styleFaint.Render("Sensitive credentials hidden · toggle Sudo [S] to reveal."))
		return strings.Join(lines, "\n")
	}
	secret := func(label, value string) string {
		return styleMuted.Render(padRight(label, 15)) + styleRed.Render(fallback(value, "—"))
	}
	lines = append(lines,
		styleRed.Bold(true).Render("SENSITIVE DATA · SUDO ENABLED"),
		secret("Password", user.Password),
		secret("TOTP secret", user.TOTPSecret),
		secret("API key", user.APIKey),
		secret("API secret", user.APISecret),
		"",
		secret("Enc token", user.EncToken),
		secret("Request token", user.RequestToken),
		secret("Access token", user.AccessToken),
		secret("KF session", user.KFSessionToken),
		secret("Public token", user.PublicToken),
	)
	return strings.Join(lines, "\n")
}

const usersAbout = "Enter opens the highlighted account's portfolio, E edits it and W opens the watchlists. Click a row to select it; click it again to open it.\n\nREADY means a usable session token is stored. LOGIN NEEDED means the session should be refreshed with F.\n\nB opens Kite web in Chrome, signed in with the stored session. Logging out there ends that session for kite-cli and the ticker too."

func (a *app) usersView(w, h int) string {
	s := a.users
	widths := splitWidth(w, 2, 1)
	if widths[1] < 40 {
		widths = []int{w - min(40, w/2), min(40, w/2)}
	}
	tableW, sideW := widths[0], widths[1]
	s.grid.x, s.grid.y = 1, 3
	info := styleMuted.Render(fmt.Sprintf("%d %s", len(s.users), pluralize("user", len(s.users))))
	table := panel("Users", info, s.grid.view(tableW-2, h-2, true), tableW, h, true)
	detailH := h * 2 / 3
	title := "Selected user"
	if s.sudo {
		title = "Selected user · SUDO"
	}
	side := textPanel(title, "", s.detail(), sideW, detailH, false) + "\n" +
		textPanel("About", "", styleMuted.Render(usersAbout), sideW, h-detailH, false)
	return joinColumns(table, side)
}

// joinColumns sets blocks side by side.
func joinColumns(blocks ...string) string {
	split := make([][]string, len(blocks))
	height := 0
	for index, block := range blocks {
		split[index] = strings.Split(block, "\n")
		height = max(height, len(split[index]))
	}
	lines := make([]string, height)
	for index, block := range split {
		width := 0
		for _, line := range block {
			width = max(width, ansi.StringWidth(line))
		}
		for line := range height {
			text := ""
			if line < len(block) {
				text = block[line]
			}
			if index < len(split)-1 {
				text = padRight(text, width)
			}
			lines[line] += text
		}
	}
	return strings.Join(lines, "\n")
}

func (a *app) usersKey(msg tea.KeyPressMsg) tea.Cmd {
	s := a.users
	key := msg.String()
	if s.grid.update(key) {
		return nil
	}
	switch strings.ToLower(key) {
	case "enter":
		return a.openSelectedPortfolio()
	case "a", "n":
		a.openUserForm(credentials.Credentials{}, false)
	case "e":
		if user, ok := s.selected(); ok {
			a.openUserForm(user, true)
		} else {
			a.showError(errNoUser)
		}
	case "d":
		a.confirmDeleteUser()
	case "f":
		return a.refreshSession()
	case "b":
		return a.openKiteWeb()
	case "r":
		return a.loadUsers("", "")
	case "s":
		s.sudo = !s.sudo
		if s.sudo {
			s.status = statusErr("SUDO ENABLED", "· raw credentials are visible in the selected-user panel")
		} else {
			s.status = statusInfo("Sudo disabled · sensitive credentials are hidden")
		}
	case "esc":
		if a.portfolio.user.UserID != "" {
			a.show(screenPortfolio)
		}
	}
	return nil
}

func (a *app) openSelectedPortfolio() tea.Cmd {
	user, ok := a.users.selected()
	if !ok {
		a.showError(errNoUser)
		return nil
	}
	return a.openPortfolio(user)
}

type usersLoadedMsg struct {
	users    []credentials.Credentials
	err      error
	selectID string
	// success is the finished operation's message.
	success string
}

// userOpDoneMsg reports a create, update, delete or session refresh.
type userOpDoneMsg struct {
	err      error
	success  string
	selectID string
}

// loadUsers reads the stored users, then selects selectID and reports
// success.
func (a *app) loadUsers(selectID, success string) tea.Cmd {
	return a.task(func(ctx context.Context) tea.Msg {
		users, err := a.deps.manager.ListUsers(ctx)
		return usersLoadedMsg{users: users, err: err, selectID: selectID, success: success}
	})
}

func (a *app) handleUsers(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case usersLoadedMsg:
		s := a.users
		if msg.err != nil {
			s.status = statusErr("Reload failed:", msg.err.Error())
			if a.isBusy() {
				a.closeOverlay()
			}
			a.showError(msg.err)
			return nil, true
		}
		s.setUsers(msg.users, msg.selectID)
		s.status = statusInfo(fmt.Sprintf("%d %s stored", len(msg.users), pluralize("user", len(msg.users))))
		if msg.success != "" {
			s.status = statusOK(msg.success)
			if a.isBusy() {
				a.closeOverlay()
			}
		}
		if !a.openedDefault {
			a.openedDefault = true
			return a.openDefaultUser(), true
		}
		return nil, true
	case userOpDoneMsg:
		if msg.err != nil {
			a.closeOverlay()
			a.showError(msg.err)
			return nil, true
		}
		return a.loadUsers(msg.selectID, msg.success), true
	}
	return nil, false
}

// openDefaultUser opens the default user's portfolio at start. Without one
// stored, the app stays on the users screen.
func (a *app) openDefaultUser() tea.Cmd {
	wanted := a.deps.defaultUser
	if wanted == "" {
		return nil
	}
	for _, user := range a.users.users {
		if strings.EqualFold(user.UserID, wanted) {
			a.users.setUsers(a.users.users, user.UserID)
			return a.openPortfolio(user)
		}
	}
	a.users.status = statusWarn(fmt.Sprintf("Default user %s is not stored", wanted)) + statusInfo(" · pick a user")
	return nil
}

// userOperation runs op behind the busy dialog.
func (a *app) userOperation(message, success, selectID string, op func(context.Context) error) tea.Cmd {
	return tea.Batch(a.busy(message), a.task(func(ctx context.Context) tea.Msg {
		return userOpDoneMsg{err: op(ctx), success: success, selectID: selectID}
	}))
}

func (a *app) confirmDeleteUser() {
	user, ok := a.users.selected()
	if !ok {
		a.showError(errNoUser)
		return
	}
	a.confirm("Delete user",
		fmt.Sprintf("Delete %s?\nThis removes the stored credentials and session tokens.", styleCyan.Render(user.UserID)),
		"Delete",
		func(a *app) tea.Cmd {
			return a.userOperation("Deleting user…", "User deleted.", "", func(ctx context.Context) error {
				return a.deps.manager.DeleteUser(ctx, user.UserID)
			})
		})
}

func (a *app) refreshSession() tea.Cmd {
	user, ok := a.users.selected()
	if !ok {
		a.showError(errNoUser)
		return nil
	}
	return a.userOperation("Refreshing Kite session…", "Session refreshed and persisted.", user.UserID, func(ctx context.Context) error {
		_, err := a.deps.manager.RefreshUserSession(ctx, user.UserID)
		return err
	})
}

// userForm adds or edits a user.
type userForm struct {
	form     *form
	original credentials.Credentials
	editing  bool
}

func (a *app) openUserForm(user credentials.Credentials, editing bool) {
	f := newForm(18, 34, "Save", "Cancel")
	if editing {
		f.addInput(fieldUserID, user.UserID, anyText, false).disabled = true
		f.addNote(fieldProfile, fmt.Sprintf("Name: %s\nEmail: %s\nBroker: %s",
			fallback(user.UserName, "Name pending profile"), fallback(user.Email, "Email pending profile"), fallback(user.Broker, "ZERODHA")))
		a.users.status = statusInfo("Blank password/API fields keep the existing secret. Escape cancels.")
	} else {
		f.addInput(fieldUserID, "", anyText, false)
		f.addNote(fieldProfile, "Name, email, and broker (ZERODHA) are populated from the Kite profile after login.")
		a.users.status = statusInfo("Enter credentials, then Save to authenticate and persist the account.")
	}
	f.addInput(fieldChatID, user.TelegramChatID, anyText, false)
	f.addInput(fieldPassword, "", anyText, true)
	f.addInput(fieldTOTP, "", anyText, true)
	mode := 0
	if user.IsAPIUser {
		mode = 1
	}
	f.addSelect(fieldAuthentication, authenticationModes, mode)
	f.addInput(fieldAPIKey, "", anyText, true)
	f.addInput(fieldAPISecret, "", anyText, true)
	f.addToggle(fieldSubscription, user.DataSubscription)
	overlay := &userForm{form: f, original: user, editing: editing}
	overlay.syncAPI()
	f.focusFirst()
	a.overlay = overlay
}

func (u *userForm) isAPI() bool {
	return u.form.field(fieldAuthentication).value() == authenticationModes[1]
}

// syncAPI shows the API key and secret only for API sessions.
func (u *userForm) syncAPI() {
	hidden := !u.isAPI()
	u.form.field(fieldAPIKey).hidden = hidden
	u.form.field(fieldAPISecret).hidden = hidden
}

func (u *userForm) update(a *app, msg tea.KeyPressMsg) tea.Cmd {
	switch key := msg.String(); {
	case key == "esc":
		a.closeOverlay()
		return nil
	}
	pressed, _ := u.form.update(msg)
	u.syncAPI()
	switch pressed {
	case 0:
		return u.submit(a)
	case 1:
		a.closeOverlay()
	}
	return nil
}

func (u *userForm) submit(a *app) tea.Cmd {
	if u.editing {
		update, err := u.updateRequest()
		if err != nil {
			u.form.message = styleRed.Render(err.Error())
			return nil
		}
		return a.userOperation("Saving user and refreshing authentication…", "User updated.", update.UserID, func(ctx context.Context) error {
			return a.deps.manager.UpdateUser(ctx, update)
		})
	}
	user, err := u.credentials()
	if err != nil {
		u.form.message = styleRed.Render(err.Error())
		return nil
	}
	return a.userOperation("Authenticating with Kite and saving user…", "User created and authenticated.", user.UserID, func(ctx context.Context) error {
		return a.deps.manager.CreateUser(ctx, user)
	})
}

func (u *userForm) text(label string) string {
	if field := u.form.field(label); !field.hidden {
		return field.value()
	}
	return ""
}

func (u *userForm) credentials() (credentials.Credentials, error) {
	userID, password, totp := u.text(fieldUserID), u.text(fieldPassword), u.text(fieldTOTP)
	if userID == "" || password == "" || totp == "" {
		return credentials.Credentials{}, errors.New("user ID, password, and TOTP secret are required")
	}
	isAPI := u.isAPI()
	apiKey, apiSecret := u.text(fieldAPIKey), u.text(fieldAPISecret)
	if isAPI && (apiKey == "" || apiSecret == "") {
		return credentials.Credentials{}, errors.New("API key and API secret are required for API authentication")
	}
	return credentials.Credentials{
		UserID:           userID,
		TelegramChatID:   u.text(fieldChatID),
		Password:         password,
		TOTPSecret:       totp,
		APIKey:           apiKey,
		APISecret:        apiSecret,
		IsAPIUser:        isAPI,
		DataSubscription: u.form.field(fieldSubscription).on,
	}, nil
}

// updateRequest changes only what the form changed; blank secrets keep the
// stored ones.
func (u *userForm) updateRequest() (credentials.CredentialsUpdate, error) {
	original := u.original
	update := credentials.CredentialsUpdate{UserID: original.UserID}
	chatID := u.text(fieldChatID)
	update.TelegramChatID = &chatID
	if password := u.text(fieldPassword); password != "" {
		update.Password = &password
	}
	if totp := u.text(fieldTOTP); totp != "" {
		update.TOTPSecret = &totp
	}
	if apiKey := u.text(fieldAPIKey); apiKey != "" {
		update.APIKey = &apiKey
	}
	if apiSecret := u.text(fieldAPISecret); apiSecret != "" {
		update.APISecret = &apiSecret
	}
	isAPI := u.isAPI()
	if isAPI != original.IsAPIUser {
		update.IsAPIUser = &isAPI
	}
	if subscription := u.form.field(fieldSubscription).on; subscription != original.DataSubscription {
		update.DataSubscription = &subscription
	}
	if isAPI {
		apiKey, apiSecret := original.APIKey, original.APISecret
		if update.APIKey != nil {
			apiKey = *update.APIKey
		}
		if update.APISecret != nil {
			apiSecret = *update.APISecret
		}
		if apiKey == "" || apiSecret == "" {
			return credentials.CredentialsUpdate{}, errors.New("API key and API secret are required for API authentication")
		}
	}
	return update, nil
}

func (u *userForm) view(_ *app, w, _ int) string {
	title := "Add user"
	if u.editing {
		title = "Edit user"
	}
	width := min(64, w-2)
	return modal(title, styleFaint.Render("Esc cancels"), u.form.view(width-4), width)
}

// openUserPicker lists the stored users to switch to; Enter opens the
// chosen user's portfolio.
func (a *app) openUserPicker() {
	users := a.users.users
	if len(users) == 0 {
		a.notify(statusWarn("No users stored") + statusInfo(" · press A to add one"))
		return
	}
	picker := &menu{title: "Switch user", info: styleFaint.Render("Enter opens · Esc cancels"), width: 56}
	for index, user := range users {
		mode := "WEB"
		if user.IsAPIUser {
			mode = "API"
		}
		chosen := user
		label := styleCyan.Render(fmt.Sprintf("%-10s", user.UserID)) + " " +
			styleText.Render(fmt.Sprintf("%-20s", truncate(fallback(user.UserName, "—"), 20))) + " " + styleFaint.Render(mode)
		picker.items = append(picker.items, menuItem{label: label, run: func(a *app) tea.Cmd {
			a.users.setUsers(a.users.users, chosen.UserID)
			return a.openPortfolio(chosen)
		}})
		if user.UserID == a.activeUser() {
			picker.cursor = index
		}
	}
	a.overlay = picker
}
