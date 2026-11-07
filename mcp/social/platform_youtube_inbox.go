// platform_youtube_inbox — YouTube comments in the Social inbox. Sync
// reads the channel's comment threads (all videos) newest first until it
// reaches what it has already seen, fetches the complete reply list for
// any thread whose embedded replies are incomplete, and walks the rest of
// the channel a few pages per sync so older threads that get new replies
// are picked up too. YouTube has no DM API.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	youtubeHeadCursor     = "youtube_head"   // newest top-level comment time seen
	youtubeDeepCursor     = "youtube_deep"   // page token of the rolling full-channel pass
	youtubePollCursor     = "youtube_poll"   // last scheduled poll
	youtubePollInterval   = 30 * time.Minute // scheduled polls; quota is shared by every channel on the OAuth client
	youtubeHeadPages      = 10               // newest pages read per sync at most
	youtubeDeepPages      = 1                // older pages rechecked per sync
	youtubeReplyPages     = 10               // reply pages fetched per thread at most
	youtubeQuotaBackoff   = 60 * time.Minute // after quotaExceeded
	youtubeErrPrefixPerm  = "permission_required: "
	youtubeErrPrefixCat   = "catalog_update_required: "
	youtubeErrPrefixQuota = "quota_exceeded: "
)

type youtubeInboxCreds struct {
	AccountID int64
	ConnID    int64
	ChannelID string
}

type youtubeSyncReport struct {
	NewComments    int      `json:"new_comments"`
	ThreadsRead    int      `json:"threads_read"`
	RepliesFetched int      `json:"replies_fetched"`
	PassCompleted  bool     `json:"full_pass_completed,omitempty"`
	Skipped        string   `json:"skipped,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

type youtubeCommentSnippet struct {
	VideoID               string `json:"videoId"`
	ParentID              string `json:"parentId"`
	TextOriginal          string `json:"textOriginal"`
	TextDisplay           string `json:"textDisplay"`
	AuthorDisplayName     string `json:"authorDisplayName"`
	AuthorProfileImageURL string `json:"authorProfileImageUrl"`
	AuthorChannelURL      string `json:"authorChannelUrl"`
	AuthorChannelID       struct {
		Value string `json:"value"`
	} `json:"authorChannelId"`
	PublishedAt string `json:"publishedAt"`
	UpdatedAt   string `json:"updatedAt"`
}

type youtubeComment struct {
	ID      string                `json:"id"`
	Snippet youtubeCommentSnippet `json:"snippet"`
}

type youtubeThread struct {
	ID      string `json:"id"`
	Snippet struct {
		VideoID         string         `json:"videoId"`
		TotalReplyCount int            `json:"totalReplyCount"`
		TopLevelComment youtubeComment `json:"topLevelComment"`
	} `json:"snippet"`
	Replies struct {
		Comments []youtubeComment `json:"comments"`
	} `json:"replies"`
}

func loadYouTubeInboxCreds(ctx *sdk.AppCtx, pid string, accountID int64) (*youtubeInboxCreds, error) {
	account, err := loadAudienceAccount(ctx, pid, accountID)
	if err != nil {
		return nil, fmt.Errorf("social_account %d not found", accountID)
	}
	if account.Platform != "youtube" || account.Status != "active" {
		return nil, fmt.Errorf("social_account %d is not an active YouTube account", accountID)
	}
	channel := account.ExternalID
	if !strings.HasPrefix(channel, "UC") {
		if channel, err = youtubeChannelIdentity(ctx, account); err != nil {
			return nil, err
		}
	}
	return &youtubeInboxCreds{AccountID: accountID, ConnID: account.ConnectionID, ChannelID: channel}, nil
}

// youtubeAPIError classifies a failed YouTube call into the warning
// prefixes coverage understands.
func youtubeAPIError(tool string, res *sdk.ExecuteResult, err error) error {
	if err != nil {
		text := err.Error()
		if strings.Contains(strings.ToLower(text), "tool not found") || strings.Contains(strings.ToLower(text), "unknown tool") {
			return errors.New(youtubeErrPrefixCat + "this platform's YouTube integration has no " + tool + " tool yet; update the platform")
		}
		return fmt.Errorf("%s: %s", tool, text)
	}
	reason := youtubeErrorReason(res)
	body := ""
	if res != nil {
		body = strings.ToLower(string(res.Data))
	}
	switch {
	case res != nil && res.Status == 404 && strings.Contains(body, "tool not found"):
		return errors.New(youtubeErrPrefixCat + "this platform's YouTube integration has no " + tool + " tool yet; update the platform")
	case reason == "insufficientPermissions" || strings.Contains(body, "access_token_scope_insufficient") || (res != nil && res.Status == 401):
		return errors.New(youtubeErrPrefixPerm + "reconnect this YouTube channel in Integrations to grant the missing access")
	case reason == "quotaExceeded" || reason == "rateLimitExceeded" || reason == "dailyLimitExceeded":
		return errors.New(youtubeErrPrefixQuota + "YouTube API quota is used up; sync resumes later")
	case reason != "":
		return fmt.Errorf("%s: %s", tool, reason)
	}
	return fmt.Errorf("%s: %w", tool, upstreamError(res))
}

func youtubeErrorReason(res *sdk.ExecuteResult) string {
	if res == nil {
		return ""
	}
	var body struct {
		Error struct {
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(res.Data, &body) != nil {
		return ""
	}
	if len(body.Error.Errors) > 0 && body.Error.Errors[0].Reason != "" {
		return body.Error.Errors[0].Reason
	}
	if len(body.Error.Details) > 0 {
		return body.Error.Details[0].Reason
	}
	return ""
}

func youtubeTime(raw string) time.Time {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func youtubeCommentPermalink(videoID, commentID string) string {
	if videoID == "" {
		return ""
	}
	return "https://www.youtube.com/watch?v=" + url.QueryEscape(videoID) + "&lc=" + url.QueryEscape(commentID)
}

func youtubeCursor(ctx *sdk.AppCtx, accountID int64, kind string) (value, lastSync string) {
	_ = ctx.AppDB().QueryRow(`SELECT COALESCE(cursor,''), COALESCE(last_sync_at,'') FROM inbox_cursors WHERE social_account_id=? AND kind=?`, accountID, kind).Scan(&value, &lastSync)
	return value, lastSync
}

func setYouTubeCursor(ctx *sdk.AppCtx, accountID int64, kind, value string) {
	_, _ = ctx.AppDB().Exec(`INSERT INTO inbox_cursors(social_account_id,kind,cursor,last_sync_at) VALUES (?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(social_account_id,kind) DO UPDATE SET cursor=excluded.cursor,last_sync_at=excluded.last_sync_at`, accountID, kind, value)
}

// syncYouTubeAccount reads new comments and replies. Scheduled polls run
// at most every youtubePollInterval; inbox_sync always runs.
func syncYouTubeAccount(ctx *sdk.AppCtx, pid string, accountID int64, scheduled bool) (*youtubeSyncReport, error) {
	report := &youtubeSyncReport{}
	if scheduled {
		if _, last := youtubeCursor(ctx, accountID, youtubePollCursor); last != "" {
			if t, err := time.Parse("2006-01-02 15:04:05", last); err == nil && time.Since(t) < youtubePollInterval {
				report.Skipped = "polled recently"
				return report, nil
			}
		}
	}
	done := beginInboxSync(ctx, accountID)
	defer done()
	creds, err := loadYouTubeInboxCreds(ctx, pid, accountID)
	if err != nil {
		return nil, err
	}
	setYouTubeCursor(ctx, accountID, youtubePollCursor, "")
	posts := map[string]int64{}

	// Newest threads until we reach comments older than the last sync.
	head, _ := youtubeCursor(ctx, accountID, youtubeHeadCursor)
	headTime := youtubeTime(head)
	newest := headTime
	token := ""
	for page := 0; page < youtubeHeadPages; page++ {
		threads, next, err := listYouTubeThreads(ctx, creds, token)
		if err != nil {
			report.Warnings = append(report.Warnings, youtubeSyncWarning(ctx, accountID, err))
			break
		}
		reachedSeen := false
		for _, thread := range threads {
			published := youtubeTime(thread.Snippet.TopLevelComment.Snippet.PublishedAt)
			if published.After(newest) {
				newest = published
			}
			if !headTime.IsZero() && published.Before(headTime.Add(-inboxBackfillGrace)) {
				reachedSeen = true
			}
			report.NewComments += recordYouTubeThread(ctx, pid, creds, thread, posts, report)
		}
		if next == "" || reachedSeen {
			break
		}
		token = next
		if page == youtubeHeadPages-1 && headTime.IsZero() {
			// First sync of a large channel: the rolling pass continues
			// from here on the next syncs.
			if deep, _ := youtubeCursor(ctx, accountID, youtubeDeepCursor); deep == "" {
				setYouTubeCursor(ctx, accountID, youtubeDeepCursor, next)
			}
		}
	}
	if len(report.Warnings) == 0 && !newest.IsZero() {
		setYouTubeCursor(ctx, accountID, youtubeHeadCursor, newest.Format(time.RFC3339))
	}

	// Rolling pass over the rest of the channel for new replies on older
	// threads.
	if len(report.Warnings) == 0 {
		deep, _ := youtubeCursor(ctx, accountID, youtubeDeepCursor)
		for page := 0; page < youtubeDeepPages; page++ {
			threads, next, err := listYouTubeThreads(ctx, creds, deep)
			if err != nil {
				report.Warnings = append(report.Warnings, youtubeSyncWarning(ctx, accountID, err))
				break
			}
			for _, thread := range threads {
				report.NewComments += recordYouTubeThread(ctx, pid, creds, thread, posts, report)
			}
			deep = next
			if next == "" {
				report.PassCompleted = true
				break
			}
		}
		if len(report.Warnings) == 0 {
			setYouTubeCursor(ctx, accountID, youtubeDeepCursor, deep)
		}
	}
	if err := inboxWriteError(ctx.AppDB(), accountID); err != nil {
		report.Warnings = append(report.Warnings, "inbox persistence incomplete: "+err.Error())
	}
	_, _ = ctx.AppDB().Exec(`INSERT INTO inbox_cursors (social_account_id, kind, cursor, last_sync_at, last_error) VALUES (?, 'all', '', CURRENT_TIMESTAMP, ?)
		ON CONFLICT(social_account_id, kind) DO UPDATE SET last_sync_at=excluded.last_sync_at, last_error=excluded.last_error`,
		accountID, nullable(strings.Join(report.Warnings, "; ")))
	return report, nil
}

func youtubeSyncWarning(ctx *sdk.AppCtx, accountID int64, err error) string {
	if strings.HasPrefix(err.Error(), youtubeErrPrefixQuota) {
		_, _ = ctx.AppDB().Exec(`INSERT INTO inbox_cursors(social_account_id,kind,cursor,last_error) VALUES (?,'poll_backoff',?,'YouTube quota exceeded') ON CONFLICT(social_account_id,kind) DO UPDATE SET cursor=excluded.cursor,last_error=excluded.last_error`, accountID, time.Now().Add(youtubeQuotaBackoff).UTC().Format(time.RFC3339))
	}
	return err.Error()
}

func listYouTubeThreads(ctx *sdk.AppCtx, creds *youtubeInboxCreds, token string) ([]youtubeThread, string, error) {
	if !consumeInboxBudget(ctx, creds.AccountID) {
		return nil, "", errors.New("inbox budget exhausted or polling paused")
	}
	input := map[string]any{
		"part":                         "snippet,replies",
		"allThreadsRelatedToChannelId": creds.ChannelID,
		"order":                        "time",
		"maxResults":                   100,
		"textFormat":                   "plainText",
	}
	if token != "" {
		input["pageToken"] = token
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "list_channel_comment_threads", input)
	if err != nil || res == nil || !res.Success {
		return nil, "", youtubeAPIError("list_channel_comment_threads", res, err)
	}
	var page struct {
		Items         []youtubeThread `json:"items"`
		NextPageToken string          `json:"nextPageToken"`
	}
	if err := json.Unmarshal(res.Data, &page); err != nil {
		return nil, "", fmt.Errorf("decode YouTube comment threads: %w", err)
	}
	return page.Items, page.NextPageToken, nil
}

// recordYouTubeThread saves the top-level comment and every reply.
// Embedded replies are a sample; when YouTube reports more replies than
// were embedded and than we already hold, the complete list is fetched.
func recordYouTubeThread(ctx *sdk.AppCtx, pid string, creds *youtubeInboxCreds, thread youtubeThread, posts map[string]int64, report *youtubeSyncReport) int {
	report.ThreadsRead++
	top := thread.Snippet.TopLevelComment
	if top.ID == "" {
		top.ID = thread.ID
	}
	videoID := thread.Snippet.VideoID
	if videoID == "" {
		videoID = top.Snippet.VideoID
	}
	added := 0
	if recordYouTubeComment(ctx, pid, creds, top, "", videoID, posts) {
		added++
	}
	replies := thread.Replies.Comments
	if thread.Snippet.TotalReplyCount > len(replies) {
		var held int
		_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE social_account_id=? AND kind='comment' AND parent_external_id=?`, creds.AccountID, top.ID).Scan(&held)
		if held < thread.Snippet.TotalReplyCount {
			all, err := listYouTubeReplies(ctx, creds, top.ID)
			if err != nil {
				report.Warnings = append(report.Warnings, youtubeSyncWarning(ctx, creds.AccountID, err))
			} else {
				report.RepliesFetched += len(all)
				replies = all
			}
		}
	}
	for _, reply := range replies {
		if recordYouTubeComment(ctx, pid, creds, reply, top.ID, videoID, posts) {
			added++
		}
	}
	return added
}

func listYouTubeReplies(ctx *sdk.AppCtx, creds *youtubeInboxCreds, parentID string) ([]youtubeComment, error) {
	var out []youtubeComment
	token := ""
	for page := 0; page < youtubeReplyPages; page++ {
		if !consumeInboxBudget(ctx, creds.AccountID) {
			return out, errors.New("inbox budget exhausted while reading replies")
		}
		input := map[string]any{"part": "snippet", "parentId": parentID, "maxResults": 100, "textFormat": "plainText"}
		if token != "" {
			input["pageToken"] = token
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "list_comment_replies", input)
		if err != nil || res == nil || !res.Success {
			return out, youtubeAPIError("list_comment_replies", res, err)
		}
		var body struct {
			Items         []youtubeComment `json:"items"`
			NextPageToken string           `json:"nextPageToken"`
		}
		if err := json.Unmarshal(res.Data, &body); err != nil {
			return out, fmt.Errorf("decode YouTube replies: %w", err)
		}
		out = append(out, body.Items...)
		if body.NextPageToken == "" {
			return out, nil
		}
		token = body.NextPageToken
	}
	return out, nil
}

func recordYouTubeComment(ctx *sdk.AppCtx, pid string, creds *youtubeInboxCreds, c youtubeComment, parentID, videoID string, posts map[string]int64) bool {
	if c.ID == "" {
		return false
	}
	if parentID == "" {
		parentID = c.Snippet.ParentID
	}
	postID, ok := posts[videoID]
	if !ok && videoID != "" {
		_ = ctx.AppDB().QueryRow(`SELECT pt.post_id FROM post_targets pt JOIN posts p ON p.id=pt.post_id
			WHERE pt.social_account_id=? AND pt.platform_post_id=? AND p.project_id=? ORDER BY pt.id DESC LIMIT 1`,
			creds.AccountID, videoID, pid).Scan(&postID)
		posts[videoID] = postID
	}
	body := c.Snippet.TextOriginal
	if body == "" {
		body = c.Snippet.TextDisplay
	}
	author := c.Snippet.AuthorChannelID.Value
	occurred := youtubeTime(c.Snippet.PublishedAt)
	_, inserted, err := recordInboxItem(ctx, inboxUpsertInput{
		Outbound:         author != "" && author == creds.ChannelID,
		ProjectID:        pid,
		SocialAccountID:  creds.AccountID,
		Platform:         "youtube",
		Kind:             inboxKindComment,
		ExternalID:       c.ID,
		ParentExternalID: parentID,
		PostID:           postID,
		ExternalPostID:   videoID,
		AuthorExternalID: author,
		AuthorName:       c.Snippet.AuthorDisplayName,
		AuthorHandle:     strings.TrimPrefix(c.Snippet.AuthorDisplayName, "@"),
		AuthorAvatarURL:  c.Snippet.AuthorProfileImageURL,
		Body:             body,
		Permalink:        youtubeCommentPermalink(videoID, c.ID),
		OccurredAt:       occurred,
		RawJSON:          marshalSafe(c),
	})
	if err != nil {
		ctx.Logger().Warn("youtube comment upsert failed", "external_id", c.ID, "err", err)
		return false
	}
	return inserted
}

// youtubeInboxReply answers a comment. YouTube threads are one level
// deep: a reply to a reply goes under the same top-level comment.
func youtubeInboxReply(ctx *sdk.AppCtx, item *inboxItem, body string) inboxOutcome {
	out := inboxOutcome{InboxItemID: item.ID, SocialAccountID: item.SocialAccountID, Platform: item.Platform}
	if item.Kind != inboxKindComment {
		out.Status, out.Reason = "unsupported", "YouTube has no direct messages"
		return out
	}
	if strings.TrimSpace(body) == "" {
		out.Status, out.Error = "failed", "body required"
		return out
	}
	creds, err := loadYouTubeInboxCreds(ctx, item.ProjectID, item.SocialAccountID)
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	parent := item.ExternalID
	if item.ParentExternalID != "" {
		parent = item.ParentExternalID
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "reply_to_comment", map[string]any{
		"snippet": map[string]any{"parentId": parent, "textOriginal": body},
	})
	if err != nil || res == nil || !res.Success {
		out.Status, out.Error = "failed", youtubeReplyError(res, err).Error()
		return out
	}
	var created youtubeComment
	_ = json.Unmarshal(res.Data, &created)
	out.Status = "ok"
	out.ExternalID = created.ID
	out.Permalink = youtubeCommentPermalink(item.ExternalPostID, created.ID)
	_ = markInboxRepliedByExternalID(ctx.AppDB(), item.SocialAccountID, item.Kind, item.ExternalID)
	if created.ID != "" {
		_, _, _ = upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
			Outbound:         true,
			ProjectID:        item.ProjectID,
			SocialAccountID:  item.SocialAccountID,
			Platform:         "youtube",
			Kind:             inboxKindComment,
			ExternalID:       created.ID,
			ParentExternalID: parent,
			PostID:           item.PostID,
			ExternalPostID:   item.ExternalPostID,
			AuthorExternalID: creds.ChannelID,
			Body:             body,
			Permalink:        out.Permalink,
			OccurredAt:       time.Now().UTC(),
		})
	}
	return out
}

func youtubeReplyError(res *sdk.ExecuteResult, err error) error {
	switch youtubeErrorReason(res) {
	case "commentsDisabled":
		return errors.New("comments are disabled on this video")
	case "commentNotFound", "parentCommentNotFound", "processingFailure":
		return errors.New("the comment no longer exists on YouTube")
	case "canNotReply", "forbidden":
		return errors.New("YouTube does not allow replying to this comment")
	}
	return youtubeAPIError("reply_to_comment", res, err)
}

// youtubeInboxHide hides (moderation status rejected) or restores a
// viewer's comment. Only the channel owner can moderate.
func youtubeInboxHide(ctx *sdk.AppCtx, item *inboxItem, hide bool) inboxOutcome {
	out := inboxOutcome{InboxItemID: item.ID, SocialAccountID: item.SocialAccountID, Platform: item.Platform}
	creds, err := loadYouTubeInboxCreds(ctx, item.ProjectID, item.SocialAccountID)
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	status := "published"
	if hide {
		status = "rejected"
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "set_comment_moderation_status", map[string]any{"id": item.ExternalID, "moderationStatus": status})
	if err != nil || res == nil || !res.Success {
		out.Status, out.Error = "failed", youtubeAPIError("set_comment_moderation_status", res, err).Error()
		return out
	}
	out.Status = "ok"
	if hide {
		_ = setInboxStatus(ctx.AppDB(), item.ProjectID, item.ID, inboxStatusHidden)
	} else {
		_ = setInboxStatus(ctx.AppDB(), item.ProjectID, item.ID, inboxStatusRead)
	}
	return out
}

// youtubeInboxDelete deletes the channel's own comment. Viewers' comments
// cannot be deleted through the API; inbox_hide hides them instead.
func youtubeInboxDelete(ctx *sdk.AppCtx, item *inboxItem) inboxOutcome {
	out := inboxOutcome{InboxItemID: item.ID, SocialAccountID: item.SocialAccountID, Platform: item.Platform}
	creds, err := loadYouTubeInboxCreds(ctx, item.ProjectID, item.SocialAccountID)
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if item.AuthorExternalID != creds.ChannelID {
		out.Status, out.Reason = "unsupported", "YouTube only deletes the channel's own comments; use inbox_hide for a viewer's comment"
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "delete_comment", map[string]any{"id": item.ExternalID})
	if err != nil || res == nil || !res.Success {
		out.Status, out.Error = "failed", youtubeAPIError("delete_comment", res, err).Error()
		return out
	}
	out.Status = "ok"
	_ = setInboxStatus(ctx.AppDB(), item.ProjectID, item.ID, inboxStatusArchived)
	return out
}
