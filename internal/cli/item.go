package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/metruzanca/nanoflux/internal/store"
)

func itemCmd(st *store.Store, out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "item",
		Short: "Inspect and repair items",
	}
	cmd.AddCommand(itemDedupCmd(st, out))
	cmd.AddCommand(itemCrossDedupCmd(st, out))
	return cmd
}

// itemCrossDedupCmd merges the same post stored once per subscription (the same
// item reached through two feeds) into a single item whose feeds are both
// members, so read/favorite/list/share state is shared and the streams show it
// once. It creates the per-user cross-feed unique index. The server runs this at
// startup; the command is for a manual run or a report.
func itemCrossDedupCmd(st *store.Store, out io.Writer) *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "cross-dedup",
		Short: "Merge the same post stored once per subscription",
		Long: "Find posts stored more than once because the same post was seen through\n" +
			"two subscriptions and merge them into one item that both feeds are members\n" +
			"of. Read/favorite state, enclosures, list memberships and shares carry over;\n" +
			"the row most recently confirmed by a feed is kept. Also creates the per-user\n" +
			"cross-feed unique index. Runs as a report unless --apply is given. The server\n" +
			"already runs this at startup.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := st.Items.MergeCrossFeedDuplicates(!apply)
			if err != nil {
				return err
			}
			verb := "would merge"
			if apply {
				verb = "merged"
			}
			for _, g := range report.Groups {
				fmt.Fprintf(out, "%s user #%d %s: keep %d, drop %v\n",
					verb, g.UserID, g.CrossKey, g.Survivor, g.Losers)
			}
			fmt.Fprintf(out, "%s %d duplicate item(s) across %d group(s)\n",
				verb, report.ItemsMerged, len(report.Groups))
			if !apply && report.ItemsMerged > 0 {
				fmt.Fprintln(out, "re-run with --apply to perform the merge")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "perform the merge (default: report only)")
	return cmd
}

// itemDedupCmd merges items a feed stored more than once because a plugin
// changed its GUID scheme (e.g. a post URL becoming "scheme:id") for the same
// entry. It reports by default; --apply performs the merge. The scan is
// conservative: only same-feed, same-link, same-published-time rows are
// grouped, so distinct posts are never merged.
func itemDedupCmd(st *store.Store, out io.Writer) *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "dedup",
		Short: "Merge duplicate items left by a plugin GUID-scheme change",
		Long: "Find items a feed stored twice under different GUIDs for the same\n" +
			"entry (a plugin changing its GUID format, e.g. a post URL becoming\n" +
			"\"scheme:<id>\"), and merge them. Grouping is conservative — same feed,\n" +
			"same link, same published time — so distinct posts are never merged.\n" +
			"The row most recently confirmed by the feed is kept; the others' read\n" +
			"and favorite state, enclosures, list memberships and share links carry\n" +
			"over. Runs as a report unless --apply is given.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := st.Items.DeduplicateItems(!apply)
			if err != nil {
				return err
			}
			verb := "would merge"
			if apply {
				verb = "merged"
			}
			for _, g := range report.Groups {
				fmt.Fprintf(out, "%s #%d from %s: keep %d, drop %v\n",
					verb, g.FeedID, g.FeedTitle, g.Survivor, g.Losers)
			}
			fmt.Fprintf(out, "%s %d duplicate item(s) across %d group(s)\n",
				verb, report.ItemsMerged, len(report.Groups))
			if !apply && report.ItemsMerged > 0 {
				fmt.Fprintln(out, "re-run with --apply to perform the merge")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "perform the merge (default: report only)")
	return cmd
}
