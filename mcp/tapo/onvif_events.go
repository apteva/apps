package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ONVIFEvent struct {
	ID         string
	OccurredAt time.Time
	Kind       string
	Topic      string
	Operation  string
	ValueName  string
	Value      bool
	RawXML     string
}

type ONVIFEventClient struct {
	ip       string
	username string
	password string
	httpc    *http.Client
}

func NewONVIFEventClient(ip, username, password string) *ONVIFEventClient {
	return &ONVIFEventClient{
		ip:       ip,
		username: username,
		password: password,
		httpc: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (c *ONVIFEventClient) PullLoop(ctx context.Context, handle func(ONVIFEvent)) error {
	endpoint, err := c.createSubscription(ctx)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		events, err := c.pullMessages(ctx, endpoint)
		if err != nil {
			return err
		}
		for _, ev := range events {
			handle(ev)
		}
	}
}

func (c *ONVIFEventClient) createSubscription(ctx context.Context) (string, error) {
	req := c.envelope(`<tev:CreatePullPointSubscription/>`)
	raw, err := c.postSOAP(ctx, "http://"+c.ip+":2020/onvif/service", req)
	if err != nil {
		return "", err
	}
	addr := textBetween(raw, "<wsa5:Address>", "</wsa5:Address>")
	if addr == "" {
		addr = textBetween(raw, "<wsa:Address>", "</wsa:Address>")
	}
	if addr == "" {
		return "", fmt.Errorf("onvif subscribe: missing pull endpoint body=%s", snippet(raw))
	}
	return addr, nil
}

func (c *ONVIFEventClient) pullMessages(ctx context.Context, endpoint string) ([]ONVIFEvent, error) {
	req := c.envelope(`<tev:PullMessages><tev:Timeout>PT10S</tev:Timeout><tev:MessageLimit>20</tev:MessageLimit></tev:PullMessages>`)
	raw, err := c.postSOAP(ctx, endpoint, req)
	if err != nil {
		return nil, err
	}
	return parseONVIFEvents(raw), nil
}

func (c *ONVIFEventClient) postSOAP(ctx context.Context, endpoint string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("onvif http %d body=%s", resp.StatusCode, snippet(raw))
	}
	if bytes.Contains(raw, []byte("<SOAP-ENV:Fault>")) || bytes.Contains(raw, []byte("<s:Fault>")) {
		return nil, fmt.Errorf("onvif fault body=%s", snippet(raw))
	}
	return raw, nil
}

func (c *ONVIFEventClient) envelope(body string) []byte {
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	nonce64 := base64.StdEncoding.EncodeToString(nonce)
	created := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	sum := sha1.Sum([]byte(string(nonce) + created + c.password))
	digest := base64.StdEncoding.EncodeToString(sum[:])
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:tev="http://www.onvif.org/ver10/events/wsdl" xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd" xmlns:wsu="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">
  <s:Header>
    <wsse:Security>
      <wsse:UsernameToken>
        <wsse:Username>%s</wsse:Username>
        <wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">%s</wsse:Password>
        <wsse:Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">%s</wsse:Nonce>
        <wsu:Created>%s</wsu:Created>
      </wsse:UsernameToken>
    </wsse:Security>
  </s:Header>
  <s:Body>%s</s:Body>
</s:Envelope>`, xmlEscape(c.username), digest, nonce64, created, body))
}

type onvifEnvelope struct {
	Body onvifBody `xml:"Body"`
}

type onvifBody struct {
	Response onvifPullResponse `xml:"PullMessagesResponse"`
}

type onvifPullResponse struct {
	Messages []onvifNotification `xml:"NotificationMessage"`
}

type onvifNotification struct {
	Topic   string       `xml:"Topic"`
	Message onvifMessage `xml:"Message>Message"`
}

type onvifMessage struct {
	Operation string       `xml:"PropertyOperation,attr"`
	UTC       string       `xml:"UtcTime,attr"`
	Source    []simpleItem `xml:"Source>SimpleItem"`
	Data      []simpleItem `xml:"Data>SimpleItem"`
}

type simpleItem struct {
	Name  string `xml:"Name,attr"`
	Value string `xml:"Value,attr"`
}

func parseONVIFEvents(raw []byte) []ONVIFEvent {
	var env onvifEnvelope
	if err := xml.Unmarshal(raw, &env); err != nil {
		return nil
	}
	out := []ONVIFEvent{}
	for _, msg := range env.Body.Response.Messages {
		ev, ok := onvifNotificationToEvent(msg, string(raw))
		if ok {
			out = append(out, ev)
		}
	}
	return out
}

func onvifNotificationToEvent(n onvifNotification, raw string) (ONVIFEvent, bool) {
	topic := strings.TrimSpace(n.Topic)
	if topic == "" || strings.EqualFold(n.Message.Operation, "Deleted") {
		return ONVIFEvent{}, false
	}
	name, value, ok := boolDataItem(n.Message.Data)
	if !ok || !value {
		return ONVIFEvent{}, false
	}
	kind := onvifTopicKind(topic)
	if kind == "" {
		return ONVIFEvent{}, false
	}
	occurredAt, err := time.Parse(time.RFC3339, n.Message.UTC)
	if err != nil {
		occurredAt = time.Now().UTC()
	}
	rule := sourceItem(n.Message.Source, "Rule")
	idParts := []string{"onvif", topic, n.Message.Operation, name, occurredAt.UTC().Format(time.RFC3339Nano)}
	if rule != "" {
		idParts = append(idParts, rule)
	}
	return ONVIFEvent{
		ID:         strings.Join(idParts, ":"),
		OccurredAt: occurredAt.UTC(),
		Kind:       kind,
		Topic:      topic,
		Operation:  n.Message.Operation,
		ValueName:  name,
		Value:      value,
		RawXML:     raw,
	}, true
}

func boolDataItem(items []simpleItem) (string, bool, bool) {
	for _, it := range items {
		if strings.EqualFold(it.Value, "true") {
			return it.Name, true, true
		}
		if strings.EqualFold(it.Value, "false") {
			return it.Name, false, true
		}
	}
	return "", false, false
}

func sourceItem(items []simpleItem, name string) string {
	for _, it := range items {
		if it.Name == name {
			return it.Value
		}
	}
	return ""
}

func onvifTopicKind(topic string) string {
	switch {
	case strings.Contains(topic, "/PeopleDetector/"):
		return "person"
	case strings.Contains(topic, "/CellMotionDetector/"),
		strings.Contains(topic, "/IntrusionDetector/"),
		strings.Contains(topic, "/LineCrossDetector/"),
		strings.Contains(topic, "/TPSmartEventDetector/"),
		strings.Contains(topic, "/TamperDetector/"):
		return "motion"
	default:
		return ""
	}
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func textBetween(raw []byte, start, end string) string {
	s := string(raw)
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	i += len(start)
	j := strings.Index(s[i:], end)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(s[i : i+j])
}
