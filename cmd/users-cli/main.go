// Command users provides an interactive terminal dashboard for the local
// credentials store.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	kiteconnect "github.com/devshoe/gokiteconnect"
	"github.com/devshoe/gokiteconnect/credentials"
	credentialstore "github.com/devshoe/gokiteconnect/credentials/repository"
	"github.com/gdamore/tcell/v2"
	_ "github.com/mattn/go-sqlite3"
	"github.com/rivo/tview"
)

const (
	dashboardPage = "dashboard"
	formPage      = "form"
	modalPage     = "modal"
	busyPage      = "busy"
)

const (
	fieldUserID         = "User ID"
	fieldChatID         = "Telegram chat ID"
	fieldPassword       = "Password"
	fieldTOTP           = "TOTP secret"
	fieldAPIKey         = "API key"
	fieldAPISecret      = "API secret"
	fieldAuthentication = "Authentication"
	fieldSubscription   = "Data subscription"
)

type userApp struct {
	application *tview.Application
	pages       *tview.Pages
	manager     *credentials.UserManager
	ctx         context.Context
	database    string

	table      *tview.Table
	detail     *tview.TextView
	status     *tview.TextView
	sudoButton *tview.Button

	users   []credentials.Credentials
	overlay string
	busy    bool
	sudo    bool
}

func main() {
	settings, err := kiteconnect.LoadConfig()
	if err != nil {
		fatal(err)
	}
	databaseFlag := flag.String("db", settings.CredentialsPath, "SQLite database used for the credentials store")
	flag.Parse()

	if err := os.MkdirAll(filepath.Dir(*databaseFlag), 0o700); err != nil {
		fatal(fmt.Errorf("create credentials directory: %w", err))
	}
	database, err := sql.Open("sqlite3", *databaseFlag)
	if err != nil {
		fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	store, err := credentialstore.NewUserSQLiteRepository(database)
	if err != nil {
		fatal(err)
	}
	manager, err := credentials.NewUserManager(store)
	if err != nil {
		fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ui := newUserApp(ctx, manager, *databaseFlag)
	if err := ui.reload(); err != nil {
		fatal(err)
	}
	if err := ui.application.Run(); err != nil && !errors.Is(err, context.Canceled) {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "users: %v\n", err)
	os.Exit(1)
}

func newUserApp(ctx context.Context, manager *credentials.UserManager, database string) *userApp {
	configureTheme()
	ui := &userApp{
		application: tview.NewApplication().EnableMouse(true),
		pages:       tview.NewPages(),
		manager:     manager,
		ctx:         ctx,
		database:    database,
	}
	ui.table = ui.newTable()
	ui.detail = ui.newDetail()
	actions := ui.newActions()
	ui.status = tview.NewTextView().
		SetDynamicColors(true).
		SetTextColor(tcell.ColorLightCyan).
		SetText("  [yellow]↑↓[-] navigate  [green]Enter[-] edit  [a] add  [d] delete  [f] refresh session  [r] reload  [s] sudo  [q] quit")
	ui.status.SetBorder(true).SetBorderColor(tcell.ColorLightCyan)

	header := tview.NewTextView().
		SetDynamicColors(true).
		SetText("[aqua::b]◆[-] [white::b]CREDENTIALS[-] [lightblue::b]USER CONSOLE[-]  [lightcyan]Local Kite session control[-]").
		SetTextAlign(tview.AlignCenter)
	header.SetBorder(true).SetBorderColor(tcell.ColorLightCyan)
	databaseView := tview.NewTextView().
		SetDynamicColors(true).
		SetWrap(false).
		SetWordWrap(false).
		SetTextAlign(tview.AlignRight).
		SetText(fmt.Sprintf("[yellow::b]DB[-] %s", tview.Escape(ui.database)))
	databaseView.SetBorder(true).SetBorderColor(tcell.ColorLightYellow).SetTitle(" Connected database ").SetTitleColor(tcell.ColorLightYellow)
	headerRow := tview.NewFlex().SetDirection(tview.FlexColumn).
		// Keep both sides visible even in a narrow terminal. A proportional
		// database pane avoids the fixed-width pane being pushed off-screen.
		AddItem(header, 0, 3, false).
		AddItem(databaseView, 0, 2, false)

	help := tview.NewTextView().
		SetDynamicColors(true).
		SetWrap(true).
		SetText("[aqua::b]Selected user[-]\n\nUse the arrow keys to move through the table, or use the action buttons below it. Press Enter to edit the highlighted account.\n\n[green]READY[-] means a usable session token is stored. [yellow]LOGIN NEEDED[-] means the session should be refreshed.")
	help.SetBorder(true).SetTitle("About")
	help.SetTitleColor(tcell.ColorLightPink)

	rightPanel := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(ui.detail, 0, 2, false).
		AddItem(help, 0, 1, false)
	body := tview.NewFlex().
		AddItem(ui.table, 0, 4, true).
		AddItem(rightPanel, 36, 1, false)
	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(headerRow, 3, 0, false).
		AddItem(body, 0, 1, true).
		AddItem(actions, 3, 0, false).
		AddItem(ui.status, 2, 0, false)
	ui.pages.AddPage(dashboardPage, root, true, true)
	ui.application.SetRoot(ui.pages, true).SetInputCapture(ui.capture)
	return ui
}

func (ui *userApp) newActions() *tview.Flex {
	actions := tview.NewFlex().SetDirection(tview.FlexColumn)
	actions.SetBorder(true).SetBorderColor(tcell.ColorDarkCyan).SetTitle(" Actions ").SetTitleColor(tcell.ColorLightYellow)
	actions.AddItem(actionButton("＋ Add new [A]", tcell.ColorLightGreen, ui.openCreate), 0, 1, false)
	actions.AddItem(actionButton("✎ Edit [E]", tcell.ColorLightCyan, ui.openEdit), 0, 1, false)
	actions.AddItem(actionButton("✖ Delete [D]", tcell.ColorLightCoral, ui.confirmDelete), 0, 1, false)
	actions.AddItem(actionButton("↻ Refresh session [F]", tcell.ColorLightYellow, ui.refreshSession), 0, 1, false)
	actions.AddItem(actionButton("⟳ Reload [R]", tcell.ColorLightSkyBlue, func() {
		if err := ui.reload(); err != nil {
			ui.showError(err)
		}
	}), 0, 1, false)
	ui.sudoButton = actionButton("⚠ Sudo OFF [S]", tcell.ColorLightYellow, ui.toggleSudo)
	actions.AddItem(ui.sudoButton, 0, 1, false)
	actions.AddItem(actionButton("⏻ Quit [Q]", tcell.ColorLightPink, ui.application.Stop), 0, 1, false)
	return actions
}

func actionButton(label string, color tcell.Color, selected func()) *tview.Button {
	return tview.NewButton(tview.Escape(label)).
		SetLabelColor(color).
		SetLabelColorActivated(tcell.ColorBlack).
		SetActivatedStyle(tcell.StyleDefault.Background(color).Foreground(tcell.ColorBlack).Bold(true)).
		SetSelectedFunc(selected)
}

func configureTheme() {
	tview.Styles = tview.Theme{
		PrimitiveBackgroundColor:    tcell.ColorBlack,
		ContrastBackgroundColor:     tcell.ColorDarkBlue,
		MoreContrastBackgroundColor: tcell.ColorDarkCyan,
		BorderColor:                 tcell.ColorLightCyan,
		TitleColor:                  tcell.ColorLightYellow,
		GraphicsColor:               tcell.ColorLightCyan,
		PrimaryTextColor:            tcell.ColorWhite,
		SecondaryTextColor:          tcell.ColorLightYellow,
		TertiaryTextColor:           tcell.ColorLightSkyBlue,
		InverseTextColor:            tcell.ColorBlack,
		ContrastSecondaryTextColor:  tcell.ColorWhite,
	}
}

func (ui *userApp) newTable() *tview.Table {
	table := tview.NewTable().
		SetBorders(true).
		SetBordersColor(tcell.ColorLightCyan).
		SetSelectable(true, false).
		SetFixed(1, 0).
		SetEvaluateAllRows(true).
		SetSelectedStyle(tcell.StyleDefault.Background(tcell.ColorBlue).Foreground(tcell.ColorWhite).Bold(true))
	table.SetTitle(" Users ").SetTitleColor(tcell.ColorLightCyan)
	table.SetSelectedFunc(func(row, _ int) {
		if row > 0 {
			ui.openEdit()
		}
	})
	table.SetSelectionChangedFunc(func(row, _ int) {
		if row > 0 && row-1 < len(ui.users) {
			ui.updateDetail(ui.users[row-1])
		}
	})
	return table
}

func (ui *userApp) newDetail() *tview.TextView {
	detail := tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	detail.SetBorder(true).SetTitle(" Selected user ").SetTitleColor(tcell.ColorLightPink)
	detail.SetText("Move to a user to inspect its session and profile.")
	return detail
}

func (ui *userApp) capture(event *tcell.EventKey) *tcell.EventKey {
	if event.Key() == tcell.KeyCtrlC {
		ui.application.Stop()
		return nil
	}
	if ui.busy {
		return nil
	}
	if ui.overlay != "" {
		return event
	}
	if event.Key() != tcell.KeyRune {
		return event
	}
	switch strings.ToLower(string(event.Rune())) {
	case "q":
		ui.application.Stop()
		return nil
	case "a", "n":
		ui.openCreate()
		return nil
	case "e":
		ui.openEdit()
		return nil
	case "d":
		ui.confirmDelete()
		return nil
	case "f":
		ui.refreshSession()
		return nil
	case "r":
		if err := ui.reload(); err != nil {
			ui.showError(err)
		}
		return nil
	case "s":
		ui.toggleSudo()
		return nil
	}
	return event
}

func (ui *userApp) reload() error {
	users, err := ui.manager.ListUsers(ui.ctx)
	if err != nil {
		return err
	}
	ui.users = users
	ui.table.Clear()
	headers := []string{"USER ID", "NAME", "EMAIL", "BROKER", "MODE", "CHAT ID", "LAST LOGIN"}
	for column, header := range headers {
		ui.table.SetCell(0, column, tview.NewTableCell(header).
			SetTextColor(tcell.ColorLightCyan).
			SetAttributes(tcell.AttrBold).
			SetSelectable(false))
	}
	for row, user := range users {
		mode := "WEB"
		modeColor := tcell.ColorLightCyan
		if user.IsAPIUser {
			mode = "API"
			modeColor = tcell.ColorLightPink
		}
		lastLogin := "—"
		if !user.LastLogin.IsZero() {
			lastLogin = user.LastLogin.Local().Format("02 Jan 06 15:04")
		}
		values := []string{
			user.UserID,
			fallback(user.UserName, "—"),
			fallback(user.Email, "—"),
			fallback(user.Broker, "—"),
			mode,
			fallback(user.TelegramChatID, "—"),
			lastLogin,
		}
		colors := []tcell.Color{tcell.ColorLightCyan, tcell.ColorWhite, tcell.ColorWhite, tcell.ColorLightBlue, modeColor, tcell.ColorYellow, tcell.ColorLightGray}
		for column, value := range values {
			ui.table.SetCell(row+1, column, tview.NewTableCell(truncate(value, 24)).SetTextColor(colors[column]))
		}
	}
	if len(users) > 0 {
		ui.table.Select(1, 0)
		ui.updateDetail(users[0])
	} else {
		ui.detail.SetText("[yellow::b]No users yet[-]\n\nPress [green]A[-] to add your first Kite account.")
	}
	ui.setStatus(fmt.Sprintf("  %d %s stored", len(users), pluralize("user", len(users))))
	return nil
}

func (ui *userApp) updateDetail(user credentials.Credentials) {
	mode := "Web session"
	if user.IsAPIUser {
		mode = "API session"
	}
	session := "READY"
	if (user.IsAPIUser && user.AccessToken == "") || (!user.IsAPIUser && user.EncToken == "") {
		session = "LOGIN NEEDED"
	}
	lastLogin := "Never"
	if !user.LastLogin.IsZero() {
		lastLogin = user.LastLogin.Local().Format(time.RFC822)
	}
	detail := fmt.Sprintf("[aqua::b]%s[-]\n\n[white]Name[-]    %s\n[white]Email[-]   %s\n[white]Broker[-]  %s\n\n[white]Mode[-]    %s\n[white]Session[-] %s\n[white]Chat ID[-] %s\n\n[lightskyblue]Last login  %s[-]",
		tview.Escape(user.UserID),
		tview.Escape(fallback(user.UserName, "—")),
		tview.Escape(fallback(user.Email, "—")),
		tview.Escape(fallback(user.Broker, "—")),
		mode,
		session,
		tview.Escape(fallback(user.TelegramChatID, "—")),
		tview.Escape(lastLogin))
	if ui.sudo {
		detail += fmt.Sprintf("\n\n[red::b]SENSITIVE DATA · SUDO ENABLED[-]\n[white]Password[-]       %s\n[white]TOTP secret[-]    %s\n[white]API key[-]        %s\n[white]API secret[-]     %s\n\n[white]Enc token[-]      %s\n[white]Request token[-]  %s\n[white]Access token[-]   %s\n[white]KF session[-]     %s\n[white]Public token[-]   %s",
			sensitiveValue(user.Password),
			sensitiveValue(user.TOTPSecret),
			sensitiveValue(user.APIKey),
			sensitiveValue(user.APISecret),
			sensitiveValue(user.EncToken),
			sensitiveValue(user.RequestToken),
			sensitiveValue(user.AccessToken),
			sensitiveValue(user.KFSessionToken),
			sensitiveValue(user.PublicToken))
	} else {
		detail += "\n\n[gray]Sensitive credentials hidden · toggle Sudo [S] to reveal.[-]"
	}
	ui.detail.SetText(detail)
}

func (ui *userApp) toggleSudo() {
	ui.sudo = !ui.sudo
	if ui.sudo {
		ui.sudoButton.SetLabel(tview.Escape("⚠ Sudo ON [S]")).SetLabelColor(tcell.ColorRed)
		ui.setStatus("  [red::b]SUDO ENABLED[-] · raw credentials are visible in the selected-user panel")
	} else {
		ui.sudoButton.SetLabel(tview.Escape("⚠ Sudo OFF [S]")).SetLabelColor(tcell.ColorLightYellow)
		ui.setStatus("  Sudo disabled · sensitive credentials are hidden")
	}
	if user, ok := ui.selectedUser(); ok {
		ui.updateDetail(user)
	}
}

func sensitiveValue(value string) string {
	return tview.Escape(fallback(value, "—"))
}

func (ui *userApp) openCreate() {
	ui.openForm(credentials.Credentials{}, false)
}

func (ui *userApp) openEdit() {
	user, ok := ui.selectedUser()
	if !ok {
		ui.showError(errors.New("select a user first"))
		return
	}
	ui.openForm(user, true)
}

func (ui *userApp) selectedUser() (credentials.Credentials, bool) {
	row, _ := ui.table.GetSelection()
	if row <= 0 || row-1 >= len(ui.users) {
		return credentials.Credentials{}, false
	}
	return ui.users[row-1], true
}

func (ui *userApp) openForm(user credentials.Credentials, editing bool) {
	form := tview.NewForm().SetItemPadding(1).SetButtonsAlign(tview.AlignCenter)
	form.SetBorder(true).SetBorderColor(tcell.ColorLightCyan)
	if editing {
		form.SetTitle(" Edit user ").SetTitleColor(tcell.ColorLightPink)
		form.AddInputField(fieldUserID, user.UserID, 32, nil, nil)
		form.GetFormItemByLabel(fieldUserID).(*tview.InputField).SetDisabled(true)
		form.AddTextView("Profile", fmt.Sprintf("Name: %s\nEmail: %s\nBroker: %s", fallback(user.UserName, "Name pending profile"), fallback(user.Email, "Email pending profile"), fallback(user.Broker, "ZERODHA")), 32, 3, false, false)
		form.AddInputField(fieldChatID, user.TelegramChatID, 32, nil, nil)
		form.AddPasswordField(fieldPassword, "", 32, '*', nil)
		form.AddPasswordField(fieldTOTP, "", 32, '*', nil)
	} else {
		form.SetTitle(" Add user ").SetTitleColor(tcell.ColorLightPink)
		form.AddInputField(fieldUserID, "", 32, nil, nil)
		form.AddTextView("Profile", "Name, email, and broker (ZERODHA) are populated from the Kite profile after login.", 32, 2, false, false)
		form.AddInputField(fieldChatID, "", 32, nil, nil)
		form.AddPasswordField(fieldPassword, "", 32, '*', nil)
		form.AddPasswordField(fieldTOTP, "", 32, '*', nil)
	}
	modeIndex := 0
	if user.IsAPIUser {
		modeIndex = 1
	}
	form.AddDropDown(fieldAuthentication, []string{"Web session", "API session"}, modeIndex, func(option string, _ int) {
		syncAPICredentialFields(form, option == "API session")
	})
	if user.IsAPIUser {
		syncAPICredentialFields(form, true)
	}
	form.AddCheckbox(fieldSubscription, user.DataSubscription, nil)
	form.AddButton("Save", func() { ui.submitForm(form, user, editing) })
	form.AddButton("Cancel", ui.closeOverlay)
	form.SetCancelFunc(ui.closeOverlay)

	ui.overlay = formPage
	ui.pages.AddAndSwitchToPage(formPage, form, true)
	ui.application.SetFocus(form)
	if editing {
		ui.setStatus("  Blank password/API fields keep the existing secret. Escape cancels.")
	} else {
		ui.setStatus("  Enter credentials, then Save to authenticate and persist the account.")
	}
}

func syncAPICredentialFields(form *tview.Form, isAPIUser bool) {
	apiKeyIndex := form.GetFormItemIndex(fieldAPIKey)
	apiSecretIndex := form.GetFormItemIndex(fieldAPISecret)
	if isAPIUser {
		if apiKeyIndex < 0 {
			form.AddPasswordField(fieldAPIKey, "", 32, '*', nil)
		}
		if apiSecretIndex < 0 {
			form.AddPasswordField(fieldAPISecret, "", 32, '*', nil)
		}
		return
	}
	// Remove in reverse order so the second removal does not invalidate the
	// index of the first field.
	if apiSecretIndex >= 0 {
		form.RemoveFormItem(apiSecretIndex)
	}
	if apiKeyIndex >= 0 {
		form.RemoveFormItem(form.GetFormItemIndex(fieldAPIKey))
	}
}

func (ui *userApp) submitForm(form *tview.Form, original credentials.Credentials, editing bool) {
	if editing {
		update, err := formUpdate(form, original)
		if err != nil {
			ui.showError(err)
			return
		}
		ui.showBusy("Saving user and refreshing authentication…")
		go func() {
			err := ui.manager.UpdateUser(ui.ctx, update)
			ui.application.QueueUpdateDraw(func() {
				ui.finishOperation(err, "User updated.")
			})
		}()
		return
	}
	newUser, err := formCredentials(form)
	if err != nil {
		ui.showError(err)
		return
	}
	ui.showBusy("Authenticating with Kite and saving user…")
	go func() {
		err := ui.manager.CreateUser(ui.ctx, newUser)
		ui.application.QueueUpdateDraw(func() {
			ui.finishOperation(err, "User created and authenticated.")
		})
	}()
}

func formCredentials(form *tview.Form) (credentials.Credentials, error) {
	userID := formText(form, fieldUserID)
	password := formText(form, fieldPassword)
	totp := formText(form, fieldTOTP)
	if userID == "" || password == "" || totp == "" {
		return credentials.Credentials{}, errors.New("user ID, password, and TOTP secret are required")
	}
	_, mode := form.GetFormItemByLabel(fieldAuthentication).(*tview.DropDown).GetCurrentOption()
	isAPI := mode == "API session"
	apiKey := formText(form, fieldAPIKey)
	apiSecret := formText(form, fieldAPISecret)
	if isAPI && (apiKey == "" || apiSecret == "") {
		return credentials.Credentials{}, errors.New("API key and API secret are required for API authentication")
	}
	return credentials.Credentials{
		UserID:           userID,
		TelegramChatID:   formText(form, fieldChatID),
		Password:         password,
		TOTPSecret:       totp,
		APIKey:           apiKey,
		APISecret:        apiSecret,
		IsAPIUser:        isAPI,
		DataSubscription: form.GetFormItemByLabel(fieldSubscription).(*tview.Checkbox).IsChecked(),
	}, nil
}

func formUpdate(form *tview.Form, original credentials.Credentials) (credentials.CredentialsUpdate, error) {
	update := credentials.CredentialsUpdate{UserID: original.UserID}
	chatID := formText(form, fieldChatID)
	update.TelegramChatID = &chatID
	if password := formText(form, fieldPassword); password != "" {
		update.Password = &password
	}
	if totp := formText(form, fieldTOTP); totp != "" {
		update.TOTPSecret = &totp
	}
	if apiKey := formText(form, fieldAPIKey); apiKey != "" {
		update.APIKey = &apiKey
	}
	if apiSecret := formText(form, fieldAPISecret); apiSecret != "" {
		update.APISecret = &apiSecret
	}
	_, mode := form.GetFormItemByLabel(fieldAuthentication).(*tview.DropDown).GetCurrentOption()
	isAPI := mode == "API session"
	if isAPI != original.IsAPIUser {
		update.IsAPIUser = &isAPI
	}
	subscription := form.GetFormItemByLabel(fieldSubscription).(*tview.Checkbox).IsChecked()
	if subscription != original.DataSubscription {
		update.DataSubscription = &subscription
	}
	if isAPI {
		apiKeyValue := original.APIKey
		if update.APIKey != nil {
			apiKeyValue = *update.APIKey
		}
		apiSecretValue := original.APISecret
		if update.APISecret != nil {
			apiSecretValue = *update.APISecret
		}
		if apiKeyValue == "" || apiSecretValue == "" {
			return credentials.CredentialsUpdate{}, errors.New("API key and API secret are required for API authentication")
		}
	}
	return update, nil
}

func formText(form *tview.Form, label string) string {
	field, ok := form.GetFormItemByLabel(label).(*tview.InputField)
	if !ok {
		return ""
	}
	return strings.TrimSpace(field.GetText())
}

func (ui *userApp) confirmDelete() {
	user, ok := ui.selectedUser()
	if !ok {
		ui.showError(errors.New("select a user first"))
		return
	}
	modal := tview.NewModal().
		SetText(fmt.Sprintf("Delete [aqua]%s[-]?\nThis removes the stored credentials and session tokens.", user.UserID)).
		SetTextColor(tcell.ColorWhite).
		AddButtons([]string{"Delete", "Cancel"}).
		SetDoneFunc(func(index int, _ string) {
			ui.closeOverlay()
			if index == 0 {
				ui.deleteUser(user.UserID)
			}
		})
	ui.overlay = modalPage
	ui.pages.AddAndSwitchToPage(modalPage, modal, true)
	ui.application.SetFocus(modal)
}

func (ui *userApp) deleteUser(userID string) {
	ui.showBusy("Deleting user…")
	go func() {
		err := ui.manager.DeleteUser(ui.ctx, userID)
		ui.application.QueueUpdateDraw(func() {
			ui.finishOperation(err, "User deleted.")
		})
	}()
}

func (ui *userApp) refreshSession() {
	user, ok := ui.selectedUser()
	if !ok {
		ui.showError(errors.New("select a user first"))
		return
	}
	ui.showBusy("Refreshing Kite session…")
	go func() {
		_, err := ui.manager.RefreshUserSession(ui.ctx, user.UserID)
		ui.application.QueueUpdateDraw(func() {
			ui.finishOperation(err, "Session refreshed and persisted.")
		})
	}()
}

func (ui *userApp) finishOperation(err error, success string) {
	ui.closeOverlay()
	ui.busy = false
	if err != nil {
		ui.showError(err)
		return
	}
	if reloadErr := ui.reload(); reloadErr != nil {
		ui.showError(reloadErr)
		return
	}
	ui.setStatus("  [green::b]✔[-] " + success)
}

func (ui *userApp) showBusy(message string) {
	ui.busy = true
	view := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	view.SetText("\n\n[aqua::b]◆[-]\n\n" + message + "\n\n[lightcyan]Please wait…[-]")
	view.SetBorder(true).SetTitle(" Working ").SetTitleColor(tcell.ColorLightCyan)
	ui.overlay = busyPage
	ui.pages.AddAndSwitchToPage(busyPage, view, true)
	ui.application.SetFocus(view)
}

func (ui *userApp) showError(err error) {
	if ui.busy {
		ui.busy = false
		ui.closeOverlay()
	} else if ui.overlay != "" {
		ui.closeOverlay()
	}
	modal := tview.NewModal().
		SetText("[red::b]Operation failed[-]\n\n" + err.Error()).
		AddButtons([]string{"OK"}).
		SetDoneFunc(func(int, string) { ui.closeOverlay() })
	ui.overlay = modalPage
	ui.pages.AddAndSwitchToPage(modalPage, modal, true)
	ui.application.SetFocus(modal)
}

func (ui *userApp) closeOverlay() {
	if ui.overlay != "" {
		ui.pages.RemovePage(ui.overlay)
	}
	ui.overlay = ""
	ui.application.SetFocus(ui.table)
}

func (ui *userApp) setStatus(message string) {
	ui.status.SetText(message)
}

func fallback(value, replacement string) string {
	if value == "" {
		return replacement
	}
	return value
}

func truncate(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width < 2 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}

func pluralize(noun string, count int) string {
	if count == 1 {
		return noun
	}
	return noun + "s"
}
