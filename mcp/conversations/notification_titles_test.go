package main

import "testing"

func TestAlertNotificationLabelsUseExistingSeverity(t *testing.T) {
	for severity, want := range map[string]string{"info": "Info", "warn": "Warning", "error": "Error", "": "Alert", "unknown": "Alert"} {
		if got := conversationAlertLabel(severity); got != want {
			t.Fatalf("%q: %q != %q", severity, got, want)
		}
	}
	manifest := (&App{}).Manifest()
	for _, event := range manifest.Provides.Publishes {
		if event.Name == "conversation.alert.created" {
			if event.Notification == nil || event.Notification.Title != "{alert_label} in {conversation_title}" {
				t.Fatal("missing generic title template")
			}
			return
		}
	}
	t.Fatal("missing alert notification")
}
