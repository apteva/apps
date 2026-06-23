package main

// feed.go — RSS 2.0 + iTunes namespace feed rendering, plus the URL
// helpers that decide where a show's feed and enclosures live.
//
// Enclosure URLs deliberately point at this sidecar's /e/{guid}/{file}
// tracking proxy rather than the storage URL directly — that's how
// download counts get collected while validators still see Range support
// on the enclosure URL itself.

import (
	"encoding/xml"
	"net/url"
	"path"
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

func enclosureURL(show *Show, ep *Episode) string {
	return feedBaseURL(show) + "/e/" + url.PathEscape(ep.GUID) + "/" + enclosureFileName(ep)
}

// ─── RSS document model ────────────────────────────────────────────

type rssDoc struct {
	XMLName   xml.Name   `xml:"rss"`
	Version   string     `xml:"version,attr"`
	ITunesNS  string     `xml:"xmlns:itunes,attr"`
	ContentNS string     `xml:"xmlns:content,attr"`
	AtomNS    string     `xml:"xmlns:atom,attr"`
	PodcastNS string     `xml:"xmlns:podcast,attr"`
	Channel   rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title          string          `xml:"title"`
	Link           string          `xml:"link"`
	Language       string          `xml:"language"`
	Description    string          `xml:"description"`
	AtomLink       *atomLink       `xml:"atom:link,omitempty"`
	Copyright      string          `xml:"copyright,omitempty"`
	LastBuildDate  string          `xml:"lastBuildDate,omitempty"`
	ITunesAuthor   string          `xml:"itunes:author,omitempty"`
	ITunesSummary  string          `xml:"itunes:summary,omitempty"`
	ITunesType     string          `xml:"itunes:type,omitempty"`
	ITunesExplicit string          `xml:"itunes:explicit"`
	ITunesImage    *itunesImage    `xml:"itunes:image,omitempty"`
	ITunesOwner    *itunesOwner    `xml:"itunes:owner,omitempty"`
	ITunesCategory *itunesCategory `xml:"itunes:category,omitempty"`
	PodcastLocked  *podcastLocked  `xml:"podcast:locked,omitempty"`
	PodcastGUID    string          `xml:"podcast:guid,omitempty"`
	Items          []rssItem       `xml:"item"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
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

type podcastLocked struct {
	Owner string `xml:"owner,attr,omitempty"`
	Value string `xml:",chardata"`
}

type rssItem struct {
	Title          string       `xml:"title"`
	GUID           rssGUID      `xml:"guid"`
	Link           string       `xml:"link,omitempty"`
	PubDate        string       `xml:"pubDate,omitempty"`
	Description    string       `xml:"description"`
	ContentEncoded *cdataValue  `xml:"content:encoded,omitempty"`
	Enclosure      rssEnclosure `xml:"enclosure"`
	ITunesDuration string       `xml:"itunes:duration,omitempty"`
	ITunesSummary  string       `xml:"itunes:summary,omitempty"`
	ITunesType     string       `xml:"itunes:episodeType,omitempty"`
	ITunesSeason   string       `xml:"itunes:season,omitempty"`
	ITunesEpisode  string       `xml:"itunes:episode,omitempty"`
	ITunesExplicit string       `xml:"itunes:explicit,omitempty"`
	ITunesImage    *itunesImage `xml:"itunes:image,omitempty"`
	Transcript     *transcript  `xml:"podcast:transcript,omitempty"`
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

type transcript struct {
	URL      string `xml:"url,attr"`
	Type     string `xml:"type,attr,omitempty"`
	Language string `xml:"language,attr,omitempty"`
}

// renderFeed builds the RSS XML for a show and its published episodes.
func renderFeed(show *Show, episodes []Episode) ([]byte, error) {
	explicit := "false"
	if show.Explicit {
		explicit = "true"
	}
	ch := rssChannel{
		Title:          show.Title,
		Link:           firstNonEmpty(show.Link, feedURL(show)),
		Language:       firstNonEmpty(show.Language, "en"),
		Description:    show.Description,
		AtomLink:       &atomLink{Href: feedURL(show), Rel: "self", Type: "application/rss+xml"},
		Copyright:      show.Copyright,
		LastBuildDate:  time.Now().UTC().Format(time.RFC1123Z),
		ITunesAuthor:   show.Author,
		ITunesSummary:  show.Description,
		ITunesType:     firstNonEmpty(show.PodcastType, "episodic"),
		ITunesExplicit: explicit,
		PodcastLocked:  &podcastLocked{Owner: show.OwnerEmail, Value: "no"},
		PodcastGUID:    show.PodcastGUID,
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
		encURL := enclosureURL(show, ep)
		desc := plainText(ep.Description)
		item := rssItem{
			Title:       ep.Title,
			GUID:        rssGUID{IsPermaLink: "false", Value: ep.GUID},
			Link:        encURL,
			Description: desc,
			Enclosure: rssEnclosure{
				URL:    encURL,
				Length: ep.AudioBytes,
				Type:   firstNonEmpty(ep.MimeType, "audio/mpeg"),
			},
			ITunesSummary:  desc,
			ITunesType:     firstNonEmpty(ep.EpisodeType, "full"),
			ITunesExplicit: explicit,
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
		} else if show.ImageFileID != "" {
			item.ITunesImage = &itunesImage{Href: feedBaseURL(show) + "/art/show/" + strconv.FormatInt(show.ID, 10)}
		}
		if ep.TranscriptFileID != "" {
			item.Transcript = &transcript{
				URL:      feedBaseURL(show) + "/transcript/episode/" + strconv.FormatInt(ep.ID, 10),
				Type:     "text/plain",
				Language: firstNonEmpty(show.Language, "en"),
			}
		}
		ch.Items = append(ch.Items, item)
	}

	doc := rssDoc{
		Version:   "2.0",
		ITunesNS:  "http://www.itunes.com/dtds/podcast-1.0.dtd",
		ContentNS: "http://purl.org/rss/1.0/modules/content/",
		AtomNS:    "http://www.w3.org/2005/Atom",
		PodcastNS: "https://podcastindex.org/namespace/1.0",
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

func enclosureFileName(ep *Episode) string {
	base := slugify(ep.Title)
	if base == "" {
		base = "episode"
	}
	return base + audioExtension(ep)
}

func audioExtension(ep *Episode) string {
	if ep != nil {
		if ext := strings.ToLower(path.Ext(storageURLPath(ep.AudioURL))); isPodcastAudioExt(ext) {
			return ext
		}
		switch strings.ToLower(strings.TrimSpace(ep.MimeType)) {
		case "audio/mpeg", "audio/mp3":
			return ".mp3"
		case "audio/mp4", "audio/x-m4a":
			return ".m4a"
		case "audio/aac":
			return ".aac"
		case "audio/wav", "audio/x-wav":
			return ".wav"
		case "audio/ogg":
			return ".ogg"
		case "audio/flac":
			return ".flac"
		}
	}
	return ".mp3"
}

func storageURLPath(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err == nil && u.Path != "" {
		return u.Path
	}
	return raw
}

func isPodcastAudioExt(ext string) bool {
	switch ext {
	case ".mp3", ".m4a", ".mp4", ".aac", ".wav", ".ogg", ".flac":
		return true
	default:
		return false
	}
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
