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
