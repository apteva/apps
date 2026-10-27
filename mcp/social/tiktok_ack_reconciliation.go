package main

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

var storedTikTokResponseRanges = regexp.MustCompile(`(?i)bytes[ =]+[0-9]+\s*-\s*[0-9]+(?:\s*/\s*[0-9]+)?`)

// Upgrade the interpretation of the exact saved HTTP response, not the remote
// operation. Never resend bytes, initialize an operation, infer publication, or
// remove first/latest error evidence. A later genuine error remains unresolved.
func (d *deliveryDiagnostics) reconcileTikTokAcknowledgement() {
	d.UploadAcknowledgementResolution = nil
	out := d.LastUpload
	latest := d.LatestUploadError
	if d.ProgressVersion < 1 || d.Method != "FILE_UPLOAD" || d.OperationID == "" || out == nil || latest == nil || out.Result != "invalid_acknowledgement" || latest.Result != out.Result || latest.At != out.At || latest.ContentRange != out.ContentRange || latest.ResponseRange != out.ResponseRange || latest.HTTPStatus != http.StatusCreated || out.HTTPStatus != http.StatusCreated || (out.ExpectedStatus != 0 && out.ExpectedStatus != http.StatusCreated) {
		return
	}
	match := contentRangeACK.FindStringSubmatch(strings.TrimSpace(out.ContentRange))
	if match == nil {
		return
	}
	first, e1 := strconv.ParseInt(match[1], 10, 64)
	last, e2 := strconv.ParseInt(match[2], 10, 64)
	total, e3 := strconv.ParseInt(match[3], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || total <= 0 || last != total-1 || first < 0 || first > last || d.AttemptedBytes == nil || *d.AttemptedBytes < total || d.SourceBytes != total {
		return
	}
	if first > 0 && (d.AcknowledgedBytes == nil || *d.AcknowledgedBytes < first) {
		return
	}
	raw := strings.TrimSpace(out.ResponseRange)
	ranges := storedTikTokResponseRanges.FindAllString(raw, -1)
	if len(ranges) == 0 || strings.Join(strings.Fields(strings.Join(ranges, " ")), "") != strings.Join(strings.Fields(raw), "") {
		return
	}
	headers := http.Header{}
	for _, value := range ranges {
		field := "Range"
		if strings.Contains(value, "/") {
			field = "Content-Range"
		}
		headers.Add(field, value)
	}
	if validateTikTokAcknowledgement(headers, first, last, total) != nil {
		return
	}
	resolved := *out
	resolved.Result = "acknowledged"
	resolved.Evidence = "Saved successful final response normalized as an inclusive endpoint or exclusive end offset; original warning retained. Existing operation only; publication still requires provider verification."
	d.UploadAcknowledgementResolution = &resolved
	if d.AcknowledgedBytes == nil || *d.AcknowledgedBytes < total {
		d.AcknowledgedBytes = bytePointer(total)
	}
	d.TransferredBytes = *d.AcknowledgedBytes
	if newerDeliveryTime(out.At, d.LastProgressAt) {
		d.LastProgressAt = out.At
	}
}
