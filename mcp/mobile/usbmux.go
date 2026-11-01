package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// usbmuxd (macOS: /var/run/usbmuxd) multiplexes TCP connections to ports on
// USB- or Wi-Fi-paired iOS devices. Only the two plist messages needed to
// reach the on-device runner are implemented.
var usbmuxSocket = "/var/run/usbmuxd"

const (
	usbmuxVersion     = 1
	usbmuxMessagePlst = 8
	maxUsbmuxPacket   = 4 << 20
)

type usbmuxDevice struct {
	ID         int
	UDID       string
	Connection string // USB or Network
}

func usbmuxConnect(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", usbmuxSocket)
	if err != nil {
		return nil, problem("setup_required", "cannot reach usbmuxd; connect the iPhone by USB to this Mac")
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	return c, nil
}

func usbmuxRequest(c net.Conn, tag uint32, fields [][2]any) (map[string]any, error) {
	var body bytes.Buffer
	body.WriteString(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>`)
	for _, f := range fields {
		body.WriteString("<key>" + xmlEscape(f[0].(string)) + "</key>")
		switch v := f[1].(type) {
		case int:
			body.WriteString("<integer>" + strconv.Itoa(v) + "</integer>")
		case string:
			body.WriteString("<string>" + xmlEscape(v) + "</string>")
		}
	}
	body.WriteString("</dict></plist>")
	head := make([]byte, 16)
	binary.LittleEndian.PutUint32(head[0:], uint32(16+body.Len()))
	binary.LittleEndian.PutUint32(head[4:], usbmuxVersion)
	binary.LittleEndian.PutUint32(head[8:], usbmuxMessagePlst)
	binary.LittleEndian.PutUint32(head[12:], tag)
	if _, err := c.Write(append(head, body.Bytes()...)); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(c, head); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(head[0:])
	if n < 16 || n > maxUsbmuxPacket {
		return nil, errors.New("invalid usbmuxd reply")
	}
	payload := make([]byte, n-16)
	if _, err := io.ReadFull(c, payload); err != nil {
		return nil, err
	}
	v, err := parsePlist(payload)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("invalid usbmuxd reply")
	}
	return m, nil
}

func usbmuxDevices(ctx context.Context) ([]usbmuxDevice, error) {
	c, err := usbmuxConnect(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	reply, err := usbmuxRequest(c, 1, [][2]any{{"MessageType", "ListDevices"}, {"ClientVersionString", "apteva-mobile"}, {"ProgName", "apteva-mobile"}})
	if err != nil {
		return nil, err
	}
	list, _ := reply["DeviceList"].([]any)
	out := []usbmuxDevice{}
	for _, item := range list {
		m, _ := item.(map[string]any)
		props, _ := m["Properties"].(map[string]any)
		id, _ := m["DeviceID"].(int)
		serial, _ := props["SerialNumber"].(string)
		conn, _ := props["ConnectionType"].(string)
		if id > 0 && serial != "" {
			out = append(out, usbmuxDevice{ID: id, UDID: serial, Connection: conn})
		}
	}
	return out, nil
}

func sameUDID(a, b string) bool {
	norm := func(s string) string { return strings.ToUpper(strings.ReplaceAll(s, "-", "")) }
	return norm(a) == norm(b)
}

// usbmuxDial opens a raw TCP stream to port on the device with udid,
// preferring a USB link when the device is also visible over Wi-Fi.
func usbmuxDial(ctx context.Context, udid string, port int) (net.Conn, error) {
	devices, err := usbmuxDevices(ctx)
	if err != nil {
		return nil, err
	}
	id := 0
	for _, d := range devices {
		if sameUDID(d.UDID, udid) && (id == 0 || d.Connection == "USB") {
			id = d.ID
		}
	}
	if id == 0 {
		return nil, problem("device_unavailable", "the iPhone is not connected; reconnect it by USB and unlock it")
	}
	c, err := usbmuxConnect(ctx)
	if err != nil {
		return nil, err
	}
	// usbmuxd expects the port in network byte order inside a host integer.
	swapped := int(uint16(port)>>8 | uint16(port)<<8)
	reply, err := usbmuxRequest(c, 2, [][2]any{{"MessageType", "Connect"}, {"DeviceID", id}, {"PortNumber", swapped}, {"ClientVersionString", "apteva-mobile"}, {"ProgName", "apteva-mobile"}})
	if err != nil {
		c.Close()
		return nil, err
	}
	if n, _ := reply["Number"].(int); n != 0 {
		c.Close()
		return nil, fmt.Errorf("usbmuxd refused the connection to device port %d (code %d)", port, n)
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}

// parsePlist decodes the XML plist subset usbmuxd emits: dict, array,
// string, integer, true/false, data (kept as text).
func parsePlist(b []byte) (any, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, errors.New("invalid plist")
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local != "plist" {
			return plistValue(dec, se)
		}
	}
}

func plistValue(dec *xml.Decoder, se xml.StartElement) (any, error) {
	switch se.Name.Local {
	case "dict":
		m := map[string]any{}
		key := ""
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "key" {
					var k string
					if err := dec.DecodeElement(&k, &t); err != nil {
						return nil, err
					}
					key = k
					continue
				}
				v, err := plistValue(dec, t)
				if err != nil {
					return nil, err
				}
				m[key] = v
			case xml.EndElement:
				return m, nil
			}
		}
	case "array":
		list := []any{}
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				v, err := plistValue(dec, t)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			case xml.EndElement:
				return list, nil
			}
		}
	case "true", "false":
		if err := dec.Skip(); err != nil {
			return nil, err
		}
		return se.Name.Local == "true", nil
	default:
		var s string
		if err := dec.DecodeElement(&s, &se); err != nil {
			return nil, err
		}
		if se.Name.Local == "integer" {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil {
				return nil, err
			}
			return n, nil
		}
		return s, nil
	}
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
