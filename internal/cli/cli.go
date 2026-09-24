// Package cli implements the nanoflux admin CLI, a subcommand tree
// (`nanoflux user ...`, `nanoflux feed list`, `nanoflux backup`) that operates
// directly on the database, independent of the web server. It shares the store
// layer so admin actions behave exactly like their web counterparts.
package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/metruzanca/nanoflux/internal/config"
	"github.com/metruzanca/nanoflux/internal/db"
	"github.com/metruzanca/nanoflux/internal/filestore"
	"github.com/metruzanca/nanoflux/internal/store"
)

// Run opens the database from env config, migrates it, and executes the
// command tree. It returns the process exit code.
func Run(version string, args []string) int {
	cfg := config.Load()
	sqldb, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nanoflux: %v\n", err)
		return 1
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		fmt.Fprintf(os.Stderr, "nanoflux: migrate: %v\n", err)
		return 1
	}
	st := store.New(sqldb)
	env := Env{
		DBPath:       cfg.DBPath,
		FileStoreDir: cfg.FileStoreDir,
		Files: func() (filestore.Store, error) {
			return filestore.NewFromConfig(filestore.ConfigFromEnv(), cfg.FileStoreDir)
		},
	}
	root := New(version, st, env, os.Stdout, os.Stderr)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		return 1
	}
	return 0
}

// Env carries the runtime paths and blob-store factory the CLI needs beyond
// the store: where the database lives, where disk blobs live, and how to build
// the object-storage client.
type Env struct {
	DBPath       string
	FileStoreDir string
	Files        func() (filestore.Store, error)
}

// New builds the command tree with injected dependencies so tests can drive it
// against an in-memory store and memory filestore.
func New(version string, st *store.Store, env Env, stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "nanoflux",
		Short:         "nanoflux admin tools",
		Long:          "Administer a nanoflux instance. Run inside the container (make shell) or on the host.",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.AddCommand(userCmd(st, env.Files, stdout, stderr))
	root.AddCommand(feedCmd(st, stdout))
	root.AddCommand(itemCmd(st, stdout))
	root.AddCommand(backupCmd(st, env, stdout, stderr))
	root.AddCommand(restoreCmd(st, env, stdout, stderr))
	root.AddCommand(versionCmd(version))
	return root
}

func versionCmd(version string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the nanoflux version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), version)
			return nil
		},
	}
}
