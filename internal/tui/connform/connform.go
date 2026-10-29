// Package connform is the create/edit connection form and the password
// prompt, both built with Huh.
package connform

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/guilhermesouza/dbui/internal/config"
	"github.com/guilhermesouza/dbui/internal/tui/styles"
)

// SubmitMsg is emitted when the connection form is completed. Password is
// only meaningful when PasswordChanged is true (editing keeps the stored
// password when the field is left empty).
type SubmitMsg struct {
	OldName         string
	Conn            config.Connection
	Password        string
	PasswordChanged bool
}

// PasswordMsg is emitted by the password prompt.
type PasswordMsg struct {
	Name     string
	Password string
}

// CancelMsg is emitted when a form is aborted with esc.
type CancelMsg struct{}

var sslModes = []string{"prefer", "disable", "allow", "require", "verify-ca", "verify-full"}

type fields struct {
	name, host, port, user, password, database, sslmode string
	readOnly, production                                bool
}

// Model wraps a huh form.
type Model struct {
	form     *huh.Form
	title    string
	data     *fields
	oldName  string
	password bool // true for the password prompt
}

func keymap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))
	return km
}

// New builds the connection form. existing is nil for a new connection;
// taken reports whether a name is already used by another connection.
func New(existing *config.Connection, taken func(string) bool) Model {
	d := &fields{host: "localhost", port: "5432", user: "postgres", database: "postgres", sslmode: "prefer"}
	title := "New connection"
	oldName := ""
	pwPlaceholder := ""
	if existing != nil {
		c := *existing
		title = "Edit " + c.Name
		oldName = c.Name
		pwPlaceholder = "leave empty to keep the stored password"
		d.name, d.host, d.port, d.user, d.database = c.Name, c.Host, strconv.Itoa(c.Port), c.User, c.DefaultDatabase()
		d.readOnly, d.production = c.ReadOnly, c.Production
		if c.SSLMode != "" {
			d.sslmode = c.SSLMode
		}
	}

	required := func(label string) func(string) error {
		return func(s string) error {
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("%s is required", label)
			}
			return nil
		}
	}

	opts := make([]huh.Option[string], len(sslModes))
	for i, s := range sslModes {
		opts[i] = huh.NewOption(s, s)
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Title("Name").Value(&d.name).Validate(func(s string) error {
				s = strings.TrimSpace(s)
				if s == "" {
					return errors.New("name is required")
				}
				if s != oldName && taken(s) {
					return fmt.Errorf("%q already exists", s)
				}
				return nil
			}),
			huh.NewInput().Title("Host").Value(&d.host).Validate(required("host")),
			huh.NewInput().Title("Port").Value(&d.port).Validate(func(s string) error {
				p, err := strconv.Atoi(strings.TrimSpace(s))
				if err != nil || p <= 0 || p > 65535 {
					return errors.New("port must be 1-65535")
				}
				return nil
			}),
			huh.NewInput().Title("User").Value(&d.user).Validate(required("user")),
			huh.NewInput().Title("Password").Value(&d.password).EchoMode(huh.EchoModePassword).Placeholder(pwPlaceholder),
			huh.NewInput().Title("Database").Value(&d.database).Validate(required("database")),
		),
		huh.NewGroup(
			huh.NewSelect[string]().Title("SSL mode").Options(opts...).Value(&d.sslmode),
			huh.NewConfirm().Title("Read-only?").Description("Every transaction is opened read-only.").Value(&d.readOnly),
			huh.NewConfirm().Title("Production?").Description("Writes require confirmation; the tab is shown in red.").Value(&d.production),
		),
	).WithTheme(huh.ThemeFunc(huh.ThemeCharm)).WithKeyMap(keymap()).WithShowHelp(true).WithWidth(60)

	return Model{form: form, title: title, data: d, oldName: oldName}
}

// NewPasswordPrompt asks for the password of a connection.
func NewPasswordPrompt(name, reason string) Model {
	d := &fields{name: name}
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Password for " + name).Description(reason).Value(&d.password).EchoMode(huh.EchoModePassword),
	)).WithTheme(huh.ThemeFunc(huh.ThemeCharm)).WithKeyMap(keymap()).WithShowHelp(true).WithWidth(60)
	return Model{form: form, title: "Password required", data: d, password: true}
}

// Init starts the form.
func (m Model) Init() tea.Cmd { return m.form.Init() }

// Update forwards messages to the form and reports completion.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if m.form.State != huh.StateNormal {
		return m, nil // already reported
	}
	f, cmd := m.form.Update(msg)
	if ff, ok := f.(*huh.Form); ok {
		m.form = ff
	}
	switch m.form.State {
	case huh.StateAborted:
		return m, func() tea.Msg { return CancelMsg{} }
	case huh.StateCompleted:
		return m, m.submit()
	}
	return m, cmd
}

func (m Model) submit() tea.Cmd {
	d := m.data
	if m.password {
		out := PasswordMsg{Name: d.name, Password: d.password}
		return func() tea.Msg { return out }
	}
	port, _ := strconv.Atoi(strings.TrimSpace(d.port))
	sslmode := d.sslmode
	if sslmode == "prefer" {
		sslmode = "" // libpq default
	}
	out := SubmitMsg{
		OldName: m.oldName,
		Conn: config.Connection{
			Name:       strings.TrimSpace(d.name),
			Host:       strings.TrimSpace(d.host),
			Port:       port,
			User:       strings.TrimSpace(d.user),
			Database:   strings.TrimSpace(d.database),
			SSLMode:    sslmode,
			ReadOnly:   d.readOnly,
			Production: d.production,
		},
		Password:        d.password,
		PasswordChanged: m.oldName == "" || d.password != "",
	}
	return func() tea.Msg { return out }
}

// View renders the form in a box.
func (m Model) View() string {
	body := styles.Title.Render(m.title) + "\n\n" + m.form.View()
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(styles.Accent).Padding(1, 2).Render(body)
}
