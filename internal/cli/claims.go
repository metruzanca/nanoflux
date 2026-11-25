package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/metruzanca/nanoflux/internal/store"
)

func claimsCmd(st *store.Store, out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "claims",
		Short: "Inspect and rebuild derived author claims",
	}
	cmd.AddCommand(claimsRebuildCmd(st, out))
	return cmd
}

func claimsRebuildCmd(st *store.Store, out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "rebuild",
		Short: "Rebuild item_claims for every user",
		Long: "Rebuild the derived item_claims table from author_feeds, saved_views and\n" +
			"the author rules. Claims are never a source of truth, so this is safe to\n" +
			"run at any time. The server also builds them on first boot.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := st.DB().Query(`SELECT id FROM users ORDER BY id`)
			if err != nil {
				return err
			}
			var ids []int64
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, id)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			for _, id := range ids {
				n, err := st.Claims.RebuildAll(id)
				if err != nil {
					return fmt.Errorf("user %d: %w", id, err)
				}
				fmt.Fprintf(out, "user #%d: %d claim(s)\n", id, n)
			}
			return nil
		},
	}
}
