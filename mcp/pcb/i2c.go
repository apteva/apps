package main

import (
	"fmt"
	"sort"
	"strings"
)

// I2CDevice is one addressed I2C target on a board.
type I2CDevice struct {
	DesignID    int64  `json:"design_id,omitempty"`
	DesignName  string `json:"design_name,omitempty"`
	ComponentID string `json:"component_id"`
	Designator  string `json:"designator"`
	Name        string `json:"name"`
	Address     int    `json:"address"`
	AddressHex  string `json:"address_hex"`
	BusNetID    string `json:"bus_net_id,omitempty"`
	BusNetName  string `json:"bus_net_name,omitempty"`
}

// I2CConflict is two or more devices answering at the same address on one bus.
type I2CConflict struct {
	Address    int         `json:"address"`
	AddressHex string      `json:"address_hex"`
	Bus        string      `json:"bus"`
	Devices    []I2CDevice `json:"devices"`
}

// i2cDevices lists components with an I2C address and the SDA net they hang
// on. The bus is identified by the net connected to a pin named SDA (or
// SDA/x); a device without one is reported on an "unconnected" bus.
func i2cDevices(def *Definition) []I2CDevice {
	netOf := map[string]Net{}
	for _, net := range def.Nets {
		for _, node := range net.Nodes {
			netOf[node.ComponentID+":"+node.PinID] = net
		}
	}
	out := []I2CDevice{}
	for _, c := range def.Components {
		if c.Model == nil || c.Model.I2CAddress <= 0 {
			continue
		}
		d := I2CDevice{ComponentID: c.ID, Designator: c.Designator, Name: c.Name, Address: c.Model.I2CAddress, AddressHex: fmt.Sprintf("0x%02X", c.Model.I2CAddress)}
		for _, pin := range c.Pins {
			if !isSDAPin(pin) {
				continue
			}
			if net, ok := netOf[c.ID+":"+pin.ID]; ok {
				d.BusNetID, d.BusNetName = net.ID, net.Name
				break
			}
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return naturalLess(out[i].Designator, out[j].Designator) })
	return out
}

func isSDAPin(pin Pin) bool {
	for _, field := range []string{pin.Name, pin.ID} {
		upper := strings.ToUpper(field)
		for _, part := range strings.FieldsFunc(upper, func(r rune) bool { return r == '/' || r == '_' || r == '-' || r == ' ' || r == '.' }) {
			if part == "SDA" {
				return true
			}
		}
	}
	return false
}

// i2cConflicts groups devices by bus and address. busKey decides which
// devices share a bus: within one board it is the SDA net; across chained
// blocks the caller passes a function that maps every device to one bus.
func i2cConflicts(devices []I2CDevice, busKey func(I2CDevice) string) []I2CConflict {
	groups := map[string][]I2CDevice{}
	for _, d := range devices {
		key := busKey(d) + "\x00" + fmt.Sprint(d.Address)
		groups[key] = append(groups[key], d)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []I2CConflict{}
	for _, k := range keys {
		g := groups[k]
		if len(g) < 2 {
			continue
		}
		out = append(out, I2CConflict{Address: g[0].Address, AddressHex: g[0].AddressHex, Bus: strings.SplitN(k, "\x00", 2)[0], Devices: g})
	}
	return out
}

func boardBusKey(d I2CDevice) string {
	if d.BusNetID == "" {
		return "unconnected"
	}
	return d.BusNetID
}

// i2cChecks is the single-board validation: devices on the same SDA net must
// have distinct addresses. Out-of-range 7-bit addresses are errors too.
func i2cChecks(def *Definition) []Check {
	checks := []Check{}
	devices := i2cDevices(def)
	for _, d := range devices {
		if d.Address < 0x08 || d.Address > 0x77 {
			checks = append(checks, Check{Code: "I2C_ADDRESS_RESERVED", Severity: "warning", Message: fmt.Sprintf("%s uses I2C address %s, outside the 0x08-0x77 range for normal 7-bit targets", d.Designator, d.AddressHex), ObjectIDs: []string{d.ComponentID}})
		}
	}
	for _, c := range i2cConflicts(devices, boardBusKey) {
		ids, names := []string{}, []string{}
		for _, d := range c.Devices {
			ids, names = append(ids, d.ComponentID), append(names, d.Designator)
		}
		bus := c.Bus
		if bus == "unconnected" {
			bus = "an unconnected bus (no SDA net found)"
		}
		checks = append(checks, Check{Code: "I2C_ADDRESS_CONFLICT", Severity: "error", Message: fmt.Sprintf("%s share I2C address %s on %s", strings.Join(names, ", "), c.AddressHex, bus), ObjectIDs: ids})
	}
	return checks
}
