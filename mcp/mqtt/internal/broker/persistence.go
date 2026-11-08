package mqtt

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/mochi-mqtt/server/v2/packets"
	"sort"
	"sync/atomic"
	"time"
)

var ErrDurableCapacity = errors.New("durable storage capacity reached")

type Delivery struct {
	Client *Client
	Packet packets.Packet
}
type Persistence interface {
	Commit(*Server, *Client, *packets.Packet, []Delivery) error
	Restore(*Server) error
}
type Session struct {
	ID            string
	Properties    ClientProperties
	Subscriptions []packets.Subscription
	Outgoing      []packets.Packet
	Incoming      []packets.Packet
	Disconnected  int64
}

// Sessions is called only inside Commit, under the mutation lock.
func (s *Server) Sessions(planned []Delivery) []Session {
	out := []Session{}
	for _, cl := range s.Clients.GetAll() {
		if cl.Net.Inline || cl.IsTakenOver() {
			continue
		}
		v := Session{ID: cl.ID, Properties: cl.Properties, Disconnected: cl.StopTime(), Outgoing: cl.State.Inflight.GetAll(false), Incoming: cl.State.Incoming.GetAll(false)}
		for _, sub := range cl.State.Subscriptions.GetAll() {
			v.Subscriptions = append(v.Subscriptions, sub)
		}
		for _, d := range planned {
			if d.Client == cl && d.Packet.FixedHeader.Qos > 0 {
				v.Outgoing = append(v.Outgoing, d.Packet)
			}
		}
		out = append(out, v)
	}
	return out
}
func (s *Server) RestoreSession(v Session) *Client {
	cl := s.NewClient(nil, "recovered", v.ID, false)
	cl.Properties = v.Properties
	cl.Stop(nil)
	atomic.StoreInt64(&cl.State.disconnected, v.Disconnected)
	for _, sub := range v.Subscriptions {
		cl.State.Subscriptions.Add(sub.Filter, sub)
		s.Topics.Subscribe(cl.ID, sub)
	}
	for _, pk := range v.Outgoing {
		cl.State.Inflight.Set(pk)
	}
	for _, pk := range v.Incoming {
		cl.State.Incoming.Set(pk)
	}
	s.Clients.Add(cl)
	return cl
}
func (s *Server) RecoverWills() error {
	for _, cl := range s.Clients.GetAll() {
		if !cl.Net.Inline {
			s.sendLWT(cl)
		}
	}
	return s.checkpoint()
}
func (s *Server) checkpoint() error {
	if s.Options.Persistence == nil {
		return nil
	}
	return s.Options.Persistence.Commit(s, nil, nil, nil)
}
func (s *Server) MutateSessions(fn func()) error {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	fn()
	return s.checkpoint()
}
func messageID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "msg_" + hex.EncodeToString(b[:])
}
func MessageID(pk packets.Packet) string {
	for _, p := range pk.Properties.User {
		if p.Key == "apteva.message_id" {
			return p.Val
		}
	}
	return ""
}
func (s *Server) PublishAccepted(topic string, payload []byte, retain bool, qos byte) (packets.Packet, error) {
	s.durableMu.Lock()
	defer s.durableMu.Unlock()
	return s.acceptPublish(s.inlineClient, packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Publish, Qos: qos, Retain: retain}, TopicName: topic, Payload: payload}, false)
}
func (s *Server) acceptPublish(cl *Client, pk packets.Packet, acknowledge bool) (packets.Packet, error) {
	if !IsValidFilter(pk.TopicName, true) {
		return pk, packets.ErrTopicNameInvalid
	}
	if !cl.Net.Inline && !s.hooks.OnACLCheck(cl, pk.TopicName, true) {
		return pk, packets.ErrNotAuthorized
	}
	if !cl.Net.Inline && pk.FixedHeader.Qos == 2 && acknowledge {
		if previous, ok := cl.State.Incoming.Get(pk.PacketID); ok {
			if previous.TopicName != pk.TopicName || !bytes.Equal(previous.Payload, pk.Payload) || previous.FixedHeader.Retain != pk.FixedHeader.Retain || !pk.FixedHeader.Dup {
				return pk, packets.ErrPacketIdentifierInUse
			}
			return previous, cl.WritePacket(s.buildAck(pk.PacketID, packets.Pubrec, 0, packets.Properties{}, packets.CodeSuccess))
		}
		if cl.State.Incoming.Len() >= int(s.Options.Capabilities.ReceiveMaximum) {
			return pk, packets.ErrReceiveMaximum
		}
	}
	pk.Origin = cl.ID
	pk.Created = time.Now().Unix()
	if pk.Properties.MessageExpiryInterval > 0 {
		pk.Expiry = pk.Created + int64(pk.Properties.MessageExpiryInterval)
	}
	pk.Properties.TopicAlias = 0
	pk.Properties.TopicAliasFlag = false
	user := []packets.UserProperty{}
	for _, p := range pk.Properties.User {
		if p.Key != "apteva.message_id" {
			user = append(user, p)
		}
	}
	pk.Properties.User = append(user, packets.UserProperty{Key: "apteva.message_id", Val: messageID()})
	var err error
	pk, err = s.hooks.OnPublish(cl, pk)
	if err != nil {
		return pk, err
	}
	subs := s.Topics.Subscribers(pk.TopicName)
	if len(subs.Shared) > 0 {
		subs = s.hooks.OnSelectSubscribers(subs, pk)
		if len(subs.SharedSelected) == 0 {
			subs.SelectShared()
		}
		subs.MergeSharedSelected()
	}
	plan := []Delivery{}
	for id, sub := range subs.Subscriptions {
		target, ok := s.Clients.Get(id)
		if !ok || (sub.NoLocal && id == cl.ID) || !s.hooks.OnACLCheck(target, pk.TopicName, false) {
			continue
		}
		out := pk.Copy(false)
		out.FixedHeader.Qos = min(pk.FixedHeader.Qos, sub.Qos)
		out.FixedHeader.Qos = min(out.FixedHeader.Qos, s.Options.Capabilities.MaximumQos)
		if !sub.RetainAsPublished || target.Properties.ProtocolVersion < 5 {
			out.FixedHeader.Retain = false
		}
		for _, n := range sub.Identifiers {
			out.Properties.SubscriptionIdentifier = append(out.Properties.SubscriptionIdentifier, n)
		}
		sort.Ints(out.Properties.SubscriptionIdentifier)
		if out.FixedHeader.Qos > 0 {
			if target.State.Inflight.Len() >= int(s.Options.Capabilities.MaximumInflight) {
				return pk, packets.ErrQuotaExceeded
			}
			id, err := target.NextPacketID()
			if err != nil {
				return pk, packets.ErrQuotaExceeded
			}
			out.PacketID = uint16(id)
		}
		plan = append(plan, Delivery{target, out})
	}
	if pk.FixedHeader.Qos == 2 && !cl.Net.Inline && acknowledge {
		cl.State.Incoming.Set(pk)
	}
	if err := s.Options.Persistence.Commit(s, cl, &pk, plan); err != nil {
		if pk.FixedHeader.Qos == 2 && !cl.Net.Inline && acknowledge {
			cl.State.Incoming.Delete(pk.PacketID)
		}
		if errors.Is(err, ErrDurableCapacity) {
			return pk, packets.ErrQuotaExceeded
		}
		return pk, err
	}
	if pk.FixedHeader.Retain {
		s.retainMessage(cl, pk)
	}
	for _, d := range plan {
		target, out := d.Client, d.Packet
		if out.FixedHeader.Qos > 0 {
			target.State.Inflight.Set(out)
			atomic.AddInt64(&s.Info.Inflight, 1)
			if target.Closed() || target.Net.Conn == nil {
				continue
			}
			if atomic.LoadInt32(&target.State.Inflight.sendQuota) <= 0 {
				target.State.Inflight.MarkPending(out.PacketID, true)
				target.State.Inflight.Set(out)
				continue
			}
			target.State.Inflight.DecreaseSendQuota()
		}
		if target.Closed() || target.Net.Conn == nil {
			continue
		}
		select {
		case target.State.outbound <- &out:
			atomic.AddInt32(&target.State.outboundQty, 1)
		default:
			target.Stop(packets.ErrPendingClientWritesExceeded)
		}
	}
	for _, sub := range subs.InlineSubscriptions {
		sub.Handler(s.inlineClient, sub.Subscription, pk)
	}
	s.hooks.OnPublished(cl, pk)
	if acknowledge && !cl.Net.Inline && pk.FixedHeader.Qos > 0 {
		typ := byte(packets.Puback)
		if pk.FixedHeader.Qos == 2 {
			typ = packets.Pubrec
		}
		return pk, cl.WritePacket(s.buildAck(pk.PacketID, typ, 0, packets.Properties{}, packets.CodeSuccess))
	}
	return pk, nil
}
