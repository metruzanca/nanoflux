package store

import (
	"database/sql"

	"github.com/metruzanca/nanoflux/internal/store/sqlcgen"
)

// ns wraps a string for a nullable TEXT column; empty means NULL.
func ns(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// ni wraps an id for a nullable INTEGER column; 0 means NULL.
func ni(n int64) sql.NullInt64 {
	if n == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: n, Valid: true}
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func toUser(id int64, username, passwordHash string, isAdmin bool, avatarKey, timezone sql.NullString, theme, accentColor, homeConfig string, createdAt string) User {
	return User{
		ID:           id,
		Username:     username,
		PasswordHash: passwordHash,
		IsAdmin:      isAdmin,
		HasAvatar:    avatarKey.Valid,
		Timezone:     timezone.String,
		Theme:        theme,
		AccentColor:  accentColor,
		HomeConfig:   homeConfig,
		CreatedAt:    createdAt,
	}
}

func toFeed(f sqlcgen.Feed) Feed {
	return Feed{
		ID:               f.ID,
		UserID:           f.UserID,
		AuthorID:         f.AuthorID,
		Title:            f.Title,
		FeedURL:          f.FeedUrl,
		HomeURL:          f.HomeUrl.String,
		Description:      f.Description.String,
		ETag:             f.Etag.String,
		LastModified:     f.LastModified.String,
		LastPolledAt:     f.LastPolledAt.String,
		LastError:        f.LastError.String,
		NextPageURL:      f.NextPageUrl,
		Kind:             f.Kind,
		ScrapeConfig:     f.ScrapeConfig.String,
		PollIntervalSec:  int(f.PollIntervalSec),
		PollIntervalAuto: f.PollIntervalAuto != 0,
		LastItemAt:       f.LastItemAt.String,
		Enabled:          f.Enabled,
		CreatedAt:        f.CreatedAt,
	}
}

func toItem(m sqlcgen.Item) Item {
	return Item{
		ID:          m.ID,
		FeedID:      m.FeedID,
		GUID:        m.Guid,
		Title:       m.Title,
		Link:        m.Link,
		Summary:     m.Summary,
		ImageURL:    m.ImageUrl.String,
		PublishedAt: m.PublishedAt.String,
		FetchedAt:   m.FetchedAt,
		Read:        m.Read,
		ReadAt:      m.ReadAt.String,
		Favorite:    m.Favorite,
	}
}

func toItemWithFeed(id, feedID int64, guid, title, link, summary string,
	imageURL, publishedAt sql.NullString, fetchedAt string, read, favorite bool,
	readAt sql.NullString, feedTitle, feedURL string,
	authorID sql.NullInt64, authorName sql.NullString,
) ItemWithFeed {
	return ItemWithFeed{
		Item: Item{
			ID:          id,
			FeedID:      feedID,
			GUID:        guid,
			Title:       title,
			Link:        link,
			Summary:     summary,
			ImageURL:    imageURL.String,
			PublishedAt: publishedAt.String,
			FetchedAt:   fetchedAt,
			Read:        read,
			ReadAt:      readAt.String,
			Favorite:    favorite,
		},
		FeedTitle:  feedTitle,
		FeedURL:    feedURL,
		AuthorID:   authorID.Int64,
		AuthorName: authorName.String,
	}
}

func toAuthor(a sqlcgen.Author) Author {
	return Author{
		ID:            a.ID,
		UserID:        a.UserID,
		Name:          a.Name,
		URL:           a.Url.String,
		AvatarURL:     a.AvatarUrl.String,
		AvatarKey:     a.AvatarKey.String,
		LastFetchedAt: a.LastFetchedAt.String,
		Description:   a.Description.String,
		CreatedAt:     a.CreatedAt,
	}
}

func toCollection(c sqlcgen.Collection) Collection {
	return Collection{
		ID:        c.ID,
		UserID:    c.UserID,
		Name:      c.Name,
		IsAuto:    c.IsAuto != 0,
		CreatedAt: c.CreatedAt,
	}
}

func toAuthorLink(l sqlcgen.AuthorLink) AuthorLink {
	return AuthorLink{
		ID:        l.ID,
		UserID:    l.UserID,
		AuthorID:  l.AuthorID,
		Label:     l.Label.String,
		URL:       l.Url,
		CreatedAt: l.CreatedAt,
	}
}

func toSourceIcon(id, userID int64, domain, iconURL string, iconKey, lastFetchedAt sql.NullString, createdAt string) SourceIcon {
	return SourceIcon{
		ID:            id,
		UserID:        userID,
		Domain:        domain,
		IconURL:       iconURL,
		IconKey:       iconKey.String,
		LastFetchedAt: lastFetchedAt.String,
		CreatedAt:     createdAt,
	}
}

func toUrlMapping(id, userID int64, pattern, template, createdAt string) UrlMapping {
	return UrlMapping{
		ID:        id,
		UserID:    userID,
		Pattern:   pattern,
		Template:  template,
		CreatedAt: createdAt,
	}
}

func feedFromUnreadRow(id, userID int64, authorID int64, title, feedURL string,
	homeURL, description, etag, lastModified, lastPolledAt, lastError sql.NullString,
	nextPageURL, kind string, scrapeConfig sql.NullString, pollIntervalSec int64, pollIntervalAuto int64, lastItemAt sql.NullString, enabled bool, createdAt string,
) sqlcgen.Feed {
	return sqlcgen.Feed{
		ID:               id,
		UserID:           userID,
		AuthorID:         authorID,
		Title:            title,
		FeedUrl:          feedURL,
		HomeUrl:          homeURL,
		Description:      description,
		Etag:             etag,
		LastModified:     lastModified,
		LastPolledAt:     lastPolledAt,
		LastError:        lastError,
		NextPageUrl:      nextPageURL,
		Kind:             kind,
		ScrapeConfig:     scrapeConfig,
		PollIntervalSec:  pollIntervalSec,
		PollIntervalAuto: pollIntervalAuto,
		LastItemAt:       lastItemAt,
		Enabled:          enabled,
		CreatedAt:        createdAt,
	}
}

func toFeedWithUnread(f sqlcgen.ListFeedsWithUnreadRow) FeedWithUnread {
	return FeedWithUnread{
		Feed:       toFeed(feedFromUnreadRow(f.ID, f.UserID, f.AuthorID, f.Title, f.FeedUrl, f.HomeUrl, f.Description, f.Etag, f.LastModified, f.LastPolledAt, f.LastError, f.NextPageUrl, f.Kind, f.ScrapeConfig, f.PollIntervalSec, f.PollIntervalAuto, f.LastItemAt, f.Enabled, f.CreatedAt)),
		AuthorName: f.AuthorName.String,
		Unread:     int(f.Unread),
	}
}

func toFeedByAuthorWithUnread(f sqlcgen.ListFeedsByAuthorWithUnreadRow) FeedWithUnread {
	return FeedWithUnread{
		Feed:       toFeed(feedFromUnreadRow(f.ID, f.UserID, f.AuthorID, f.Title, f.FeedUrl, f.HomeUrl, f.Description, f.Etag, f.LastModified, f.LastPolledAt, f.LastError, f.NextPageUrl, f.Kind, f.ScrapeConfig, f.PollIntervalSec, f.PollIntervalAuto, f.LastItemAt, f.Enabled, f.CreatedAt)),
		AuthorName: f.AuthorName.String,
		Unread:     int(f.Unread),
	}
}
