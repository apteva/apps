package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Policies belong to destinations. Source files and the post's media IDs are
// immutable; only a destination's delivery copy is normalized.
type imageDeliveryPolicy struct {
	ID             string `json:"id"`
	MaxShortEdge   int    `json:"max_short_edge"`
	MaxLongEdge    int    `json:"max_long_edge"`
	MaxBytes       int    `json:"max_bytes"`
	MaxSourceBytes int    `json:"max_source_bytes"`
	MaxPixels      int64  `json:"max_pixels"`
	JPEGQuality    int    `json:"jpeg_quality"`
	URLTTLSeconds  int    `json:"url_ttl_seconds"`
}

var tikTokPhotoPolicy = imageDeliveryPolicy{
	ID: "tiktok-photo-jpeg-v2", MaxShortEdge: 1080, MaxLongEdge: 1920,
	MaxBytes: 20_000_000, MaxSourceBytes: 25 << 20, MaxPixels: 40_000_000, JPEGQuality: 92, URLTTLSeconds: 7200,
}

type imageDeliveryDiagnostics struct {
	Policy       string `json:"policy"`
	SourceID     int64  `json:"source_file_id"`
	SourceSHA    string `json:"source_sha256"`
	SourceMIME   string `json:"source_mime"`
	SourceWidth  int    `json:"source_width"`
	SourceHeight int    `json:"source_height"`
	SourceBytes  int    `json:"source_bytes"`
	Orientation  int    `json:"source_orientation"`
	DeliveryID   int64  `json:"delivery_file_id"`
	DeliverySHA  string `json:"delivery_sha256"`
	DeliveryMIME string `json:"delivery_mime"`
	Width        int    `json:"delivery_width"`
	Height       int    `json:"delivery_height"`
	Bytes        int    `json:"delivery_bytes"`
	CacheHit     bool   `json:"cache_hit"`
	HTTPStatus   int    `json:"http_status"`
	DeliveryPath string `json:"delivery_path,omitempty"`
	Validation   string `json:"validation"`
}

func imageSHA(body []byte) string { s := sha256.Sum256(body); return hex.EncodeToString(s[:]) }

func decodeDeliveryImage(body []byte, policy imageDeliveryPolicy) (image.Image, string, error) {
	if len(body) == 0 || len(body) > policy.MaxSourceBytes {
		return nil, "", errors.New("image source is empty or exceeds source byte limit")
	}
	c, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("decode image header: %w", err)
	}
	if c.Width <= 0 || c.Height <= 0 || int64(c.Width) > policy.MaxPixels/int64(c.Height) {
		return nil, "", errors.New("decoded image exceeds pixel limit")
	}
	img, actual, err := image.Decode(bytes.NewReader(body))
	if err != nil || actual != format {
		return nil, "", fmt.Errorf("decode image: %v", err)
	}
	return img, "image/" + strings.ReplaceAll(format, "jpg", "jpeg"), nil
}

func deliveryDimensions(w, h int, p imageDeliveryPolicy) (int, int) {
	short, long := min(w, h), max(w, h)
	scale := min(1.0, min(float64(p.MaxShortEdge)/float64(short), float64(p.MaxLongEdge)/float64(long)))
	return max(1, int(math.Floor(float64(w)*scale+0.5))), max(1, int(math.Floor(float64(h)*scale+0.5)))
}

func validateDeliveryImage(body []byte, contentType string, p imageDeliveryPolicy) (int, int, error) {
	if len(body) > p.MaxBytes {
		return 0, 0, errors.New("delivery exceeds image byte limit")
	}
	parsed, _, err := mime.ParseMediaType(contentType)
	if err != nil || parsed != "image/jpeg" {
		return 0, 0, errors.New("delivery Content-Type must be image/jpeg")
	}
	img, actual, err := decodeDeliveryImage(body, p)
	if err != nil {
		return 0, 0, err
	}
	if actual != parsed {
		return 0, 0, errors.New("delivery MIME does not match decoded image")
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if min(w, h) > p.MaxShortEdge || max(w, h) > p.MaxLongEdge {
		return 0, 0, errors.New("delivery exceeds image dimension limits")
	}
	if exifOrientation(body) != 1 {
		return 0, 0, errors.New("delivery EXIF orientation is not normalized")
	}
	return w, h, nil
}

func normalizeDeliveryImage(body []byte, p imageDeliveryPolicy) ([]byte, imageDeliveryDiagnostics, error) {
	d := imageDeliveryDiagnostics{Policy: p.ID, SourceSHA: imageSHA(body), SourceBytes: len(body), Orientation: exifOrientation(body), Validation: "preparing"}
	img, actual, err := decodeDeliveryImage(body, p)
	if err != nil {
		return nil, d, err
	}
	d.SourceMIME = actual
	d.SourceWidth = img.Bounds().Dx()
	d.SourceHeight = img.Bounds().Dy()
	oriented := orientImage(img, d.Orientation)
	w, h := deliveryDimensions(oriented.Bounds().Dx(), oriented.Bounds().Dy(), p)
	// Decode to RGB, composite transparency on white, strip source metadata.
	opaque := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(opaque, opaque.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(opaque, opaque.Bounds(), oriented, oriented.Bounds(), draw.Over, nil)
	var encoded bytes.Buffer
	for quality := p.JPEGQuality; quality >= 50; quality -= 10 {
		encoded.Reset()
		if err = jpeg.Encode(&encoded, opaque, &jpeg.Options{Quality: quality}); err != nil {
			return nil, d, err
		}
		if encoded.Len() <= p.MaxBytes {
			break
		}
	}
	out := encoded.Bytes()
	if _, _, err = validateDeliveryImage(out, "image/jpeg", p); err != nil {
		return nil, d, err
	}
	d.Width = w
	d.Height = h
	d.Bytes = len(out)
	d.DeliveryMIME = "image/jpeg"
	d.DeliverySHA = imageSHA(out)
	d.Validation = "prepared"
	return out, d, nil
}

func orientImage(src image.Image, orientation int) image.Image {
	if orientation == 1 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	ow, oh := w, h
	if orientation >= 5 {
		ow, oh = h, w
	}
	out := image.NewNRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := x, y
			switch orientation {
			case 2:
				dx = w - 1 - x
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dy = h - 1 - y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			out.Set(dx, dy, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

// JPEG APP1, PNG eXIf and WebP EXIF all carry the same bounded TIFF IFD.
func exifOrientation(body []byte) int {
	if bytes.HasPrefix(body, []byte{0xff, 0xd8}) {
		for pos := 2; pos+4 <= len(body); {
			if body[pos] != 0xff {
				break
			}
			marker := body[pos+1]
			pos += 2
			if marker == 0xda || marker == 0xd9 {
				break
			}
			if marker == 0x01 || marker >= 0xd0 && marker <= 0xd7 {
				continue
			}
			n := int(binary.BigEndian.Uint16(body[pos : pos+2]))
			if n < 2 || n > len(body)-pos {
				break
			}
			segment := body[pos+2 : pos+n]
			if marker == 0xe1 && bytes.HasPrefix(segment, []byte("Exif\x00\x00")) {
				return tiffOrientation(segment[6:])
			}
			pos += n
		}
	} else if bytes.HasPrefix(body, []byte("\x89PNG\r\n\x1a\n")) {
		for pos := 8; pos+12 <= len(body); {
			n := uint64(binary.BigEndian.Uint32(body[pos : pos+4]))
			if n > uint64(len(body)-pos-12) {
				break
			}
			if string(body[pos+4:pos+8]) == "eXIf" {
				return tiffOrientation(body[pos+8 : pos+8+int(n)])
			}
			pos += 12 + int(n)
		}
	} else if len(body) >= 12 && string(body[:4]) == "RIFF" && string(body[8:12]) == "WEBP" {
		for pos := 12; pos+8 <= len(body); {
			n := uint64(binary.LittleEndian.Uint32(body[pos+4 : pos+8]))
			if n > uint64(len(body)-pos-8) {
				break
			}
			if string(body[pos:pos+4]) == "EXIF" {
				payload := body[pos+8 : pos+8+int(n)]
				payload = bytes.TrimPrefix(payload, []byte("Exif\x00\x00"))
				return tiffOrientation(payload)
			}
			pos += 8 + int(n) + int(n%2)
		}
	}
	return 1
}
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(t[2:4]) != 42 {
		return 1
	}
	offset := uint64(order.Uint32(t[4:8]))
	if offset > uint64(len(t)-2) {
		return 1
	}
	count := int(order.Uint16(t[offset : offset+2]))
	pos := int(offset) + 2
	for i := 0; i < count && pos+12 <= len(t); i++ {
		entry := t[pos : pos+12]
		pos += 12
		if order.Uint16(entry[:2]) == 0x112 && order.Uint16(entry[2:4]) == 3 && order.Uint32(entry[4:8]) == 1 {
			o := int(order.Uint16(entry[8:10]))
			if o >= 1 && o <= 8 {
				return o
			}
		}
	}
	return 1
}

func (a *App) prepareImageDelivery(ctx *sdk.AppCtx, item mediaItem, projectID string, p imageDeliveryPolicy) (mediaItem, imageDeliveryDiagnostics, error) {
	d := imageDeliveryDiagnostics{Policy: p.ID, SourceID: item.ID, Validation: "preparing"}
	if item.ID <= 0 {
		return mediaItem{}, d, errors.New("storage file id is missing")
	}
	if projectID == "" {
		projectID = projectScope(ctx)
	}
	args := map[string]any{"id": item.ID, "_project_id": projectID}
	var source struct {
		ContentBase64 string `json:"content_base64"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_content", args, &source); err != nil {
		return mediaItem{}, d, fmt.Errorf("read source from storage: %w", err)
	}
	if len(source.ContentBase64) > base64.StdEncoding.EncodedLen(p.MaxSourceBytes) {
		return mediaItem{}, d, errors.New("source image exceeds byte limit")
	}
	body, err := base64.StdEncoding.DecodeString(source.ContentBase64)
	if err != nil {
		return mediaItem{}, d, fmt.Errorf("decode storage content: %w", err)
	}
	sourceSHA := imageSHA(body)
	policyJSON, _ := json.Marshal(p)
	key := imageSHA(append([]byte(sourceSHA), policyJSON...))
	unlock := lockSocialResource(ctx, item.ID, "image-delivery:"+p.ID+":"+projectID)
	defer unlock()
	var cachedID int64
	var raw string
	if ctx.AppDB().QueryRow(`SELECT delivery_file_id,diagnostics FROM image_delivery_cache WHERE cache_key=? AND media_project_id=?`, key, projectID).Scan(&cachedID, &raw) == nil && json.Unmarshal([]byte(raw), &d) == nil {
		// Re-check source permission above and derivative existence in its scope.
		var meta struct {
			ID   int64 `json:"id"`
			File struct {
				ID int64 `json:"id"`
			} `json:"file"`
		}
		if ctx.PlatformAPI().CallAppResult("storage", "files_get", map[string]any{"id": cachedID, "_project_id": projectID}, &meta) == nil && (meta.ID == cachedID || meta.File.ID == cachedID) {
			d.SourceID = item.ID
			d.CacheHit = true
			return mediaItem{ID: cachedID, Mime: d.DeliveryMIME, Name: key + ".jpg", Bytes: int64(d.Bytes)}, d, nil
		}
	}
	out, d, err := normalizeDeliveryImage(body, p)
	d.SourceID = item.ID
	if err != nil {
		return mediaItem{}, d, err
	}
	upload := map[string]any{"name": key + ".jpg", "folder": "/.social/image-delivery/" + p.ID + "/", "content_type": "image/jpeg", "content_base64": base64.StdEncoding.EncodeToString(out), "source": "social-image-derivative", "tags": []string{"internal", "derived", p.ID}, "visibility": "private", "_project_id": projectID}
	var stored struct {
		ID int64 `json:"id"`
	}
	if err = ctx.PlatformAPI().CallAppResult("storage", "files_upload", upload, &stored); err != nil {
		return mediaItem{}, d, fmt.Errorf("store image derivative: %w", err)
	}
	if stored.ID <= 0 {
		return mediaItem{}, d, errors.New("storage returned no derivative id")
	}
	d.DeliveryID = stored.ID
	rawBytes, _ := json.Marshal(d)
	_, err = ctx.AppDB().Exec(`INSERT INTO image_delivery_cache(cache_key,media_project_id,source_sha256,policy,delivery_file_id,diagnostics) VALUES(?,?,?,?,?,?) ON CONFLICT(cache_key,media_project_id) DO UPDATE SET delivery_file_id=excluded.delivery_file_id,diagnostics=excluded.diagnostics`, key, projectID, sourceSHA, p.ID, stored.ID, string(rawBytes))
	if err != nil {
		return mediaItem{}, d, err
	}
	return mediaItem{ID: stored.ID, Mime: "image/jpeg", Name: key + ".jpg", Bytes: int64(len(out))}, d, nil
}

func preflightImageDeliveryURL(raw string, p imageDeliveryPolicy, expectedSHA string) (imageDeliveryDiagnostics, error) {
	d := imageDeliveryDiagnostics{Policy: p.ID, Validation: "failed"}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return d, errors.New("invalid image delivery URL")
	}
	// Persist no signatures or query parameters in diagnostics.
	d.DeliveryPath = u.Scheme + "://" + u.Host + u.EscapedPath()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return d, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Apteva-MediaDelivery/1.0)")
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return d, errors.New("image delivery GET failed")
	}
	defer resp.Body.Close()
	d.HTTPStatus = resp.StatusCode
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return d, fmt.Errorf("image delivery URL returned redirect status %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return d, fmt.Errorf("image delivery GET returned status %d; redirect-free 200 required", resp.StatusCode)
	}
	if resp.ContentLength > int64(p.MaxBytes) {
		return d, errors.New("image delivery Content-Length exceeds byte limit")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(p.MaxBytes)+1))
	if err != nil {
		return d, fmt.Errorf("read image delivery: %w", err)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != int64(len(body)) {
		return d, errors.New("image delivery Content-Length does not match bytes")
	}
	w, h, err := validateDeliveryImage(body, resp.Header.Get("Content-Type"), p)
	if err != nil {
		return d, err
	}
	d.DeliverySHA = imageSHA(body)
	if expectedSHA != "" && d.DeliverySHA != expectedSHA {
		return d, errors.New("image delivery checksum does not match prepared copy")
	}
	d.Width = w
	d.Height = h
	d.Bytes = len(body)
	d.DeliveryMIME = "image/jpeg"
	d.Validation = "verified"
	return d, nil
}

func (a *App) resolveImageDeliveryURL(ctx *sdk.AppCtx, item mediaItem, projectID string, p imageDeliveryPolicy, expectedSHA string) (string, imageDeliveryDiagnostics, error) {
	args := map[string]any{"id": item.ID, "ttl_seconds": p.URLTTLSeconds, "delivery": "proxy", "disposition": "inline"}
	if projectID != "" {
		args["_project_id"] = projectID
	}
	var signed struct {
		URL string `json:"url"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_url", args, &signed); err != nil {
		return "", imageDeliveryDiagnostics{}, err
	}
	fullURL := absoluteStorageURL(signed.URL)
	verified, err := preflightImageDeliveryURL(fullURL, p, expectedSHA)
	return fullURL, verified, err
}

func persistImageDeliveryDiagnostics(ctx *sdk.AppCtx, targetID int64, diagnostics []imageDeliveryDiagnostics) error {
	if targetID <= 0 {
		return nil
	}
	raw, err := json.Marshal(diagnostics)
	if err != nil {
		return err
	}
	_, err = ctx.AppDB().Exec(`UPDATE post_targets SET media_delivery_diagnostics=? WHERE id=? AND EXISTS(SELECT 1 FROM posts p WHERE p.id=post_targets.post_id AND p.project_id=?)`, string(raw), targetID, projectScope(ctx))
	return err
}
