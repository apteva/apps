package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
	"github.com/phpdave11/gofpdf"
)

func pdfPageCount(body []byte) (int, error) {
	return api.PageCount(bytes.NewReader(body), model.NewDefaultConfiguration())
}

func renderCompletedPDF(source []byte, env *Envelope, recipients []Recipient, values []completionValue) ([]byte, error) {
	dims, err := api.PageDims(bytes.NewReader(source), model.NewDefaultConfiguration())
	if err != nil {
		return nil, err
	}
	watermarks := map[int][]*model.Watermark{}
	for _, value := range values {
		if value.Page < 1 || value.Page > len(dims) || value.ValueText == "" {
			continue
		}
		dim := dims[value.Page-1]
		x := value.X * dim.Width
		y := (1 - value.Y - value.Height) * dim.Height
		if img, ok := drawnSignatureImage(value.ValueText); ok {
			wm, err := imageFieldWatermark(img, value.Width*dim.Width, value.Height*dim.Height, x, y)
			if err != nil {
				return nil, fmt.Errorf("prepare drawn field %d: %w", value.ID, err)
			}
			watermarks[value.Page] = append(watermarks[value.Page], wm)
			continue
		}
		fontSize := value.Height * dim.Height * 0.55
		if fontSize < 8 {
			fontSize = 8
		}
		if fontSize > 28 {
			fontSize = 28
		}
		text := value.ValueText
		font := "Helvetica"
		if value.FieldType == "signature" {
			text = "Signed: " + text
			font = "Helvetica-Oblique"
		} else if value.FieldType == "checkbox" {
			if value.ValueText == "true" {
				text = "X"
			} else {
				continue
			}
		}
		desc := fmt.Sprintf("font:%s, points:%d, scale:1 abs, pos:bl, off:%.2f %.2f, fillcol:#111827, rot:0", font, int(fontSize+.5), x, y)
		wm, err := api.TextWatermark(text, desc, true, false, types.POINTS)
		if err != nil {
			return nil, fmt.Errorf("prepare field %d: %w", value.ID, err)
		}
		watermarks[value.Page] = append(watermarks[value.Page], wm)
	}

	stamped := source
	if len(watermarks) > 0 {
		var buf bytes.Buffer
		if err := api.AddWatermarksSliceMap(bytes.NewReader(source), &buf, watermarks, model.NewDefaultConfiguration()); err != nil {
			return nil, fmt.Errorf("apply signature fields: %w", err)
		}
		stamped = buf.Bytes()
	}

	certificate, err := completionCertificate(env, recipients)
	if err != nil {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp("", "apteva-signatures-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	stampedPath := filepath.Join(tmpDir, "signed.pdf")
	certificatePath := filepath.Join(tmpDir, "certificate.pdf")
	outputPath := filepath.Join(tmpDir, "complete.pdf")
	if err := os.WriteFile(stampedPath, stamped, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(certificatePath, certificate, 0o600); err != nil {
		return nil, err
	}
	if err := api.MergeCreateFile([]string{stampedPath, certificatePath}, outputPath, false, model.NewDefaultConfiguration()); err != nil {
		return nil, fmt.Errorf("append completion certificate: %w", err)
	}
	out, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, err
	}
	return out, nil
}

const drawnSignaturePrefix = "data:image/png;base64,"

// maxDrawnSignatureBytes bounds the decoded PNG a signer may submit.
const maxDrawnSignatureBytes = 512 * 1024

// drawnSignatureImage decodes a signing-pad value. Only PNG data URLs
// produced by the signing page's draw pad qualify; anything else is
// treated as typed text.
func drawnSignatureImage(value string) ([]byte, bool) {
	if !strings.HasPrefix(value, drawnSignaturePrefix) {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(value[len(drawnSignaturePrefix):])
	if err != nil {
		return nil, false
	}
	return raw, true
}

// validateDrawnSignature vets a submitted draw-pad value: bounded size
// and an actually decodable PNG header.
func validateDrawnSignature(value string) error {
	raw, ok := drawnSignatureImage(value)
	if !ok {
		return errors.New("drawn signature must be a PNG data URL")
	}
	if len(raw) > maxDrawnSignatureBytes {
		return fmt.Errorf("drawn signature exceeds %d bytes", maxDrawnSignatureBytes)
	}
	if _, err := png.DecodeConfig(bytes.NewReader(raw)); err != nil {
		return fmt.Errorf("drawn signature is not a valid PNG: %w", err)
	}
	return nil
}

// imageFieldWatermark stamps a drawn signature PNG into its field box:
// scaled to fit (abs scale = points per image pixel), anchored at the
// box's bottom-left like the text stamps.
func imageFieldWatermark(img []byte, boxW, boxH, x, y float64) (*model.Watermark, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(img))
	if err != nil {
		return nil, err
	}
	if cfg.Width < 1 || cfg.Height < 1 {
		return nil, errors.New("empty signature image")
	}
	scale := boxW / float64(cfg.Width)
	if h := boxH / float64(cfg.Height); h < scale {
		scale = h
	}
	desc := fmt.Sprintf("pos:bl, off:%.2f %.2f, scale:%.5f abs, rot:0", x, y, scale)
	return api.ImageWatermarkForReader(bytes.NewReader(img), desc, true, false, types.POINTS)
}

func completionCertificate(env *Envelope, recipients []Recipient) ([]byte, error) {
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetTitle("Completion certificate - "+env.Title, true)
	pdf.SetAuthor("Apteva Signatures", true)
	pdf.AddPage()
	pdf.SetMargins(18, 18, 18)
	pdf.SetFont("Helvetica", "B", 20)
	pdf.CellFormat(0, 12, "Completion certificate", "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 11)
	pdf.SetTextColor(55, 65, 81)
	pdf.MultiCell(0, 7, "This page records the simple electronic-signature workflow completed through Apteva Signatures.", "", "L", false)
	pdf.Ln(5)
	pdf.SetTextColor(17, 24, 39)
	certificateRow(pdf, "Envelope", env.PublicID)
	certificateRow(pdf, "Document", env.Title)
	certificateRow(pdf, "Original file", env.SourceName)
	certificateRow(pdf, "Original SHA-256", env.SourceSHA256)
	certificateRow(pdf, "Sent", env.SentAt)
	certificateRow(pdf, "Completed", time.Now().UTC().Format(time.RFC3339))
	pdf.Ln(5)
	pdf.SetFont("Helvetica", "B", 13)
	pdf.CellFormat(0, 9, "Recipients", "", 1, "L", false, 0, "")
	for _, recipient := range recipients {
		pdf.SetFont("Helvetica", "B", 10)
		pdf.CellFormat(55, 7, recipient.Name, "", 0, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", 10)
		line := recipient.Role + " - " + recipient.Status
		if recipient.CompletedAt != "" {
			line += " at " + recipient.CompletedAt
		}
		pdf.MultiCell(0, 7, line, "", "L", false)
	}
	pdf.Ln(7)
	pdf.SetFont("Helvetica", "I", 9)
	pdf.SetTextColor(75, 85, 99)
	pdf.MultiCell(0, 6, "This certificate documents a simple electronic-signature process. It is not a qualified electronic signature, notarization, or legal opinion.", "", "L", false)
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func certificateRow(pdf *gofpdf.Fpdf, label, value string) {
	pdf.SetFont("Helvetica", "B", 10)
	pdf.CellFormat(38, 7, label, "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.MultiCell(0, 7, value, "", "L", false)
}

func finalizeEnvelope(ctx *sdk.AppCtx, env *Envelope) (*Envelope, error) {
	if ctx == nil || env == nil {
		return nil, fmt.Errorf("finalize: missing context or envelope")
	}
	file, source, _, err := sourcePDF(ctx.WithProject(env.ProjectID), env.SourceFileID)
	if err != nil {
		return nil, err
	}
	if got := bytesHash(source); got != env.SourceSHA256 {
		_ = addAudit(ctx.AppDB(), env.ID, env.ProjectID, 0, "finalization.failed", map[string]any{"reason": "source_hash_mismatch"})
		return nil, errors.New("source PDF changed after the envelope was created")
	}
	recipients, err := listRecipients(ctx.AppDB(), env.ProjectID, env.ID)
	if err != nil {
		return nil, err
	}
	for _, recipient := range recipients {
		if recipient.Status != "signed" && recipient.Status != "approved" {
			return nil, errors.New("not every recipient has completed")
		}
	}
	values, err := loadCompletionValues(ctx.AppDB(), env.ProjectID, env.ID)
	if err != nil {
		return nil, err
	}
	completedPDF, err := renderCompletedPDF(source, env, recipients, values)
	if err != nil {
		_ = addAudit(ctx.AppDB(), env.ID, env.ProjectID, 0, "finalization.failed", map[string]any{"error": err.Error()})
		return nil, err
	}
	completedHash := bytesHash(completedPDF)
	audit, err := listAudit(ctx.AppDB(), env.ProjectID, env.ID)
	if err != nil {
		return nil, err
	}
	evidenceEnvelope := *env
	evidenceEnvelope.Status = "completed"
	evidenceEnvelope.CompletedAt = nowUTC()
	evidenceEnvelope.CompletedSHA256 = completedHash
	// Drawn signatures are large PNG data URLs; the evidence JSON
	// records their hash instead of megabytes of base64. The image
	// itself is preserved inside the completed PDF.
	auditValues := make([]completionValue, len(values))
	copy(auditValues, values)
	for i := range auditValues {
		if raw, ok := drawnSignatureImage(auditValues[i].ValueText); ok {
			auditValues[i].ValueText = "drawn-signature-png sha256:" + bytesHash(raw)
		}
	}
	auditBody, err := json.MarshalIndent(map[string]any{
		"schema":                   "apteva-signatures-evidence/v1",
		"envelope":                 &evidenceEnvelope,
		"recipients":               recipients,
		"field_values":             auditValues,
		"events":                   audit,
		"completed_sha256":         completedHash,
		"generated_at":             nowUTC(),
		"signature_assurance":      "simple_electronic_signature",
		"independent_verification": false,
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	folder := outputFolder(ctx) + env.PublicID + "/"
	baseName := safeFilename(strings.TrimSuffix(file.Name, filepath.Ext(file.Name)))
	if baseName == "" {
		baseName = "document"
	}
	completed, err := storageUpload(ctx.WithProject(env.ProjectID), baseName+"-signed.pdf", folder, "application/pdf", completedPDF)
	if err != nil {
		return nil, err
	}
	auditUpload, err := storageUpload(ctx.WithProject(env.ProjectID), baseName+"-audit.json", folder, "application/json", auditBody)
	if err != nil {
		return nil, err
	}
	completed.SHA256 = completedHash
	return markEnvelopeCompleted(ctx.AppDB(), env.ProjectID, env.ID, completed, auditUpload)
}

func safeFilename(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
