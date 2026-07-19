package main

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowseRejectsNegativeIndexAndUnknownFlag(t *testing.T) {
	app := &App{}
	app.updateID.Store(9)
	if _, err := app.contentDirectoryBrowse(context.Background(), &browseRequest{BrowseFlag: "BrowseDirectChildren", StartingIndex: -1}); err == nil {
		t.Fatal("negative StartingIndex accepted")
	}
	if _, err := app.contentDirectoryBrowse(context.Background(), &browseRequest{BrowseFlag: "anything", RequestedCount: 10}); err == nil {
		t.Fatal("unknown BrowseFlag accepted")
	}
}

func TestSOAPRequestBodyIsBounded(t *testing.T) {
	app := &App{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/ContentDirectory/control", strings.NewReader(strings.Repeat("x", maxSOAPBodyBytes+1)))
	request.Header.Set("SOAPACTION", `"urn:schemas-upnp-org:service:ContentDirectory:1#Browse"`)
	app.handleControlContentDirectory(recorder, request)
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "Invalid Args") {
		t.Fatalf("oversized SOAP body status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestServiceDescriptionsAreWellFormedXML(t *testing.T) {
	for name, document := range map[string]string{
		"content directory":  scpdContentDirectory,
		"connection manager": scpdConnectionManager,
	} {
		var value struct{ XMLName xml.Name }
		if err := xml.Unmarshal([]byte(document), &value); err != nil {
			t.Errorf("%s SCPD is invalid XML: %v", name, err)
		}
	}
}

func TestConnectionManagerUsesItsOwnSOAPNamespace(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeSOAP(recorder, connectionManagerURN, "GetProtocolInfoResponse", "<Source></Source>")
	body := recorder.Body.String()
	if !strings.Contains(body, `xmlns:u="`+connectionManagerURN+`"`) {
		t.Fatalf("wrong SOAP namespace: %s", body)
	}
	if strings.Contains(body, `xmlns:u="`+contentDirectoryURN+`"`) {
		t.Fatal("ConnectionManager response used ContentDirectory namespace")
	}
}

func TestPaginationIsBoundsSafe(t *testing.T) {
	containers := []didlContainer{{ID: "one"}}
	if got := paginateContainers(containers, -1, 10); len(got) != 0 {
		t.Fatalf("negative page returned %#v", got)
	}
	if got := paginateContainers(containers, 5, 10); len(got) != 0 {
		t.Fatalf("out-of-range page returned %#v", got)
	}
}

func TestUnknownContainerCountIsOmitted(t *testing.T) {
	didl := renderDIDL([]didlContainer{{ID: "folder", ParentID: "0", Title: "Folder", Class: "object.container", Count: -1}}, nil)
	if strings.Contains(didl, "childCount") {
		t.Fatalf("unknown count was advertised as empty: %s", didl)
	}
}

func TestDIDLRemainsValidWithControlCharacters(t *testing.T) {
	didl := renderDIDL(nil, []didlItem{{ID: "i:1", ParentID: "0", Title: "bad\x00name & <x>", Class: "object.item", ContentType: "video/mp4", URL: "http://example/media?id=1"}})
	var document struct{ XMLName xml.Name }
	if err := xml.Unmarshal([]byte(didl), &document); err != nil {
		t.Fatalf("DIDL is invalid XML: %v\n%s", err, didl)
	}
	if strings.ContainsRune(didl, '\x00') {
		t.Fatal("DIDL retained an XML-invalid control character")
	}
}

func TestGENASubscriptionHandshake(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("SUBSCRIBE", "/ContentDirectory/event", nil)
	request.Header.Set("CALLBACK", "<http://192.168.1.20/events>")
	request.Header.Set("NT", "upnp:event")
	stubEvent(recorder, request)
	if recorder.Code != 200 || !strings.HasPrefix(recorder.Header().Get("SID"), "uuid:") {
		t.Fatalf("subscription failed: status=%d SID=%q", recorder.Code, recorder.Header().Get("SID"))
	}
}
