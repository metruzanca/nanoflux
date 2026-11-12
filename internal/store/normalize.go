package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
	"github.com/metruzanca/nanoflux/internal/tagnorm"
	"github.com/metruzanca/nanoflux/internal/urlcanon"
)

// normalizationMarker records that the one-time canonicalization pass has run.
// The pass is idempotent, so a partial failure simply reruns it. v2 folds feed
// post-type words (image, video, audio, text) onto their reserved type:*
// identifier; bumping the marker runs the pass once more on existing rows.
const normalizationMarker = "data.normalization_v2"

// NormalizationReport counts the rows a canonicalization pass changed.
type NormalizationReport struct {
	Usernames          int
	Items              int
	ItemCategories     int
	AuthorFilterValues int
	Labels             int
	AuthorLinks        int
}

// Total returns how many rows the pass changed.
func (r NormalizationReport) Total() int {
	return r.Usernames + r.Items + r.ItemCategories + r.AuthorFilterValues + r.Labels + r.AuthorLinks
}

// idText is one row's id and its free-text value, used by the bulk passes.
type idText struct {
	id    int64
	value string
}

// categoryChange is the canonical category set for one item or filter rule.
type categoryChange struct {
	id   int64
	cats []string
}

// labelChange is one display label to rewrite in a known table/column.
type labelChange struct {
	id     int64
	value  string
	target labelTarget
}

type labelTarget struct {
	Table  string
	Column string
}

// normalizableLabels lists the display-text columns the one-time pass cleans.
// Only constants are interpolated into SQL; values are always bound.
var normalizableLabels = []labelTarget{
	{"authors", "name"},
	{"feeds", "title"},
	{"collections", "name"},
	{"lists", "name"},
	{"saved_views", "title"},
	{"author_links", "label"},
}

// NormalizeData canonicalizes data written by older versions:
//
//   - usernames are trimmed and lowercased, so login is case-insensitive;
//   - item tags are lowercased, NFC-normalized, whitespace-collapsed and
//     deduplicated in both items.categories and item_categories, and a bare
//     media word (a feed's post-type category) is folded onto type:*;
//   - author-filter tag values are canonicalized to match them;
//   - display labels gain canonical whitespace and Unicode composition;
//   - author-link URLs are canonicalized (host case and default ports only).
//
// It runs once, guarded by a settings marker, and is safe to rerun. It must run
// before MergeCrossFeedDuplicates, which copies item_categories rows.
func (s *Store) NormalizeData() (NormalizationReport, error) {
	ctx := context.Background()
	var report NormalizationReport

	done, err := s.q.GetSetting(ctx, normalizationMarker)
	if err == nil && done == "1" {
		return report, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return report, fmt.Errorf("read normalization marker: %w", err)
	}

	users, err := readIDText(s.db, `SELECT id, username FROM users ORDER BY id`)
	if err != nil {
		return report, err
	}
	items, err := readIDText(s.db, `SELECT id, categories FROM items WHERE categories <> '' ORDER BY id`)
	if err != nil {
		return report, err
	}
	itemCats, err := readIDText(s.db, `SELECT item_id, category FROM item_categories ORDER BY item_id, category`)
	if err != nil {
		return report, err
	}
	authorFilt, err := readIDText(s.db, `
		SELECT r.id, v.value
		FROM author_filter_rules r
		JOIN author_filter_values v ON v.rule_id = r.id
		WHERE r.kind = 'tag'
		ORDER BY r.id, v.value`)
	if err != nil {
		return report, err
	}
	authorLinks, err := readIDText(s.db, `SELECT id, url FROM author_links ORDER BY id`)
	if err != nil {
		return report, err
	}

	// Usernames must be unique after normalization. Detect collisions before
	// touching any row so a database with "Alice" and "alice" is reported
	// instead of half-renamed.
	usernames := make([]idText, 0, len(users))
	seenNames := make(map[string]int64, len(users))
	for _, row := range users {
		name := tagnorm.Username(row.value)
		if name == "" || name == row.value {
			continue
		}
		if other, ok := seenNames[name]; ok && other != row.id {
			return report, fmt.Errorf(
				"cannot lowercase usernames: ids %d and %d both normalize to %q; rename one and restart",
				other, row.id, name)
		}
		seenNames[name] = row.id
		usernames = append(usernames, idText{id: row.id, value: name})
	}

	labels := make([]labelChange, 0)
	for _, target := range normalizableLabels {
		rows, err := readIDText(s.db,
			`SELECT id, `+target.Column+` FROM `+target.Table+` ORDER BY id`)
		if err != nil {
			return report, err
		}
		for _, row := range rows {
			cleaned := tagnorm.Label(row.value)
			if cleaned != row.value {
				labels = append(labels, labelChange{id: row.id, value: cleaned, target: target})
			}
		}
	}

	itemChanges := make([]idText, 0, len(items))
	for _, row := range items {
		joined := strings.Join(normalizeCategories(strings.Split(row.value, "\n")), "\n")
		if joined != row.value {
			itemChanges = append(itemChanges, idText{id: row.id, value: joined})
		}
	}
	itemCatChanges := normalizedCategoryRows(itemCats)
	filterChanges := normalizedFilterRows(authorFilt)

	linkChanges := make([]idText, 0, len(authorLinks))
	for _, row := range authorLinks {
		clean := urlcanon.Link(row.value)
		if clean != row.value {
			linkChanges = append(linkChanges, idText{id: row.id, value: clean})
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, fmt.Errorf("begin normalization: %w", err)
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)

	for _, row := range usernames {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET username = ? WHERE id = ?`, row.value, row.id); err != nil {
			return report, fmt.Errorf("normalize username %d: %w", row.id, err)
		}
	}
	report.Usernames = len(usernames)

	for _, row := range itemChanges {
		if _, err := tx.ExecContext(ctx, `UPDATE items SET categories = ? WHERE id = ?`, row.value, row.id); err != nil {
			return report, fmt.Errorf("normalize item %d categories: %w", row.id, err)
		}
	}
	report.Items = len(itemChanges)

	for _, change := range itemCatChanges {
		if _, err := tx.ExecContext(ctx, `DELETE FROM item_categories WHERE item_id = ?`, change.id); err != nil {
			return report, fmt.Errorf("clear item %d categories: %w", change.id, err)
		}
		for _, cat := range change.cats {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO item_categories (item_id, category) VALUES (?, ?)`, change.id, cat); err != nil {
				return report, fmt.Errorf("insert item %d category: %w", change.id, err)
			}
		}
	}
	report.ItemCategories = len(itemCatChanges)

	for _, change := range filterChanges {
		if _, err := tx.ExecContext(ctx, `DELETE FROM author_filter_values WHERE rule_id = ?`, change.id); err != nil {
			return report, fmt.Errorf("clear author filter %d values: %w", change.id, err)
		}
		for _, value := range change.cats {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO author_filter_values (rule_id, value) VALUES (?, ?)`, change.id, value); err != nil {
				return report, fmt.Errorf("insert author filter %d value: %w", change.id, err)
			}
		}
	}
	report.AuthorFilterValues = len(filterChanges)

	for _, row := range labels {
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+row.target.Table+` SET `+row.target.Column+` = ? WHERE id = ?`, row.value, row.id); err != nil {
			return report, fmt.Errorf("normalize %s.%s %d: %w", row.target.Table, row.target.Column, row.id, err)
		}
	}
	report.Labels = len(labels)

	for _, row := range linkChanges {
		if _, err := tx.ExecContext(ctx, `UPDATE author_links SET url = ? WHERE id = ?`, row.value, row.id); err != nil {
			return report, fmt.Errorf("normalize author link %d: %w", row.id, err)
		}
	}
	report.AuthorLinks = len(linkChanges)

	if err := q.UpsertSetting(ctx, sqlcgen.UpsertSettingParams{Key: normalizationMarker, Value: "1"}); err != nil {
		return report, fmt.Errorf("write normalization marker: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("commit normalization: %w", err)
	}
	return report, nil
}

// readIDText runs a two-column id/text query and returns its rows.
func readIDText(db *sql.DB, query string) ([]idText, error) {
	rows, err := db.QueryContext(context.Background(), query)
	if err != nil {
		return nil, fmt.Errorf("read normalization rows: %w", err)
	}
	defer rows.Close()
	var out []idText
	for rows.Next() {
		var (
			row   idText
			value sql.NullString
		)
		if err := rows.Scan(&row.id, &value); err != nil {
			return nil, fmt.Errorf("scan normalization row: %w", err)
		}
		row.value = value.String
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read normalization rows: %w", err)
	}
	return out, nil
}

// normalizedCategoryRows groups item_categories rows by item and returns the
// items whose stored set is not already canonical. Order is preserved as the
// query returned it so a rerun is stable.
func normalizedCategoryRows(rows []idText) []categoryChange {
	var (
		order   []int64
		grouped = map[int64][]string{}
	)
	for _, row := range rows {
		if _, ok := grouped[row.id]; !ok {
			order = append(order, row.id)
		}
		grouped[row.id] = append(grouped[row.id], row.value)
	}
	var changes []categoryChange
	for _, id := range order {
		normalized := normalizeCategories(grouped[id])
		if !sameCategorySet(grouped[id], normalized) {
			changes = append(changes, categoryChange{id: id, cats: normalized})
		}
	}
	return changes
}

// normalizeCategories canonicalizes one tag set, folding a feed's bare media
// word onto the reserved type:* identifier before dedupe.
func normalizeCategories(values []string) []string {
	return tagnorm.NormalizeAll(tagnorm.FoldMediaTypeAliases(values))
}

// normalizedFilterRows is normalizedCategoryRows for author_filter_values,
// keyed by rule id.
func normalizedFilterRows(rows []idText) []categoryChange {
	return normalizedCategoryRows(rows)
}

// sameCategorySet reports whether raw already equals normalized: same length
// (so no duplicates were removed) and every value is already canonical.
func sameCategorySet(raw, normalized []string) bool {
	if len(raw) != len(normalized) {
		return false
	}
	for i := range raw {
		if raw[i] != normalized[i] {
			return false
		}
	}
	return true
}
