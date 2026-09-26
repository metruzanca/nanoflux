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
	cmd.AddCommand(itemBackfillThumbsCmd(st, out))
	cmd.AddCommand(itemDedupCmd(st, out))
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

// itemBackfillThumbsCmd repairs the image_url of YouTube items stored before
// the media:thumbnail fix. Older items are outside the feed's current window,
// so a re-poll would never reach them; the thumbnail is deterministic from the
// video id, so it can be filled without any network access. Run it inside the
// container (`make shell`) or on the host against the same NF_DB.
func itemBackfillThumbsCmd(st *store.Store, out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "backfill-thumbs",
		Short: "Fill missing YouTube item thumbnails",
		Long: "Set image_url for YouTube items that have none, deriving it from the\n" +
			"video id in the item GUID. Repairs items stored before the plugin's\n" +
			"media:thumbnail fix; older items are no longer in the feed window, so a\n" +
			"normal refresh cannot reach them. Requires no network access.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := st.Items.BackfillYouTubeThumbnails()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "filled %d youtube thumbnail(s)\n", n)
			return nil
		},
	}
}
