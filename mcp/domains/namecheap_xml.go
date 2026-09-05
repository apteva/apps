package main

import (
	"encoding/xml"
	"fmt"
	"strings"
)

type namecheapHostResult struct {
	EmailType     string
	Domain        string
	IsUsingOurDNS string
	Hosts         []namecheapHost
}

// Published examples use Host while production responses also use host.
// Unknown children must not silently become an empty, replaceable zone.
func (h *namecheapHostResult) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for _, a := range start.Attr {
		switch strings.ToLower(a.Name.Local) {
		case "emailtype":
			h.EmailType = a.Value
		case "domain":
			h.Domain = a.Value
		case "isusingourdns":
			h.IsUsingOurDNS = a.Value
		}
	}
	for {
		token, err := d.Token()
		if err != nil {
			return err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if !strings.EqualFold(t.Name.Local, "host") {
				return fmt.Errorf("unrecognized Namecheap zone element %q", t.Name.Local)
			}
			var host namecheapHost
			if err := d.DecodeElement(&host, &t); err != nil {
				return err
			}
			h.Hosts = append(h.Hosts, host)
		case xml.EndElement:
			if t.Name == start.Name {
				return nil
			}
		}
	}
}
