package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/metruzanca/nanoflux/internal/storage"
	"github.com/metruzanca/nanoflux/internal/store"
	"github.com/metruzanca/nanoflux/internal/web"
)

// storageCmd groups object-store inspection and maintenance commands.
func storageCmd(st *store.Store, env Env, out, errOut io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "Inspect and maintain the object store",
		Long:  "Report where object-storage bytes live, remove cached item media that no row references, and backfill cached-media sizes.",
	}
	cmd.AddCommand(storageReportCmd(st, env, out))
	cmd.AddCommand(storageGCCmd(st, env, out, errOut))
	cmd.AddCommand(storageBackfillCmd(st, env, out, errOut))
	return cmd
}

func storageReportCmd(st *store.Store, env Env, out io.Writer) *cobra.Command {
	var top int
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Show where object-storage bytes live",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			files, err := env.Files()
			if err != nil {
				return err
			}
			rep, err := storage.Audit(cmd.Context(), files, st)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "total: %s in %d objects\n", web.FormatBytes(rep.TotalBytes), rep.TotalObjects)
			if rep.OrphanObjects > 0 {
				fmt.Fprintf(out, "orphaned cache: %s in %d objects (run `nanoflux storage gc`)\n",
					web.FormatBytes(rep.OrphanBytes), rep.OrphanObjects)
			}
			fmt.Fprintln(out, "breakdown:")
			n := top
			if n <= 0 || n > len(rep.Groups) {
				n = len(rep.Groups)
			}
			for _, g := range rep.Groups[:n] {
				line := fmt.Sprintf("  %-24s %10s  %6d objects", g.Label, web.FormatBytes(g.Bytes), g.Objects)
				if g.OrphanBytes > 0 {
					line += fmt.Sprintf("  (%s orphaned)", web.FormatBytes(g.OrphanBytes))
				}
				fmt.Fprintln(out, line)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&top, "top", 20, "show only the N largest groups (0 = all)")
	return cmd
}

func storageGCCmd(st *store.Store, env Env, out, errOut io.Writer) *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Delete cached item media that no row references",
		Long: "Find cached item media (cache/<plugin>/<itemID>/...) that no item or " +
			"enclosure references, e.g. after a feed or account was deleted, and remove it. " +
			"Reports only unless --apply is given.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			files, err := env.Files()
			if err != nil {
				return err
			}
			orphans, err := storage.Orphans(cmd.Context(), files, st)
			if err != nil {
				return err
			}
			var total int64
			for _, o := range orphans {
				total += o.Size
			}
			if !apply {
				fmt.Fprintf(out, "would delete %s in %d orphaned objects (pass --apply to delete)\n",
					web.FormatBytes(total), len(orphans))
				return nil
			}
			deleted := 0
			for _, o := range orphans {
				if err := files.Delete(cmd.Context(), o.Key); err != nil {
					fmt.Fprintf(errOut, "warning: delete %q: %v\n", o.Key, err)
					continue
				}
				deleted++
			}
			fmt.Fprintf(out, "deleted %d orphaned objects (%s)\n", deleted, web.FormatBytes(total))
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "delete the orphaned objects")
	return cmd
}

func storageBackfillCmd(st *store.Store, env Env, out, errOut io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Fill cached-media sizes recorded before this feature existed",
		Long: "Walk the cached item media in object storage and record each blob's byte " +
			"size against its item/enclosure, so per-feed storage totals include media " +
			"cached before size tracking was added.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			files, err := env.Files()
			if err != nil {
				return err
			}
			objs, err := files.List(cmd.Context(), "cache/")
			if err != nil {
				return err
			}
			updated := 0
			for _, o := range objs {
				if err := st.Items.SetItemImageCacheSizeByKey(o.Key, o.Size); err != nil {
					fmt.Fprintf(errOut, "warning: image size %q: %v\n", o.Key, err)
					continue
				}
				if err := st.Items.SetEnclosureCacheSizeByKey(o.Key, o.Size); err != nil {
					fmt.Fprintf(errOut, "warning: enclosure size %q: %v\n", o.Key, err)
					continue
				}
				updated++
			}
			fmt.Fprintf(out, "backfilled %d cached objects\n", updated)
			return nil
		},
	}
	return cmd
}
