package main

import (
	"encoding/json"
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
