package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/filestore"
	"github.com/metruzanca/nanoflux/internal/store"
)

func userCmd(st *store.Store, files func() (filestore.Store, error), stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage users",
		Long:  "List, create, modify and remove users. Grant the admin flag with `user set-admin <username> true`.",
	}
	cmd.AddCommand(userListCmd(st, stdout))
	cmd.AddCommand(userCreateCmd(st, stdout, stderr))
	cmd.AddCommand(userSetAdminCmd(st, stdout))
	cmd.AddCommand(userResetPasswordCmd(st, stdout, stderr))
	cmd.AddCommand(userDeleteCmd(st, files, stdout, stderr))
	return cmd
}

func userCreateCmd(st *store.Store, out, errOut io.Writer) *cobra.Command {
	var password string
	var admin bool
	cmd := &cobra.Command{
		Use:   "create <username>",
		Short: "Create a user account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			username := strings.TrimSpace(args[0])
			if username == "" {
				return fmt.Errorf("username is required")
			}
			if _, err := st.Users.ByUsername(username); err == nil {
				return fmt.Errorf("user %q already exists", username)
			}
			pw := password
			if pw == "" {
				var err error
				pw, err = promptPassword(errOut)
				if err != nil {
					return err
				}
			}
			if len(pw) < 8 {
				return fmt.Errorf("password must be at least 8 characters")
			}
			hash, err := auth.HashPassword(pw)
			if err != nil {
				return err
			}
			u, err := st.Users.Create(username, hash)
			if err != nil {
				return err
			}
			if admin {
				if err := st.Users.SetAdmin(u.ID, true); err != nil {
					return err
				}
				fmt.Fprintf(out, "created user %q as admin\n", username)
				return nil
			}
			fmt.Fprintf(out, "created user %q\n", username)
			return nil
		},
	}
	cmd.Flags().StringVarP(&password, "password", "p", "", "password (prompted for if omitted)")
	cmd.Flags().BoolVar(&admin, "admin", false, "grant the admin flag")
	return cmd
}

func userListCmd(st *store.Store, out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List users",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			users, err := st.Users.List()
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "USERNAME\tADMIN\tCREATED")
			for _, u := range users {
				fmt.Fprintf(tw, "%s\t%v\t%s\n", u.Username, u.IsAdmin, u.CreatedAt)
			}
			return tw.Flush()
		},
	}
}

func userSetAdminCmd(st *store.Store, out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "set-admin <username> <true|false>",
		Short: "Grant or revoke the admin flag",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			username := args[0]
			admin, err := strconv.ParseBool(args[1])
			if err != nil {
				return fmt.Errorf("invalid admin flag %q: want true or false", args[1])
			}
			u, err := st.Users.ByUsername(username)
			if err != nil {
				return fmt.Errorf("user %q: %w", username, err)
			}
			if !admin {
				admins, err := st.Users.CountAdmins()
				if err != nil {
					return err
				}
				if admins <= 1 {
					return fmt.Errorf("cannot revoke admin from %q: it is the last admin", username)
				}
			}
			if err := st.Users.SetAdmin(u.ID, admin); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s is now %s\n", username, adminRole(admin))
			return nil
		},
	}
}

func userResetPasswordCmd(st *store.Store, out, errOut io.Writer) *cobra.Command {
	var password string
	cmd := &cobra.Command{
		Use:   "reset-password <username>",
		Short: "Reset a user's password and log out all their sessions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			username := args[0]
			u, err := st.Users.ByUsername(username)
			if err != nil {
				return fmt.Errorf("user %q: %w", username, err)
			}
			pw := password
			if pw == "" {
				pw, err = promptPassword(errOut)
				if err != nil {
					return err
				}
			}
			if len(pw) < 8 {
				return fmt.Errorf("password must be at least 8 characters")
			}
			hash, err := auth.HashPassword(pw)
			if err != nil {
				return err
			}
			if err := st.Users.ResetPassword(u.ID, hash); err != nil {
				return err
			}
			if err := st.Sessions.DeleteUserSessions(u.ID); err != nil {
				return err
			}
			fmt.Fprintf(out, "password reset for %s (all sessions logged out)\n", username)
			return nil
		},
	}
	cmd.Flags().StringVarP(&password, "password", "p", "", "new password (prompted for if omitted)")
	return cmd
}

func userDeleteCmd(st *store.Store, files func() (filestore.Store, error), out, errOut io.Writer) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <username>",
		Short: "Delete a user and all their data",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			username := args[0]
			u, err := st.Users.ByUsername(username)
			if err != nil {
				return fmt.Errorf("user %q: %w", username, err)
			}
			admins, err := st.Users.CountAdmins()
			if err != nil {
				return err
			}
			if u.IsAdmin && admins <= 1 {
				return fmt.Errorf("cannot delete %q: it is the last admin", username)
			}
			if !yes {
				if !term.IsTerminal(int(os.Stdin.Fd())) {
					return fmt.Errorf("confirmation required; pass --yes to delete %q", username)
				}
				fmt.Fprintf(errOut, "Delete user %q? This removes all their data and cannot be undone. [y/N] ", username)
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.TrimSpace(line) != "y" && strings.TrimSpace(line) != "Y" {
					fmt.Fprintln(errOut, "aborted")
					return nil
				}
			}
			if err := purgeUserObjects(cmd, st, files, u.ID, errOut); err != nil {
				return err
			}
			if err := st.Users.Delete(u.ID); err != nil {
				return err
			}
			fmt.Fprintf(out, "deleted user %q\n", username)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}

// purgeUserObjects removes the user's avatar and custom-icon blobs from object
// storage. Failures are warnings, never fatal: the DB row is the source of
// truth and deleting the user must not be blocked by a flaky store.
func purgeUserObjects(cmd *cobra.Command, st *store.Store, files func() (filestore.Store, error), userID int64, errOut io.Writer) error {
	if files == nil {
		return nil
	}
	keys, err := st.Users.ListObjectKeys(userID)
	if err != nil {
		return fmt.Errorf("list object keys: %w", err)
	}
	if len(keys) == 0 {
		return nil
	}
	fs, err := files()
	if err != nil {
		fmt.Fprintf(errOut, "warning: could not reach object storage; orphaned files may remain: %v\n", err)
		return nil
	}
	for _, k := range keys {
		if err := fs.Delete(cmd.Context(), k); err != nil {
			fmt.Fprintf(errOut, "warning: could not delete object %q: %v\n", k, err)
		}
	}
	return nil
}

func adminRole(admin bool) string {
	if admin {
		return "an admin"
	}
	return "not an admin"
}

// promptPassword reads a new password from the terminal, hiding input. Without
// a terminal it errors so callers must pass --password instead.
func promptPassword(errOut io.Writer) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("no terminal for password prompt; pass --password")
	}
	fmt.Fprint(errOut, "New password: ")
	a, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(errOut)
	if err != nil {
		return "", err
	}
	fmt.Fprint(errOut, "Confirm: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(errOut)
	if err != nil {
		return "", err
	}
	if string(a) != string(b) {
		return "", fmt.Errorf("passwords do not match")
	}
	return string(a), nil
}
