package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/storage"
	"github.com/mochi-mqtt/server/v2/packets"
)

// retainedHook makes the broker's retained-message index durable in the app
// database. Mochi calls StoredRetainedMessages before accepting traffic and
// calls OnRetainMessage whenever a retained value is added, replaced, or
// cleared.
type retainedHook struct {
	mqtt.HookBase
	app *App
}

type retainedMessage struct {
	Topic     string `json:"topic"`
	QoS       int    `json:"qos"`
	Payload   any    `json:"payload"`
	SizeBytes int    `json:"size_bytes"`
	UpdatedAt string `json:"updated_at"`
}

func listRetainedMessages(db *sql.DB, topicPattern string, limit int) ([]retainedMessage, error) {
	if topicPattern != "" && !mqtt.IsValidFilter(topicPattern, false) {
		return nil, fmt.Errorf("topic_pattern %q is not a valid MQTT filter", topicPattern)
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := db.Query(`SELECT topic, substr(payload, 1, 4096), length(payload), qos, updated_at FROM mqtt_retained ORDER BY topic`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]retainedMessage, 0, limit)
	for rows.Next() {
		var topic, updated string
		var payload []byte
		var qos, payloadSize int
		if err := rows.Scan(&topic, &payload, &payloadSize, &qos, &updated); err != nil {
			return nil, err
		}
		if topicPattern != "" && !mqttTopicMatch(topicPattern, topic) {
			continue
		}
		out = append(out, makeRetainedMessage(topic, payload, payloadSize, qos, updated, false))
		if len(out) == limit {
			break
		}
	}
	return out, rows.Err()
}

func getRetainedMessage(db *sql.DB, topic string) (*retainedMessage, error) {
	var updated string
	var payload []byte
	var qos int
	err := db.QueryRow(`SELECT payload, qos, updated_at FROM mqtt_retained WHERE topic = ?`, topic).
		Scan(&payload, &qos, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("retained message %q not found", topic)
	}
	if err != nil {
		return nil, err
	}
	out := makeRetainedMessage(topic, payload, len(payload), qos, updated, true)
	return &out, nil
}

func makeRetainedMessage(topic string, payload []byte, payloadSize, qos int, updated string, full bool) retainedMessage {
	const previewLimit = 4096
	visible := payload
	omitted := !full && payloadSize > len(visible)
	if !full && len(visible) > previewLimit {
		visible = visible[:previewLimit]
		omitted = true
	}
	value := any(string(visible))
	if !isPrintableUTF8(visible) {
		value = map[string]any{"binary": true, "base64": base64.StdEncoding.EncodeToString(visible)}
	}
	if omitted {
		value = map[string]any{"preview": value, "truncated": true}
	}
	return retainedMessage{Topic: topic, QoS: qos, Payload: value, SizeBytes: payloadSize, UpdatedAt: updated}
}

func (h *retainedHook) ID() string { return "apteva-retained" }

func (h *retainedHook) Provides(b byte) bool {
	return b == mqtt.OnRetainMessage || b == mqtt.OnRetainedExpired || b == mqtt.StoredRetainedMessages
}

func (h *retainedHook) OnRetainMessage(_ *mqtt.Client, pk packets.Packet, result int64) {
	if result == -1 || len(pk.Payload) == 0 {
		_, _ = h.app.ctx.AppDB().Exec(`DELETE FROM mqtt_retained WHERE topic = ?`, pk.TopicName)
		return
	}
	props, _ := json.Marshal(storageProperties(pk.Properties))
	_, _ = h.app.ctx.AppDB().Exec(
		`INSERT INTO mqtt_retained(topic, payload, qos, properties, updated_at)
		 VALUES (?,?,?,?,CURRENT_TIMESTAMP)
		 ON CONFLICT(topic) DO UPDATE SET
		   payload = excluded.payload,
		   qos = excluded.qos,
		   properties = excluded.properties,
		   updated_at = CURRENT_TIMESTAMP`,
		pk.TopicName, pk.Payload, pk.FixedHeader.Qos, string(props),
	)
}

func (h *retainedHook) OnRetainedExpired(topic string) {
	_, _ = h.app.ctx.AppDB().Exec(`DELETE FROM mqtt_retained WHERE topic = ?`, topic)
}

func (h *retainedHook) StoredRetainedMessages() ([]storage.Message, error) {
	rows, err := h.app.ctx.AppDB().Query(
		`SELECT topic, payload, qos, properties, unixepoch(updated_at) FROM mqtt_retained ORDER BY topic`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []storage.Message{}
	for rows.Next() {
		var topic, propsJSON string
		var payload []byte
		var qos byte
		var created int64
		if err := rows.Scan(&topic, &payload, &qos, &propsJSON, &created); err != nil {
			return nil, err
		}
		var props storage.MessageProperties
		_ = json.Unmarshal([]byte(propsJSON), &props)
		if created == 0 {
			created = time.Now().Unix()
		}
		out = append(out, storage.Message{
			T: storage.RetainedKey, ID: storage.RetainedKey + ":" + topic,
			TopicName: topic, Payload: append([]byte(nil), payload...), Created: created,
			FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: qos, Retain: true},
			Properties:  props,
		})
	}
	return out, rows.Err()
}

func storageProperties(p packets.Properties) storage.MessageProperties {
	return storage.MessageProperties{
		PayloadFormat: p.PayloadFormat, PayloadFormatFlag: p.PayloadFormatFlag,
		MessageExpiryInterval: p.MessageExpiryInterval, ContentType: p.ContentType,
		ResponseTopic: p.ResponseTopic, CorrelationData: append([]byte(nil), p.CorrelationData...),
		SubscriptionIdentifier: append([]int(nil), p.SubscriptionIdentifier...),
		TopicAlias:             p.TopicAlias, User: append([]packets.UserProperty(nil), p.User...),
	}
}
