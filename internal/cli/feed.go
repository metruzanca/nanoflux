package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/metruzanca/nanoflux/internal/config"
	"github.com/metruzanca/nanoflux/internal/plugin"
	"github.com/metruzanca/nanoflux/internal/poller"
	"github.com/metruzanca/nanoflux/internal/store"
)

func feedCmd(st *store.Store, out, errOut io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "feed",
		Short: "Inspect and refetch feeds",
	}
	cmd.AddCommand(feedListCmd(st, out))
	cmd.AddCommand(feedRefetchCmd(st, out, errOut))
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
				if f.LastError != "" {
					state = "error"
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

// feedRefetchCmd re-polls feeds whose stored last error matches a substring
// (default: an HTTP 400), so a fetcher fix takes effect without waiting for the
// next scheduled poll. Feeds are polled one at a time with a delay between
// them, and a rate-limit cooldown learned during the run is honored, so the
// batch does not itself get throttled.
func feedRefetchCmd(st *store.Store, out, errOut io.Writer) *cobra.Command {
	var (
		errorContains string
		delay         time.Duration
		dryRun        bool
	)
	cmd := &cobra.Command{
		Use:   "refetch",
		Short: "Re-poll feeds whose last error matches (default: HTTP 400)",
		Long: "Re-poll feeds whose stored last error contains a substring. The " +
			"default (\"status 400\") re-fetches feeds a host rejected before a " +
			"fetcher fix. Feeds are polled one at a time with a delay, and a " +
			"learned rate-limit cooldown is honored, so the batch is not itself " +
			"throttled.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			feeds, err := st.Feeds.ListAll()
			if err != nil {
				return err
			}
			targets := errorFeeds(feeds, errorContains)
			if len(targets) == 0 {
				fmt.Fprintln(out, "no matching feeds")
				return nil
			}
			fmt.Fprintf(out, "%d feed(s) to refetch (delay %s)\n", len(targets), delay)
			if dryRun {
				for _, f := range targets {
					fmt.Fprintf(out, "  [%d] %s — %s\n", f.ID, f.Title, f.LastError)
				}
				return nil
			}
			return refetchFeeds(cmd.Context(), st, targets, delay, out, errOut)
		},
	}
	cmd.Flags().StringVar(&errorContains, "error", "status 400", "only feeds whose last error contains this substring (empty = any error)")
	cmd.Flags().DurationVar(&delay, "delay", 30*time.Second, "delay between feeds, to avoid rate limits")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list matching feeds without fetching")
	return cmd
}

// errorFeeds returns the enabled feeds whose last poll failed with an error
// containing needle (case-insensitive). An empty needle matches any error.
func errorFeeds(feeds []store.FeedWithOwner, needle string) []store.Feed {
	needle = strings.ToLower(needle)
	var out []store.Feed
	for _, f := range feeds {
		if !f.Enabled || f.LastError == "" {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(f.LastError), needle) {
			continue
		}
		out = append(out, f.Feed)
	}
	return out
}

// refetchFeeds polls each target once with the full plugin system in place, so
// a plugin-owned feed is fetched by its plugin (and a site fix applies
// immediately). It canonicalizes each URL first, so a stored feed that predates
// a URL-rule fix is fetched in its working shape.
func refetchFeeds(ctx context.Context, st *store.Store, targets []store.Feed, delay time.Duration, out, errOut io.Writer) error {
	cfg := config.Load()
	// One cooldown shared by the poller and the plugin host, so a limit seen by
	// either paces the rest of the batch.
	cooldown := plugin.NewCooldown()
	p := poller.New(st, cfg.PollInterval, 1)
	p.SetHostSpacing(cfg.PollHostSpacing)
	p.SetHostCooler(cooldown)
	rt := plugin.Setup(st, p.Client(), cfg.PluginsDir, cooldown)
	defer rt.Close()
	// Own the URL rules, then rewrite stored feed URLs to their canonical shape
	// so a feed whose URL a fix changes is fetched in its working form.
	st.SetURLPolicy(plugin.NewStoreURLPolicy(rt.Registry))
	if _, err := st.Feeds.CanonicalizeFeedURLs(); err != nil {
		fmt.Fprintf(errOut, "canonicalize feed urls: %v\n", err)
	}

	for i := range targets {
		targets[i].FeedURL = rt.Registry.CanonicalizeFeedURL(targets[i].FeedURL)
	}
	for i, f := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		if i > 0 {
			if err := sleepCtx(ctx, delay); err != nil {
				return err
			}
		}
		// A host the batch has already been rate-limited on is paced by its
		// learned window before the next request.
		if until, ok := cooldown.Until(f.FeedURL); ok {
			wait := time.Until(until)
			fmt.Fprintf(out, "  host cooling, waiting %s\n", wait.Round(time.Second))
			if err := sleepCtx(ctx, wait); err != nil {
				return err
			}
		}
		n, err := p.PollOne(ctx, f)
		if err != nil {
			fmt.Fprintf(out, "  [%d] %s: %v\n", f.ID, f.Title, err)
			continue
		}
		fmt.Fprintf(out, "  [%d] %s: %d new item(s)\n", f.ID, f.Title, n)
	}
	return nil
}

// sleepCtx sleeps for d unless the context is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
