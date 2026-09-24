# Content Catalog

Content Catalog coordinates production work across Apteva apps. Its SQLite
database stores stable Catalog IDs and relationships; it does not copy or
modify Storage files, Media records, Gigs, Social posts, or Patreon posts.

## Current slice

- Brands own an explicit Storage root and optional cloud host configuration.
- Sessions have stable IDs, an explicit brand, and optional links to existing
  Gigs. A Gig is checked through Gigs before the link is saved.
- A session can attach existing Storage file IDs. Catalog checks each file
  through Storage and stores the file ID, checksum, and a metadata snapshot.
  A read-only import preview lists up to 200 files under the brand root for
  human review; it never guesses a session from a path.
- Assets support many source relationships, editorial review, live Media
  read-through, and release-use lookup. `media.completed` events update only
  already linked assets' cached completion/rating state.
- Session cards show a linked image or an available Media thumbnail. Asset
  cards show image, thumbnail, or waveform previews. The detail viewer reads
  the original from Storage for image, video, audio, and PDF playback; other
  files retain an open-original link. Legacy import candidates can be opened
  and previewed before linking them. Preview endpoints only redirect to
  Storage bytes and never create derivatives.
- Releases contain one or more destinations and ordered assets. Publication
  observations carry explicit evidence and actual time for published states.
  A planned release or a hosted video never becomes a verified post by itself.
- `HostProvider` isolates cloud-host operations from Catalog records. Bunny
  Stream is the first adapter. Upload starts only from an explicit hosting
  request for an approved video. A reservation and checksum/destination
  lookup prevent normal retries or duplicate assets from starting another
  transfer. Ambiguous starts remain `uncertain` for operator review.

## Boundaries and next slices

Direct file upload routing, Media derivative discovery for unattached files,
automatic size-threshold hosting, Social result ingestion, Patreon page
verification, and deeper historical import are future slices. No deployment
or migration of existing app records is part of this directory.

Run focused checks from this directory with `GOWORK=off go test ./...` and
`GOWORK=off go build .`. Build the panel from the `apps` repo with
`bun run scripts/build-panels.ts --app content-catalog`.
