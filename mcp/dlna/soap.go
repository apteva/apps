// soap.go — UPnP ContentDirectory + ConnectionManager SOAP handlers.
//
// The wire is SOAP 1.1 over HTTP. The fun bit: the `Result` field of
// a Browse response is itself an XML document (DIDL-Lite) but
// transmitted as a single string-escaped XML literal inside the SOAP
// envelope. So every <item>, every <container> we render gets
// escaped and embedded. Hand-rolled string templates beat fighting
// encoding/xml namespaces here — the surface is small and stable.
//
// Object ID grammar (we own this; clients treat it as opaque):
//
//   "0"                        — root
//   "0/audio"                  — audio top-level virtual container
//   "0/video"                  — video top-level virtual container
//   "0/photos"                 — image top-level virtual container
//   "0/recent"                 — newest-first across all kinds
//   "0/folders"                — list of published folders
//   "f:<id>"                   — published folder root (id from DB)
//   "f:<id>/<rel-b64>"         — recursive subfolder, rel-b64 = url-safe
//                                base64 of the path inside the published root
//   "i:<file_id>"              — a leaf item (storage file_id)
//
// The "-1" ParentID convention is preserved on root.
package main

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

// browseRequest is what the TV sends.
type browseRequest struct {
	XMLName        xml.Name `xml:"Browse"`
	ObjectID       string   `xml:"ObjectID"`
	BrowseFlag     string   `xml:"BrowseFlag"` // BrowseDirectChildren | BrowseMetadata
	Filter         string   `xml:"Filter"`
	StartingIndex  int      `xml:"StartingIndex"`
	RequestedCount int      `xml:"RequestedCount"`
	SortCriteria   string   `xml:"SortCriteria"`
}

type searchRequest struct {
	XMLName        xml.Name `xml:"Search"`
	ContainerID    string   `xml:"ContainerID"`
	SearchCriteria string   `xml:"SearchCriteria"`
	Filter         string   `xml:"Filter"`
	StartingIndex  int      `xml:"StartingIndex"`
	RequestedCount int      `xml:"RequestedCount"`
	SortCriteria   string   `xml:"SortCriteria"`
}

// item / container are the rendered DIDL-Lite payloads. We build
// strings directly rather than xml.Marshal because the namespace
// declarations on <DIDL-Lite> need to be on the outer element only,
// and Go's xml package wants to repeat them on children.
type didlContainer struct {
	ID       string
	ParentID string
	Title    string
	Class    string // object.container or object.container.storageFolder
	Count    int    // childCount; 0 for unknown (clients tolerate)
}

type didlItem struct {
	ID          string
	ParentID    string
	Title       string
	Class       string // object.item.audioItem.musicTrack | videoItem | imageItem | …
	Size        int64
	ContentType string
	URL         string
	Duration    string // optional, ISO 8601 / hh:mm:ss(.ms)
	Resolution  string // optional, "WxH"
}

// browseResult is what we ultimately marshal back.
type browseResult struct {
	DIDL           string
	NumberReturned int
	TotalMatches   int
	UpdateID       int
}

// ─── HTTP entry points ──────────────────────────────────────────────

// handleControlContentDirectory routes a SOAP POST to Browse / Search.
// Anything else gets a 401 — UPnP control points only ever invoke
// known actions, so an unknown action is almost always a bug or a
// probe.
func (a *App) handleControlContentDirectory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", 405)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	a.logClient(r)

	action := soapAction(r)
	switch {
	case strings.HasSuffix(action, "#Browse"):
		req, err := parseBrowse(body)
		if err != nil {
			soapFault(w, "401", "Invalid Action")
			return
		}
		res, err := a.contentDirectoryBrowse(r.Context(), req)
		if err != nil {
			soapFault(w, "501", err.Error())
			return
		}
		writeSOAP(w, "BrowseResponse", browseResponseBody(res))
	case strings.HasSuffix(action, "#Search"):
		req, err := parseSearch(body)
		if err != nil {
			soapFault(w, "401", "Invalid Action")
			return
		}
		res, err := a.contentDirectorySearch(r.Context(), req)
		if err != nil {
			soapFault(w, "501", err.Error())
			return
		}
		writeSOAP(w, "SearchResponse", browseResponseBody(res))
	case strings.HasSuffix(action, "#GetSortCapabilities"):
		writeSOAP(w, "GetSortCapabilitiesResponse",
			`<SortCaps>dc:title,dc:date</SortCaps>`)
	case strings.HasSuffix(action, "#GetSearchCapabilities"):
		writeSOAP(w, "GetSearchCapabilitiesResponse",
			`<SearchCaps>dc:title,upnp:class</SearchCaps>`)
	case strings.HasSuffix(action, "#GetSystemUpdateID"):
		writeSOAP(w, "GetSystemUpdateIDResponse", `<Id>1</Id>`)
	default:
		soapFault(w, "401", "Invalid Action")
	}
}

// handleControlConnectionManager — stub. Most TVs don't use anything
// here in practice except GetProtocolInfo, which we answer with the
// MIME types we're willing to source. Sink stays empty (we never
// receive media; we serve it).
func (a *App) handleControlConnectionManager(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", 405)
		return
	}
	action := soapAction(r)
	switch {
	case strings.HasSuffix(action, "#GetProtocolInfo"):
		writeSOAP(w, "GetProtocolInfoResponse",
			`<Source>`+protocolInfoSourceList()+`</Source><Sink></Sink>`)
	case strings.HasSuffix(action, "#GetCurrentConnectionIDs"):
		writeSOAP(w, "GetCurrentConnectionIDsResponse", `<ConnectionIDs>0</ConnectionIDs>`)
	case strings.HasSuffix(action, "#GetCurrentConnectionInfo"):
		// Single virtual connection 0.
		writeSOAP(w, "GetCurrentConnectionInfoResponse", strings.Join([]string{
			`<RcsID>-1</RcsID>`,
			`<AVTransportID>-1</AVTransportID>`,
			`<ProtocolInfo></ProtocolInfo>`,
			`<PeerConnectionManager></PeerConnectionManager>`,
			`<PeerConnectionID>-1</PeerConnectionID>`,
			`<Direction>Output</Direction>`,
			`<Status>OK</Status>`,
		}, ""))
	default:
		soapFault(w, "401", "Invalid Action")
	}
}

// ─── ContentDirectory action: Browse ───────────────────────────────

func (a *App) contentDirectoryBrowse(ctx context.Context, req *browseRequest) (*browseResult, error) {
	if req.RequestedCount <= 0 || req.RequestedCount > 1000 {
		req.RequestedCount = 200
	}
	if req.BrowseFlag == "BrowseMetadata" {
		c, i, err := a.resolveOne(req.ObjectID)
		if err != nil {
			return nil, err
		}
		var didl string
		if c != nil {
			didl = renderDIDL([]didlContainer{*c}, nil)
		} else {
			didl = renderDIDL(nil, []didlItem{*i})
		}
		return &browseResult{DIDL: didl, NumberReturned: 1, TotalMatches: 1, UpdateID: 1}, nil
	}

	containers, items, total, err := a.listChildren(ctx, req.ObjectID, req.StartingIndex, req.RequestedCount, req.SortCriteria)
	if err != nil {
		return nil, err
	}
	didl := renderDIDL(containers, items)
	return &browseResult{
		DIDL:           didl,
		NumberReturned: len(containers) + len(items),
		TotalMatches:   total,
		UpdateID:       1,
	}, nil
}

// listChildren is the heart of the server: take an objectID, fan out
// to storage / DB to compute its direct children. Pagination is
// honoured at the storage layer where possible; for synthetic
// containers (root, type filters) we materialise the full list and
// slice in-memory.
func (a *App) listChildren(ctx context.Context, objectID string, start, count int, sort string) ([]didlContainer, []didlItem, int, error) {
	switch {
	case objectID == "" || objectID == "0":
		// Root — five fixed children.
		all := []didlContainer{
			{ID: "0/audio", ParentID: "0", Title: "Audio", Class: "object.container"},
			{ID: "0/video", ParentID: "0", Title: "Video", Class: "object.container"},
			{ID: "0/photos", ParentID: "0", Title: "Photos", Class: "object.container"},
			{ID: "0/recent", ParentID: "0", Title: "Recent", Class: "object.container"},
			{ID: "0/folders", ParentID: "0", Title: "Folders", Class: "object.container"},
		}
		return paginateContainers(all, start, count), nil, len(all), nil

	case objectID == "0/audio":
		items, err := a.searchByContentTypePrefix(ctx, "audio/", start, count, sort)
		return nil, items, len(items) + start, err
	case objectID == "0/video":
		items, err := a.searchByContentTypePrefix(ctx, "video/", start, count, sort)
		return nil, items, len(items) + start, err
	case objectID == "0/photos":
		items, err := a.searchByContentTypePrefix(ctx, "image/", start, count, sort)
		return nil, items, len(items) + start, err
	case objectID == "0/recent":
		items, err := a.recentItems(ctx, start, count)
		return nil, items, len(items) + start, err

	case objectID == "0/folders":
		folders, err := a.publishedFoldersAsContainers()
		if err != nil {
			return nil, nil, 0, err
		}
		return paginateContainers(folders, start, count), nil, len(folders), nil
	}

	// Per-published-folder browsing. "f:<id>[/<rel-b64>]"
	if strings.HasPrefix(objectID, "f:") {
		pubID, rel, err := decodeFolderID(objectID)
		if err != nil {
			return nil, nil, 0, err
		}
		root, err := a.publishedFolderPath(pubID)
		if err != nil {
			return nil, nil, 0, err
		}
		full := joinFolder(root, rel)
		return a.listStorageFolder(ctx, objectID, full, pubID, rel, start, count)
	}

	return nil, nil, 0, fmt.Errorf("unknown object id: %q", objectID)
}

// listStorageFolder pulls subfolders + files from `storage` and maps
// to DIDL containers / items. Storage's folder list is the
// authoritative tree below a publish root.
func (a *App) listStorageFolder(ctx context.Context, objectID, full string, pubID int64, rel string, start, count int) ([]didlContainer, []didlItem, int, error) {
	subs, err := a.storageListFolders(ctx, full)
	if err != nil {
		return nil, nil, 0, err
	}
	files, err := a.storageListFiles(ctx, full, false /*recursive*/)
	if err != nil {
		return nil, nil, 0, err
	}

	containers := make([]didlContainer, 0, len(subs))
	for _, sub := range subs {
		childRel := childRelative(rel, sub.Name)
		childID := encodeFolderID(pubID, childRel)
		containers = append(containers, didlContainer{
			ID:       childID,
			ParentID: objectID,
			Title:    sub.Name,
			Class:    "object.container.storageFolder",
			Count:    sub.Count,
		})
	}
	items := make([]didlItem, 0, len(files))
	for _, f := range files {
		items = append(items, a.fileToDIDL(ctx, f, objectID))
	}

	all := append([]any{}, containersToAny(containers)...)
	all = append(all, itemsToAny(items)...)
	total := len(all)
	page := paginateAny(all, start, count)
	containers = nil
	items = nil
	for _, x := range page {
		switch v := x.(type) {
		case didlContainer:
			containers = append(containers, v)
		case didlItem:
			items = append(items, v)
		}
	}
	return containers, items, total, nil
}

// resolveOne handles BrowseMetadata: produce a single container/item
// description for the given id. Used when a TV "drills in" — it asks
// for metadata on the container before listing its children.
func (a *App) resolveOne(objectID string) (*didlContainer, *didlItem, error) {
	switch objectID {
	case "", "0":
		return &didlContainer{ID: "0", ParentID: "-1", Title: a.friendlyName(), Class: "object.container"}, nil, nil
	case "0/audio":
		return &didlContainer{ID: "0/audio", ParentID: "0", Title: "Audio", Class: "object.container"}, nil, nil
	case "0/video":
		return &didlContainer{ID: "0/video", ParentID: "0", Title: "Video", Class: "object.container"}, nil, nil
	case "0/photos":
		return &didlContainer{ID: "0/photos", ParentID: "0", Title: "Photos", Class: "object.container"}, nil, nil
	case "0/recent":
		return &didlContainer{ID: "0/recent", ParentID: "0", Title: "Recent", Class: "object.container"}, nil, nil
	case "0/folders":
		return &didlContainer{ID: "0/folders", ParentID: "0", Title: "Folders", Class: "object.container"}, nil, nil
	}
	if strings.HasPrefix(objectID, "f:") {
		pubID, rel, err := decodeFolderID(objectID)
		if err != nil {
			return nil, nil, err
		}
		title := rel
		parent := "0/folders"
		if rel == "" {
			pub, err := a.publishedFolderRow(pubID)
			if err != nil {
				return nil, nil, err
			}
			title = pub.Display()
		} else {
			parent = encodeFolderID(pubID, parentRel(rel))
			if parent == objectID {
				parent = "0/folders"
			}
			title = path.Base(rel)
		}
		return &didlContainer{ID: objectID, ParentID: parent, Title: title, Class: "object.container.storageFolder"}, nil, nil
	}
	if strings.HasPrefix(objectID, "i:") {
		fileID, err := strconv.ParseInt(strings.TrimPrefix(objectID, "i:"), 10, 64)
		if err != nil {
			return nil, nil, err
		}
		f, err := a.storageGetFile(context.Background(), fileID)
		if err != nil {
			return nil, nil, err
		}
		it := a.fileToDIDL(context.Background(), *f, "0")
		return nil, &it, nil
	}
	return nil, nil, fmt.Errorf("unknown object id: %q", objectID)
}

// ─── Search (best-effort) ──────────────────────────────────────────

// contentDirectorySearch implements a small useful subset of UPnP
// search criteria. Real-world TVs send predicates like:
//
//   upnp:class derivedfrom "object.item.audioItem"
//   dc:title contains "summer"
//
// We pull out the class hint and the title-contains needle, and
// otherwise treat Search as a flat scan filtered by content_type.
func (a *App) contentDirectorySearch(ctx context.Context, req *searchRequest) (*browseResult, error) {
	if req.RequestedCount <= 0 || req.RequestedCount > 1000 {
		req.RequestedCount = 200
	}
	contentPrefix := ""
	switch {
	case strings.Contains(req.SearchCriteria, "audioItem"):
		contentPrefix = "audio/"
	case strings.Contains(req.SearchCriteria, "videoItem"):
		contentPrefix = "video/"
	case strings.Contains(req.SearchCriteria, "imageItem"):
		contentPrefix = "image/"
	}
	query := extractSearchTerm(req.SearchCriteria)

	items, err := a.searchStorage(ctx, contentPrefix, query, req.StartingIndex, req.RequestedCount)
	if err != nil {
		return nil, err
	}
	didl := renderDIDL(nil, items)
	return &browseResult{
		DIDL:           didl,
		NumberReturned: len(items),
		TotalMatches:   len(items) + req.StartingIndex, // approximate
		UpdateID:       1,
	}, nil
}

// extractSearchTerm pulls the first quoted string from a criteria
// expression like `dc:title contains "harry"`.
func extractSearchTerm(crit string) string {
	i := strings.IndexByte(crit, '"')
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(crit[i+1:], '"')
	if j < 0 {
		return ""
	}
	return crit[i+1 : i+1+j]
}

// ─── DIDL-Lite rendering ───────────────────────────────────────────

const didlHeader = `<DIDL-Lite ` +
	`xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" ` +
	`xmlns:dc="http://purl.org/dc/elements/1.1/" ` +
	`xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">`

const didlFooter = `</DIDL-Lite>`

func renderDIDL(containers []didlContainer, items []didlItem) string {
	var b strings.Builder
	b.WriteString(didlHeader)
	for _, c := range containers {
		fmt.Fprintf(&b,
			`<container id="%s" parentID="%s" restricted="1" childCount="%d">`+
				`<dc:title>%s</dc:title>`+
				`<upnp:class>%s</upnp:class>`+
				`</container>`,
			xmlAttr(c.ID), xmlAttr(c.ParentID), c.Count, xmlText(c.Title), xmlText(c.Class),
		)
	}
	for _, it := range items {
		fmt.Fprintf(&b,
			`<item id="%s" parentID="%s" restricted="1">`+
				`<dc:title>%s</dc:title>`+
				`<upnp:class>%s</upnp:class>`,
			xmlAttr(it.ID), xmlAttr(it.ParentID), xmlText(it.Title), xmlText(it.Class),
		)
		if it.Duration != "" || it.Resolution != "" || it.Size > 0 {
			b.WriteString(`<res `)
			b.WriteString(`protocolInfo="http-get:*:` + xmlAttr(it.ContentType) + `:DLNA.ORG_OP=01"`)
			if it.Size > 0 {
				fmt.Fprintf(&b, ` size="%d"`, it.Size)
			}
			if it.Duration != "" {
				fmt.Fprintf(&b, ` duration="%s"`, xmlAttr(it.Duration))
			}
			if it.Resolution != "" {
				fmt.Fprintf(&b, ` resolution="%s"`, xmlAttr(it.Resolution))
			}
			b.WriteString(`>`)
			b.WriteString(xmlText(it.URL))
			b.WriteString(`</res>`)
		} else {
			fmt.Fprintf(&b, `<res protocolInfo="http-get:*:%s:*">%s</res>`,
				xmlAttr(it.ContentType), xmlText(it.URL))
		}
		b.WriteString(`</item>`)
	}
	b.WriteString(didlFooter)
	return b.String()
}

// browseResponseBody wraps a browseResult into the inner SOAP body
// fragment. Result is double-escaped — DIDL-Lite XML embedded as a
// string literal, so we html-escape it once on the way in.
func browseResponseBody(r *browseResult) string {
	return strings.Join([]string{
		`<Result>` + html.EscapeString(r.DIDL) + `</Result>`,
		fmt.Sprintf(`<NumberReturned>%d</NumberReturned>`, r.NumberReturned),
		fmt.Sprintf(`<TotalMatches>%d</TotalMatches>`, r.TotalMatches),
		fmt.Sprintf(`<UpdateID>%d</UpdateID>`, r.UpdateID),
	}, "")
}

// writeSOAP emits a complete SOAP envelope for an action response.
// All UPnP responses share the same envelope shape — only the inner
// action element name and body vary.
func writeSOAP(w http.ResponseWriter, action, body string) {
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.Header().Set("EXT", "")
	w.Header().Set("SERVER", "Apteva/1.0 UPnP/1.0 dlna/0.1")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?>`+
		`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" `+
		`s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">`+
		`<s:Body>`+
		`<u:%s xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1">`+
		`%s`+
		`</u:%s>`+
		`</s:Body></s:Envelope>`,
		action, body, action)
}

func soapFault(w http.ResponseWriter, code, msg string) {
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.WriteHeader(500)
	fmt.Fprintf(w, `<?xml version="1.0"?>`+
		`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" `+
		`s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">`+
		`<s:Body><s:Fault>`+
		`<faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring>`+
		`<detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0">`+
		`<errorCode>%s</errorCode><errorDescription>%s</errorDescription>`+
		`</UPnPError></detail>`+
		`</s:Fault></s:Body></s:Envelope>`,
		xmlText(code), xmlText(msg))
}

// soapAction extracts the action name from the SOAPACTION header.
// Example: `"urn:schemas-upnp-org:service:ContentDirectory:1#Browse"`
func soapAction(r *http.Request) string {
	return strings.Trim(r.Header.Get("SOAPACTION"), `"`)
}

// parseBrowse / parseSearch — pull the action element out of the
// SOAP envelope. We don't enforce the namespace because TVs vary in
// how strictly they emit prefixes; we just locate the inner element.
func parseBrowse(body []byte) (*browseRequest, error) {
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Browse" {
			var br browseRequest
			if err := dec.DecodeElement(&br, &se); err != nil {
				return nil, err
			}
			return &br, nil
		}
	}
}

func parseSearch(body []byte) (*searchRequest, error) {
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Search" {
			var sr searchRequest
			if err := dec.DecodeElement(&sr, &se); err != nil {
				return nil, err
			}
			return &sr, nil
		}
	}
}

// ─── helpers ────────────────────────────────────────────────────────

func xmlAttr(s string) string { return strings.NewReplacer(`"`, "&quot;", `&`, "&amp;", `<`, "&lt;").Replace(s) }
func xmlText(s string) string { return strings.NewReplacer(`&`, "&amp;", `<`, "&lt;", `>`, "&gt;").Replace(s) }

func paginateContainers(in []didlContainer, start, count int) []didlContainer {
	if start >= len(in) {
		return nil
	}
	end := start + count
	if end > len(in) {
		end = len(in)
	}
	return in[start:end]
}

func paginateAny(in []any, start, count int) []any {
	if start >= len(in) {
		return nil
	}
	end := start + count
	if end > len(in) {
		end = len(in)
	}
	return in[start:end]
}

func containersToAny(in []didlContainer) []any {
	out := make([]any, len(in))
	for i, c := range in {
		out[i] = c
	}
	return out
}

func itemsToAny(in []didlItem) []any {
	out := make([]any, len(in))
	for i, x := range in {
		out[i] = x
	}
	return out
}

// folder ID encoding — "f:<id>" or "f:<id>/<rel-b64>". Storage paths
// can contain anything, so we url-safe base64 the relative portion.
func encodeFolderID(pubID int64, rel string) string {
	if rel == "" {
		return fmt.Sprintf("f:%d", pubID)
	}
	return fmt.Sprintf("f:%d/%s", pubID, base64.RawURLEncoding.EncodeToString([]byte(rel)))
}

func decodeFolderID(id string) (int64, string, error) {
	body := strings.TrimPrefix(id, "f:")
	parts := strings.SplitN(body, "/", 2)
	pubID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, "", err
	}
	if len(parts) == 1 {
		return pubID, "", nil
	}
	relB, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, "", err
	}
	return pubID, string(relB), nil
}

func childRelative(parentRel, childName string) string {
	if parentRel == "" {
		return childName
	}
	return parentRel + "/" + childName
}

func parentRel(rel string) string {
	if rel == "" {
		return ""
	}
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return ""
}

func joinFolder(root, rel string) string {
	if rel == "" {
		return root
	}
	if strings.HasSuffix(root, "/") {
		return root + rel
	}
	return root + "/" + rel
}

// protocolInfoSourceList — the comma-joined list of protocols we'll
// serve. Not exhaustive; common ones cover typical libraries.
func protocolInfoSourceList() string {
	prots := []string{
		"http-get:*:audio/mpeg:*",
		"http-get:*:audio/mp4:*",
		"http-get:*:audio/flac:*",
		"http-get:*:audio/ogg:*",
		"http-get:*:audio/wav:*",
		"http-get:*:video/mp4:*",
		"http-get:*:video/x-matroska:*",
		"http-get:*:video/quicktime:*",
		"http-get:*:video/webm:*",
		"http-get:*:image/jpeg:*",
		"http-get:*:image/png:*",
		"http-get:*:image/heic:*",
	}
	return strings.Join(prots, ",")
}

// formatDuration renders an integer second count as "H:MM:SS".
func formatDuration(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	d := time.Duration(seconds) * time.Second
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	s := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%d:%02d:%02d", h, m, s)
}
