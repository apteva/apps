package main

// feed.go — RSS 2.0 + iTunes namespace feed rendering, plus the URL
// helpers that decide where a show's feed and enclosures live.
//
// Enclosure URLs deliberately point at this sidecar's /e/{guid}
// tracking redirect rather than the storage URL directly — that's how
// download counts get collected. The redirect 302s to the real
// storage URL (Episode.AudioURL).

import (
	"encoding/xml"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ─── URL helpers ───────────────────────────────────────────────────

// feedBaseURL is the public origin a show is served from. With a
// custom hostname (claimed via server-native ingress), that hostname is
// the origin. Otherwise apteva-server reverse-proxies this sidecar under
// /api/apps/podcast on the platform's public origin.
func feedBaseURL(show *Show) string {
	if h := strings.TrimSpace(show.Hostname); h != "" {
		return "https://" + h
	}
	origin := platformPublicOrigin()
	if origin == "" {
		origin = "http://localhost:5280"
	}
	return origin + "/api/apps/podcast"
}

func feedURL(show *Show) string {
	return feedBaseURL(show) + "/feed/" + show.Slug + ".xml"
}

func enclosureURL(show *Show, guid string) string {
	return feedBaseURL(show) + "/e/" + guid
}

// ─── RSS document model ────────────────────────────────────────────

type rssDoc struct {
	XMLName   xml.Name   `xml:"rss"`
	Version   string     `xml:"version,attr"`
	ITunesNS  string     `xml:"xmlns:itunes,attr"`
	ContentNS string     `xml:"xmlns:content,attr"`
	Channel   rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title          string          `xml:"title"`
	Link           string          `xml:"link"`
	Language       string          `xml:"language"`
	Description    string          `xml:"description"`
	Copyright      string          `xml:"copyright,omitempty"`
	LastBuildDate  string          `xml:"lastBuildDate,omitempty"`
	ITunesAuthor   string          `xml:"itunes:author,omitempty"`
	ITunesType     string          `xml:"itunes:type,omitempty"`
	ITunesExplicit string          `xml:"itunes:explicit"`
	ITunesImage    *itunesImage    `xml:"itunes:image,omitempty"`
	ITunesOwner    *itunesOwner    `xml:"itunes:owner,omitempty"`
	ITunesCategory *itunesCategory `xml:"itunes:category,omitempty"`
	Items          []rssItem       `xml:"item"`
}

type itunesImage struct {
	Href string `xml:"href,attr"`
}

type itunesOwner struct {
	Name  string `xml:"itunes:name,omitempty"`
	Email string `xml:"itunes:email,omitempty"`
}

type itunesCategory struct {
	Text string `xml:"text,attr"`
}

type rssItem struct {
	Title          string       `xml:"title"`
	GUID           rssGUID      `xml:"guid"`
	PubDate        string       `xml:"pubDate,omitempty"`
	Description    string       `xml:"description"`
	ContentEncoded *cdataValue  `xml:"content:encoded,omitempty"`
	Enclosure      rssEnclosure `xml:"enclosure"`
	ITunesDuration string       `xml:"itunes:duration,omitempty"`
	ITunesType     string       `xml:"itunes:episodeType,omitempty"`
	ITunesSeason   string       `xml:"itunes:season,omitempty"`
	ITunesEpisode  string       `xml:"itunes:episode,omitempty"`
	ITunesExplicit string       `xml:"itunes:explicit,omitempty"`
	ITunesImage    *itunesImage `xml:"itunes:image,omitempty"`
}

type rssGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type cdataValue struct {
	Value string `xml:",cdata"`
}

// renderFeed builds the RSS XML for a show and its published episodes.
func renderFeed(show *Show, episodes []Episode) ([]byte, error) {
	explicit := "false"
	if show.Explicit {
		explicit = "true"
	}
	ch := rssChannel{
		Title:          show.Title,
		Link:           show.Link,
		Language:       firstNonEmpty(show.Language, "en"),
		Description:    show.Description,
		Copyright:      show.Copyright,
		LastBuildDate:  time.Now().UTC().Format(time.RFC1123Z),
		ITunesAuthor:   show.Author,
		ITunesType:     firstNonEmpty(show.PodcastType, "episodic"),
		ITunesExplicit: explicit,
	}
	if show.ImageFileID != "" {
		// image_file_id is a storage file id; the panel resolves it to
		// a URL on write. v0.1 stores the id; the feed needs an
		// absolute URL, so we point at this sidecar's art passthrough.
		ch.ITunesImage = &itunesImage{Href: feedBaseURL(show) + "/art/show/" + strconv.FormatInt(show.ID, 10)}
	}
	if show.OwnerEmail != "" || show.Author != "" {
		ch.ITunesOwner = &itunesOwner{Name: show.Author, Email: show.OwnerEmail}
	}
	if show.Category != "" {
		ch.ITunesCategory = &itunesCategory{Text: show.Category}
	}

	for i := range episodes {
		ep := &episodes[i]
		item := rssItem{
			Title:       ep.Title,
			GUID:        rssGUID{IsPermaLink: "false", Value: ep.GUID},
			Description: plainText(ep.Description),
			Enclosure: rssEnclosure{
				URL:    enclosureURL(show, ep.GUID),
				Length: ep.AudioBytes,
				Type:   firstNonEmpty(ep.MimeType, "audio/mpeg"),
			},
			ITunesType: firstNonEmpty(ep.EpisodeType, "full"),
		}
		if ep.Description != "" {
			item.ContentEncoded = &cdataValue{Value: ep.Description}
		}
		if ep.PublishedAt != nil {
			item.PubDate = rfc822(*ep.PublishedAt)
		}
		if ep.DurationSeconds > 0 {
			item.ITunesDuration = strconv.FormatInt(ep.DurationSeconds, 10)
		}
		if ep.SeasonNumber != nil {
			item.ITunesSeason = strconv.FormatInt(*ep.SeasonNumber, 10)
		}
		if ep.EpisodeNumber != nil {
			item.ITunesEpisode = strconv.FormatInt(*ep.EpisodeNumber, 10)
		}
		if ep.ImageFileID != "" {
			item.ITunesImage = &itunesImage{Href: feedBaseURL(show) + "/art/episode/" + strconv.FormatInt(ep.ID, 10)}
		}
		ch.Items = append(ch.Items, item)
	}

	doc := rssDoc{
		Version:   "2.0",
		ITunesNS:  "http://www.itunes.com/dtds/podcast-1.0.dtd",
		ContentNS: "http://purl.org/rss/1.0/modules/content/",
		Channel:   ch,
	}
	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}

// ─── feed cache ────────────────────────────────────────────────────
//
// Podcast clients poll feeds aggressively; rendering on every hit is
// wasteful. The cache is keyed by show id and bust on any write to the
// show or its episodes. TTL is config.feed_cache_seconds.

type feedCacheEntry struct {
	body       []byte
	renderedAt time.Time
}

var (
	feedCacheMu sync.Mutex
	feedCache   = map[int64]feedCacheEntry{}
)

func cachedFeed(showID int64, ttl time.Duration) ([]byte, bool) {
	feedCacheMu.Lock()
	defer feedCacheMu.Unlock()
	e, ok := feedCache[showID]
	if !ok || time.Since(e.renderedAt) > ttl {
		return nil, false
	}
	return e.body, true
}

func storeFeed(showID int64, body []byte) {
	feedCacheMu.Lock()
	feedCache[showID] = feedCacheEntry{body: body, renderedAt: time.Now()}
	feedCacheMu.Unlock()
}

func bustFeed(showID int64) {
	feedCacheMu.Lock()
	delete(feedCache, showID)
	feedCacheMu.Unlock()
}

// ─── small helpers ─────────────────────────────────────────────────

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// rfc822 converts DB timestamp strings into the RFC1123Z pubDate
// format podcast clients expect. SQLite stores "2006-01-02 15:04:05",
// while some drivers scan the same value back as RFC3339.
func rfc822(raw string) string {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, time.RFC3339Nano} {
		t, err := time.Parse(layout, raw)
		if err == nil {
			return t.UTC().Format(time.RFC1123Z)
		}
	}
	return raw
}

// plainText strips HTML tags for the plain <description> element;
// the full HTML show notes go in <content:encoded> as CDATA.
func plainText(html string) string {
	var b strings.Builder
	inTag := false
	for _, r := range html {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
