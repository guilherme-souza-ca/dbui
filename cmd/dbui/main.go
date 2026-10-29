// Command dbui is a terminal client for PostgreSQL.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/guilhermesouza/dbui/internal/config"
	"github.com/guilhermesouza/dbui/internal/db"
	"github.com/guilhermesouza/dbui/internal/db/postgres"
	"github.com/guilhermesouza/dbui/internal/secret"
	"github.com/guilhermesouza/dbui/internal/tui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dbui:", err)
		os.Exit(1)
	}
}

func run() error {
	defaultPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	configPath := flag.String("config", defaultPath, "connections file")
	logPath := flag.String("log", "", "write debug logs to this file")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: dbui [flags] [connection-name]\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *logPath != "" {
		f, err := tea.LogToFile(*logPath, "dbui")
		if err != nil {
			return err
		}
		defer f.Close()
	}

	store, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	initial := flag.Arg(0)
	if initial != "" {
		if _, ok := store.Get(initial); !ok {
			return fmt.Errorf("no saved connection named %q (see %s)", initial, store.Path())
		}
	}

	deps := tui.Deps{
		Store:   store,
		Secrets: secret.Keyring{},
		Open: func(ctx context.Context, c config.Connection, password string) (db.Session, error) {
			return postgres.Open(ctx, c, password)
		},
	}
	_, err = tea.NewProgram(tui.New(deps, initial)).Run()
	return err
}
