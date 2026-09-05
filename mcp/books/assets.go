package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"path/filepath"
	"strings"
)

const maxAssetBytes = 25 << 20

type BookAsset struct {
	ID          int64  `json:"id"`
	BookID      int64  `json:"book_id"`
	NodeID      *int64 `json:"node_id,omitempty"`
	Kind        string `json:"kind"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	AltText     string `json:"alt_text,omitempty"`
	Caption     string `json:"caption,omitempty"`
	SHA256      string `json:"sha256"`
	SizeBytes   int    `json:"size_bytes"`
	WidthPX     int    `json:"width_px,omitempty"`
	HeightPX    int    `json:"height_px,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	Content     []byte `json:"-"`
}

func createAsset(db *sql.DB, asset *BookAsset) (*BookAsset, error) {
	if asset.BookID <= 0 {
		return nil, errors.New("book_id required")
	}
	if strings.TrimSpace(asset.Filename) == "" || len(asset.Content) == 0 {
		return nil, errors.New("filename and content required")
	}
	if len(asset.Content) > maxAssetBytes {
		return nil, fmt.Errorf("asset exceeds %d MB limit", maxAssetBytes>>20)
	}
	asset.Kind = strings.TrimSpace(asset.Kind)
	if asset.Kind == "" {
		asset.Kind = "interior_image"
	}
	if asset.Kind != "cover" && asset.Kind != "print_cover" && asset.Kind != "interior_image" {
		return nil, errors.New("kind must be cover, print_cover, or interior_image")
	}
	asset.ContentType = normalizedAssetContentType(asset.Filename, asset.ContentType)
	if asset.Kind == "print_cover" {
		if asset.ContentType != "application/pdf" {
			return nil, errors.New("print_cover must be a PDF")
		}
		if !strings.HasPrefix(string(asset.Content), "%PDF-") {
			return nil, errors.New("print_cover content is not a valid PDF file")
		}
	} else if asset.ContentType != "image/jpeg" && asset.ContentType != "image/png" {
		return nil, errors.New("ebook covers and interior images must be JPEG or PNG")
	} else {
		config, format, err := image.DecodeConfig(bytes.NewReader(asset.Content))
		if err != nil {
			return nil, errors.New("image content is not a readable JPEG or PNG")
		}
		detected := "image/" + format
		if detected == "image/jpg" {
			detected = "image/jpeg"
		}
		if detected != asset.ContentType {
			return nil, fmt.Errorf("content type %s does not match detected %s", asset.ContentType, detected)
		}
		asset.WidthPX, asset.HeightPX = config.Width, config.Height
	}
	hash := sha256.Sum256(asset.Content)
	asset.SHA256 = hex.EncodeToString(hash[:])
	asset.SizeBytes = len(asset.Content)

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if asset.Kind == "cover" || asset.Kind == "print_cover" {
		if _, err := tx.Exec(`UPDATE book_assets SET deleted_at = ?, updated_at = ? WHERE book_id = ? AND kind = ? AND deleted_at IS NULL`, now(), now(), asset.BookID, asset.Kind); err != nil {
			return nil, err
		}
	}
	var node any
	if asset.NodeID != nil && *asset.NodeID > 0 {
		node = *asset.NodeID
	}
	res, err := tx.Exec(`
		INSERT INTO book_assets (book_id, node_id, kind, filename, content_type, content_blob, alt_text, caption, sha256, size_bytes, width_px, height_px)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		asset.BookID, node, asset.Kind, filepath.Base(asset.Filename), asset.ContentType, asset.Content,
		asset.AltText, asset.Caption, asset.SHA256, asset.SizeBytes, asset.WidthPX, asset.HeightPX)
	if err != nil {
		return nil, err
	}
	asset.ID, _ = res.LastInsertId()
	if _, err := tx.Exec(`UPDATE books SET updated_at = ? WHERE id = ?`, now(), asset.BookID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return getAsset(db, asset.ID, false)
}

func listAssets(db *sql.DB, bookID int64) ([]BookAsset, error) {
	rows, err := db.Query(`
		SELECT id, book_id, node_id, kind, filename, content_type, alt_text, caption, sha256, size_bytes, width_px, height_px, created_at, updated_at
		FROM book_assets WHERE book_id = ? AND deleted_at IS NULL
		ORDER BY CASE kind WHEN 'cover' THEN 0 WHEN 'print_cover' THEN 1 ELSE 2 END, id`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BookAsset{}
	for rows.Next() {
		asset, err := scanAsset(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, *asset)
	}
	return out, rows.Err()
}

func getAsset(db *sql.DB, id int64, withContent bool) (*BookAsset, error) {
	columns := "id, book_id, node_id, kind, filename, content_type, alt_text, caption, sha256, size_bytes, width_px, height_px, created_at, updated_at"
	if withContent {
		columns += ", content_blob"
	}
	asset, err := scanAsset(db.QueryRow(`SELECT `+columns+` FROM book_assets WHERE id = ? AND deleted_at IS NULL`, id), withContent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return asset, err
}

func listAssetsWithContent(db *sql.DB, bookID int64) ([]BookAsset, error) {
	rows, err := db.Query(`
		SELECT id, book_id, node_id, kind, filename, content_type, alt_text, caption, sha256, size_bytes, width_px, height_px, created_at, updated_at, content_blob
		FROM book_assets WHERE book_id = ? AND deleted_at IS NULL ORDER BY id`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BookAsset{}
	for rows.Next() {
		asset, err := scanAsset(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, *asset)
	}
	return out, rows.Err()
}

func updateAsset(db *sql.DB, id int64, fields map[string]any) error {
	sets := []string{}
	args := []any{}
	for _, field := range []string{"alt_text", "caption"} {
		if value, ok := fields[field].(string); ok {
			sets = append(sets, field+" = ?")
			args = append(args, value)
		}
	}
	if len(sets) == 0 {
		return errors.New("no updatable fields provided")
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, now(), id)
	res, err := db.Exec(`UPDATE book_assets SET `+strings.Join(sets, ", ")+` WHERE id = ? AND deleted_at IS NULL`, args...)
	if err != nil {
		return err
	}
	if count, _ := res.RowsAffected(); count == 0 {
		return errNotFound
	}
	return nil
}

func deleteAsset(db *sql.DB, id int64) error {
	res, err := db.Exec(`UPDATE book_assets SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, now(), now(), id)
	if err != nil {
		return err
	}
	if count, _ := res.RowsAffected(); count == 0 {
		return errNotFound
	}
	return nil
}

func scanAsset(scanner rowScanner, withContent bool) (*BookAsset, error) {
	var asset BookAsset
	var node sql.NullInt64
	args := []any{&asset.ID, &asset.BookID, &node, &asset.Kind, &asset.Filename, &asset.ContentType, &asset.AltText, &asset.Caption, &asset.SHA256, &asset.SizeBytes, &asset.WidthPX, &asset.HeightPX, &asset.CreatedAt, &asset.UpdatedAt}
	if withContent {
		args = append(args, &asset.Content)
	}
	if err := scanner.Scan(args...); err != nil {
		return nil, err
	}
	if node.Valid {
		asset.NodeID = &node.Int64
	}
	return &asset, nil
}

func firstAssetOfKind(assets []BookAsset, kind string) *BookAsset {
	for i := range assets {
		if assets[i].Kind == kind {
			return &assets[i]
		}
	}
	return nil
}

func normalizedAssetContentType(filename, contentType string) string {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if contentType != "" && contentType != "application/octet-stream" {
		return contentType
	}
	if guessed := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))); guessed != "" {
		return strings.Split(guessed, ";")[0]
	}
	return "application/octet-stream"
}

func assetExtension(asset BookAsset) string {
	switch asset.ContentType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "application/pdf":
		return ".pdf"
	default:
		ext := strings.ToLower(filepath.Ext(asset.Filename))
		if ext != "" {
			return ext
		}
		return ".bin"
	}
}
