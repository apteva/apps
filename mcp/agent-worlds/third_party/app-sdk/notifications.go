package sdk

import (
	"fmt"
	"regexp"
	"strings"
)

// NotificationSpec opts an existing published event into user notifications.
// Recipient IDs must be platform user IDs, never app-local account IDs. The
// platform intersects this audience with current installation/project access.
type NotificationSpec struct {
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Audience is "project" (all authorized subscribers) or "recipients".
	Audience        string `yaml:"audience" json:"audience"`
	RecipientsField string `yaml:"recipients_field,omitempty" json:"recipients_field,omitempty"`
	Title           string `yaml:"title" json:"title"`
	Body            string `yaml:"body,omitempty" json:"body,omitempty"`
	// Link is a query string for this app's dashboard page, e.g. ?task={task.id}.
	// It cannot navigate to another app or external origin. Values are URL escaped.
	Link     string               `yaml:"link,omitempty" json:"link,omitempty"`
	GroupBy  string               `yaml:"group_by,omitempty" json:"group_by,omitempty"`
	Defaults NotificationChannels `yaml:"defaults,omitempty" json:"defaults"`
	Filters  []NotificationFilter `yaml:"filters,omitempty" json:"filters,omitempty"`
}

type NotificationChannels struct {
	InApp   bool `yaml:"in_app" json:"in_app"`
	Tab     bool `yaml:"tab" json:"tab"`
	Desktop bool `yaml:"desktop" json:"desktop"`
	Mobile  bool `yaml:"mobile" json:"mobile"`
}

type NotificationFilter struct {
	Field string `yaml:"field" json:"field"`
	Label string `yaml:"label" json:"label"`
	// "$me" compares a payload field to the authenticated platform user ID.
	// An empty Options list permits a resource ID supplied by the user/app UI.
	Options []NotificationFilterOption `yaml:"options,omitempty" json:"options,omitempty"`
}
type NotificationFilterOption struct {
	Value string `yaml:"value" json:"value"`
	Label string `yaml:"label" json:"label"`
}

var notificationField = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*$`)
var notificationID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,79}$`)
var notificationTemplate = regexp.MustCompile(`\{([^{}]+)\}`)

func validateNotificationDeclarations(events []EventDecl) error {
	seen := map[string]bool{}
	for _, event := range events {
		n := event.Notification
		if n == nil {
			continue
		}
		if !notificationID.MatchString(n.ID) || seen[n.ID] {
			return fmt.Errorf("notification ID %q must be valid and unique", n.ID)
		}
		seen[n.ID] = true
		if event.Name == "" || n.Name == "" || n.Title == "" {
			return fmt.Errorf("notification %s requires event, name and title", n.ID)
		}
		if n.Audience != "project" && n.Audience != "recipients" {
			return fmt.Errorf("notification %s audience must be project or recipients", n.ID)
		}
		if n.Audience == "recipients" && !notificationField.MatchString(n.RecipientsField) {
			return fmt.Errorf("notification %s requires recipients_field", n.ID)
		}
		if n.Defaults.Desktop || n.Defaults.Mobile {
			return fmt.Errorf("notification %s desktop/mobile delivery requires user opt-in", n.ID)
		}
		if n.Link != "" && (!strings.HasPrefix(n.Link, "?") || strings.ContainsAny(n.Link, "\r\n\\#")) {
			return fmt.Errorf("notification %s link must be an app query string", n.ID)
		}
		if n.GroupBy != "" && !notificationField.MatchString(n.GroupBy) {
			return fmt.Errorf("invalid notification group_by")
		}
		fields := map[string]bool{}
		for _, f := range n.Filters {
			if !notificationField.MatchString(f.Field) || f.Label == "" || fields[f.Field] {
				return fmt.Errorf("invalid or duplicate notification filter")
			}
			fields[f.Field] = true
		}
		for _, template := range []string{n.Title, n.Body, n.Link} {
			if len(template) > 2048 {
				return fmt.Errorf("notification template too long")
			}
			for _, match := range notificationTemplate.FindAllStringSubmatch(template, -1) {
				if !notificationField.MatchString(match[1]) {
					return fmt.Errorf("invalid notification template field %s", match[1])
				}
			}
		}
	}
	return nil
}
