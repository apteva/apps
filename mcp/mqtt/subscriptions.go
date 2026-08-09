// subscriptions.go — DB layer for the per-pattern bus-bridge
// subscriptions (mqtt_subscriptions). Each row says "when an MQTT
// message matches topic_pattern, re-emit it on the platform event
// bus under `mqtt.<bus_topic>`". bridgeBusLoopback registers an
// inline subscription per row at boot.

package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	mqtt "github.com/mochi-mqtt/server/v2"
)

type BusSubscription struct {
	ID           int64  `json:"id"`
	ProjectID    string `json:"project_id"`
	TopicPattern string `json:"topic_pattern"`
	BusTopic     string `json:"bus_topic"`
	CreatedBy    string `json:"created_by"`
	CreatedAt    string `json:"created_at"`
}

func listBusSubscriptions(db *sql.DB, projectID string) ([]BusSubscription, error) {
	rows, err := db.Query(
		`SELECT id, project_id, topic_pattern, bus_topic, created_by, created_at
		   FROM mqtt_subscriptions WHERE project_id = ? ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	return scanBusSubscriptions(rows)
}

func listAllBusSubscriptions(db *sql.DB) ([]BusSubscription, error) {
	rows, err := db.Query(
		`SELECT id, project_id, topic_pattern, bus_topic, created_by, created_at
		   FROM mqtt_subscriptions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return scanBusSubscriptions(rows)
}

func scanBusSubscriptions(rows *sql.Rows) ([]BusSubscription, error) {
	defer rows.Close()
	out := []BusSubscription{}
	for rows.Next() {
		var s BusSubscription
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.TopicPattern, &s.BusTopic, &s.CreatedBy, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func addBusSubscription(db *sql.DB, projectID, topicPattern, busTopic, createdBy string) (*BusSubscription, error) {
	topicPattern = strings.TrimSpace(topicPattern)
	busTopic = strings.TrimSpace(busTopic)
	if topicPattern == "" {
		return nil, errors.New("topic_pattern required")
	}
	if !mqtt.IsValidFilter(topicPattern, false) {
		return nil, fmt.Errorf("topic_pattern %q is not a valid MQTT filter", topicPattern)
	}
	if busTopic == "" {
		return nil, errors.New("bus_topic required")
	}
	if !validBusTopic(busTopic) {
		return nil, fmt.Errorf("bus_topic %q invalid (a-z0-9 plus . _ -)", busTopic)
	}
	_, err := db.Exec(
		`INSERT INTO mqtt_subscriptions(project_id, topic_pattern, bus_topic, created_by)
		 VALUES (?,?,?,?)
		 ON CONFLICT(project_id, topic_pattern, bus_topic) DO UPDATE SET created_at = CURRENT_TIMESTAMP`,
		projectID, topicPattern, busTopic, createdBy)
	if err != nil {
		return nil, err
	}
	var out BusSubscription
	err = db.QueryRow(
		`SELECT id, project_id, topic_pattern, bus_topic, created_by, created_at
		   FROM mqtt_subscriptions
		  WHERE project_id = ? AND topic_pattern = ? AND bus_topic = ?`,
		projectID, topicPattern, busTopic,
	).Scan(&out.ID, &out.ProjectID, &out.TopicPattern, &out.BusTopic, &out.CreatedBy, &out.CreatedAt)
	return &out, err
}

func deleteBusSubscription(db *sql.DB, projectID string, id int64) (*BusSubscription, error) {
	var out BusSubscription
	err := db.QueryRow(
		`SELECT id, project_id, topic_pattern, bus_topic, created_by, created_at
		   FROM mqtt_subscriptions WHERE id = ? AND project_id = ?`, id, projectID,
	).Scan(&out.ID, &out.ProjectID, &out.TopicPattern, &out.BusTopic, &out.CreatedBy, &out.CreatedAt)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`DELETE FROM mqtt_subscriptions WHERE id = ? AND project_id = ?`, id, projectID); err != nil {
		return nil, err
	}
	return &out, nil
}

// validBusTopic — bus topics namespace under "mqtt.<name>", so we
// only allow chars that look fine in event-bus topic strings.
func validBusTopic(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}
