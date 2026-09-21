package cli

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/metruzanca/nanoflux/internal/store"
)

func feedCmd(st *store.Store, out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "feed",
		Short: "Inspect feeds",
	}
	cmd.AddCommand(feedListCmd(st, out))
	return cmd
}

// feedListCmd lists every feed across all users. Only the owner, title, state
// and last-polled time are shown — item summaries and content are never
// rendered, so nothing potentially NSFW flashes up on a shared screen.
func feedListCmd(st *store.Store, out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every feed across all users",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			feeds, err := st.Feeds.ListAll()
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "OWNER\tTITLE\tSTATE\tLAST POLLED")
			for _, f := range feeds {
				state := "on"
				if !f.Enabled {
					state = "paused"
				}
				last := f.LastPolledAt
				if last == "" {
					last = "never"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", f.Owner, f.Title, state, last)
			}
			return tw.Flush()
		},
	}
}
