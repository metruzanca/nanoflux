package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/metruzanca/nanoflux/internal/store"
)

// fixCmd groups one-off data repairs for instances that predate a change. Every
// subcommand here is temporary and will be removed before the v1.0.0 release;
// prefer a normal migration (internal/db/migrate.go) for anything permanent.
func fixCmd(st *store.Store, out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fix",
		Short: "One-off data repairs (temporary; removed before v1.0.0)",
		Long: "One-off repairs for data written by an older version. These are\n" +
			"temporary and will be removed before v1.0.0. Most also run\n" +
			"automatically at server startup, so this is for running them\n" +
			"explicitly and seeing what changed.",
	}
	cmd.AddCommand(fixRedditUserFeedsCmd(st, out))
	return cmd
}

// fixRedditUserFeedsCmd rewrites a reddit user's bare feed URL to the
// posts-only /submitted.rss form. Reddit's bare user feed mixes posts and
// comments; /submitted.rss is posts-only and is what discover.Derive now
// produces, so existing feeds must be moved to match. It reports by default;
// --apply performs the rewrite. Idempotent.
func fixRedditUserFeedsCmd(st *store.Store, out io.Writer) *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "reddit-user-feeds",
		Short: "Rewrite reddit user feeds to /submitted.rss",
		Long: "Rewrite stored reddit user feeds (reddit.com/u/<name>.rss) to the\n" +
			"posts-only reddit.com/u/<name>/submitted.rss form. The bare user feed\n" +
			"mixes posts and comments. Runs as a report unless --apply is given.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			feeds, err := st.Feeds.ListAll()
			if err != nil {
				return err
			}
			changed := 0
			for _, f := range feeds {
				canonical := store.CanonicalFeedURL(f.FeedURL)
				if canonical == f.FeedURL {
					continue
				}
				verb := "would rewrite"
				if apply {
					verb = "rewrote"
				}
				fmt.Fprintf(out, "%s #%d %s: %s -> %s\n", verb, f.ID, f.Title, f.FeedURL, canonical)
				changed++
			}
			if changed == 0 {
				fmt.Fprintln(out, "no reddit user feeds to rewrite")
				return nil
			}
			if !apply {
				fmt.Fprintf(out, "found %d feed(s) to rewrite; re-run with --apply to apply\n", changed)
				return nil
			}
			n, err := st.Feeds.CanonicalizeFeedURLs()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "rewrote %d feed(s)\n", n)
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "perform the rewrite (default: report only)")
	return cmd
}
