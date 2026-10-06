package main

import (
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

var snsSubscriptionMu sync.Mutex

func confirmedSNSSubscription(arn string) bool {
	return strings.HasPrefix(arn, "arn:")
}

func ownedMessagingSubscription(endpoint string, expected []string, token, projectID string, includeProjectID bool) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	for _, want := range expected {
		canonical, err := url.Parse(want)
		if err != nil || u.Path != canonical.Path {
			continue
		}
		q, cq := u.Query(), canonical.Query()
		installID := cq.Get("install_id")
		if q.Get("install_id") != "" && q.Get("install_id") != installID {
			continue
		}
		// Legacy secrets identify this install even after its public URL
		// changes. Modern tokenless callbacks also require the same origin.
		legacy := token != "" && q.Get("api_key") == token
		modern := installID != "" && q.Get("install_id") == installID && u.Scheme == canonical.Scheme && u.Host == canonical.Host
		if !legacy && !modern {
			continue
		}
		if u.Path == "/api/apps/messaging/webhooks/ses-inbound" {
			// Remove all project-specific fan-out URLs for this install.
			return true
		}
		if !includeProjectID || projectID == "" || q.Get("project_id") == projectID {
			return true
		}
	}
	return false
}

// Existing installations converge without requiring every domain to be
// registered again. One inbound callback serves all projects on each topic.
func (a *App) reconcileSESInboundSubscriptions(ctx *sdk.AppCtx) error {
	bound := ctx.IntegrationFor("inbound_notifications")
	if bound == nil {
		return nil
	}
	query := `SELECT DISTINCT json_extract(inbound_config,'$.topic_arn') FROM identities
		WHERE kind='email_domain' AND provider IN ('','aws-ses') AND deleted_at IS NULL AND inbound_bootstrapped=1
		AND json_valid(inbound_config) AND COALESCE(json_extract(inbound_config,'$.topic_arn'),'')!=''`
	args := []any{}
	if project := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); project != "" {
		query += ` AND project_id=?`
		args = append(args, project)
	}
	rows, err := ctx.AppDB().Query(query, args...)
	if err != nil {
		return err
	}
	topics := []string{}
	for rows.Next() {
		var topic string
		if err := rows.Scan(&topic); err != nil {
			rows.Close()
			return err
		}
		topics = append(topics, topic)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if configured := strings.TrimSpace(ctx.Config().Get("ses_inbound_topic_arn")); configured != "" {
		found := false
		for _, topic := range topics {
			found = found || topic == configured
		}
		if !found {
			topics = append(topics, configured)
		}
	}
	if len(topics) == 0 {
		return nil
	}
	identity, err := ctx.PlatformAPI().WhoAmI()
	if err != nil {
		return err
	}
	publicURL, err := messagingWebhookPublicURL(ctx, identity)
	if err != nil {
		return err
	}
	endpoint := messagingWebhookURL(publicURL, "/webhooks/ses-inbound", "", false)
	if u, _ := url.Parse(endpoint); u.Query().Get("install_id") == "" {
		return errors.New("SNS reconciliation requires an installation ID")
	}
	var firstErr error
	for _, topic := range topics {
		if _, _, err := bootstrapSubscribeWebhook(ctx, bound.ConnectionID, topic, endpoint); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if _, err := cleanupStaleMessagingSNSSubscriptions(ctx, bound.ConnectionID, topic, []string{endpoint}, "", false); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
