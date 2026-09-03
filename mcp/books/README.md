# Books

Books is Apteva's structured manuscript and publication-preparation app. It keeps books, chapters, sections, notes, revisions, publication metadata, covers, and interior images in one project-scoped workspace.

## Publication workflow

1. Create a book and write the manuscript in Markdown.
2. Complete the Publish panel: author, description, categories, keywords, rights, territories, pricing, accessibility, identifiers, and print settings.
3. Attach a JPEG or PNG ebook cover. Attach a full-wrap print-cover PDF for print packages.
4. Insert interior images from the Writing panel. Books stores the image and inserts an `asset:<id>` Markdown reference into the selected manuscript node.
5. Run the platform readiness check.
6. Export EPUB, print PDF, or a platform package ZIP and inspect it in the target store's previewer.

The app prepares files for upload. Publishing remains a deliberate human action in the Kindle, Apple Books, Kobo, Google Play Books, or print portal.

## Export formats

- `markdown`: editable source manuscript; `include_notes` appends research notes.
- `epub`: reflowable EPUB 3 with embedded cover and images, navigation document, landmarks, reading direction, publication metadata, and accessibility metadata.
- `pdf`: typeset print interior with configurable trim, mirrored margins, optional bleed page geometry, embedded Unicode fonts, title/copyright pages, table of contents, and page numbers.
- `package`: platform-specific ZIP containing the applicable publication files, `metadata.json`, validation report, checklist, and upload instructions.

Use `platform` with package or readiness exports: `kindle`, `apple_books`, `kobo`, `google_play`, `print`, or `generic`.

## EPUB validation

Every EPUB runs through the built-in structural preflight, which is sufficient for the app's readiness checklist. When an `epubcheck` executable is on `PATH`, Books additionally runs the official W3C EPUBCheck automatically. A Java distribution can instead be configured with `EPUBCHECK_JAR=/path/to/epubcheck.jar`.

The package always includes `EPUB-VALIDATION.md`. If the official checker is unavailable, the report identifies the built-in preflight accurately and recommends independent EPUBCheck validation before submission.

## Print scope

The PDF renderer is intended for reflowable, text-led books with ordinary inline JPEG/PNG images. It embeds its fonts and produces exact trim geometry. Complex tables, mathematical typesetting, custom fonts, color-managed art books, and edge-to-edge image placement still need specialist prepress review. Always order a physical proof.
