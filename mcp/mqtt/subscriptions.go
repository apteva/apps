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
)

type BusSubscription struct {
	ID           int64  `json:"id"`
	ProjectID    string `json:"project_id"`
	TopicPattern string `json:"topic_pattern"`
	BusTopic     string `json:"bus_topic"`
	CreatedBy    string `json:"created_by"`
	CreatedAt    string `json:"created_at"`
}

func listBusSubscriptions(db *sql.DB) ([]BusSubscription, error) {
	rows, err := db.Query(
		`SELECT id, project_id, topic_pattern, bus_topic, created_by, created_at
		   FROM mqtt_subscriptions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BusSubscription{}
	for rows.Next() {
		var s BusSubscription
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.TopicPattern, &s.BusTopic, &s.CreatedBy, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func addBusSubscription(db *sql.DB, projectID, topicPattern, busTopic, createdBy string) (*BusSubscription, error) {
	topicPattern = strings.TrimSpace(topicPattern)
	busTopic = strings.TrimSpace(busTopic)
	if topicPattern == "" {
		return nil, errors.New("topic_pattern required")
	}
	if busTopic == "" {
		return nil, errors.New("bus_topic required")
	}
	if !validBusTopic(busTopic) {
		return nil, fmt.Errorf("bus_topic %q invalid (a-z0-9 plus . _ -)", busTopic)
	}
	res, err := db.Exec(
		`INSERT INTO mqtt_subscriptions(project_id, topic_pattern, bus_topic, created_by)
		 VALUES (?,?,?,?)
		 ON CONFLICT(project_id, topic_pattern, bus_topic) DO UPDATE SET created_at = CURRENT_TIMESTAMP`,
		projectID, topicPattern, busTopic, createdBy)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &BusSubscription{
		ID: id, ProjectID: projectID, TopicPattern: topicPattern,
		BusTopic: busTopic, CreatedBy: createdBy,
	}, nil
}

func deleteBusSubscription(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM mqtt_subscriptions WHERE id = ?`, id)
	return err
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
