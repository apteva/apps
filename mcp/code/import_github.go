package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// importGitHub is the worker behind the repos_import_github MCP tool.
// It resolves the bound github connection, calls the catalog's
// get_archive tool to fetch a gzipped tarball, unpacks it via the
// FileStore, then registers the new repo in the metadata DB.
//
// The integration runner returns binary responses in the
// {_binary, base64, mimeType, size} envelope (server-side change in
// apteva-server c4b4928 + http-executor.ts shape) — this function
// decodes that envelope, gunzips, untars, strips the leading
// "<repo>-<sha>/" directory GitHub adds, and rejects path traversal.
//
// Slug collision returns an error mirroring repos_create's rejection;
// the panel surfaces this so the user can rename and retry.
func importGitHub(ctx *sdk.AppCtx, store FileStore, in importGitHubInput) (*importGitHubResult, error) {
	if in.Owner == "" || in.Repo == "" {
		return nil, errors.New("owner and repo are required")
	}
	bound := ctx.IntegrationFor("github")
	if bound == nil || bound.ConnectionID == 0 {
		return nil, errors.New("github not connected: bind a github connection on this install first")
	}

	ref := in.Ref
	if ref == "" {
		ref = "HEAD"
	}

	// Translate the role's logical capability → catalog tool name.
	// Falls back to the literal string when the manifest's tools map
	// is missing — the install picker enforces compatibility, so this
	// only fails on misconfigured catalogs.
	toolName := bound.ToolFor("get_archive")
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, toolName, map[string]any{
		"owner": in.Owner,
		"repo":  in.Repo,
		"ref":   ref,
	})
	if err != nil {
		return nil, fmt.Errorf("call %s.get_archive: %w", bound.AppSlug, err)
	}
	if res == nil || !res.Success {
		status := 0
		if res != nil {
			status = res.Status
		}
		// Surface a useful message — the integration runner returns a
		// map with an `error` field on caps / 4xx / 5xx, plus the raw
		// status code which is the most informative thing here.
		msg := upstreamErrorString(res)
		return nil, fmt.Errorf("github get_archive failed (status=%d): %s", status, msg)
	}
	tarBytes, err := decodeBinaryEnvelope(res.Data)
	if err != nil {
		return nil, fmt.Errorf("decode archive: %w (status=%d)", err, res.Status)
	}

	// Unpack into the FileStore.
	slug := in.Slug
	if slug == "" {
		slug = in.Repo
	}
	slug = slugify(slug)
	if slug == "" {
		return nil, errors.New("could not derive a slug; pass one explicitly")
	}
	pid, err := resolveProjectFromArgs(map[string]any{"_project_id": in.ProjectID})
	if err != nil {
		return nil, err
	}
	// Materialise files first into a staging map so we can hand the
	// fully-formed set to detectFramework() before we touch the store.
	// Streaming straight into the store is fine for production but
	// makes "no files imported" cleanup awkward.
	files, err := readGitHubTarball(tarBytes)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("github archive contained no files")
	}

	framework := strings.TrimSpace(in.Framework)
	if framework == "" {
		framework = detectImportFramework(files)
	}
	if framework == "" {
		framework = "blank"
	}

	if err := store.CreateRepo(slug); err != nil {
		return nil, fmt.Errorf("create repo storage: %w", err)
	}
	for path, body := range files {
		if _, err := store.Write(slug, path, body); err != nil {
			return nil, fmt.Errorf("write %s: %w", path, err)
		}
	}

	repo, err := dbCreateRepo(ctx.AppDB(), pid, CreateRepoInput{
		Name:        in.Owner + "/" + in.Repo,
		Slug:        slug,
		Description: fmt.Sprintf("Imported from github.com/%s/%s @ %s", in.Owner, in.Repo, ref),
		Framework:   framework,
	})
	if err != nil {
		return nil, fmt.Errorf("create repo row: %w", err)
	}

	bytesWritten := 0
	for _, b := range files {
		bytesWritten += len(b)
	}

	if ctx != nil {
		ctx.Emit("repo.added", map[string]any{
			"id": repo.ID, "slug": repo.Slug, "name": repo.Name,
			"framework": repo.Framework, "imported_from": "github",
		})
	}

	return &importGitHubResult{
		Repository:   repo,
		FileCount:    len(files),
		BytesWritten: bytesWritten,
		SourceURL:    fmt.Sprintf("https://github.com/%s/%s", in.Owner, in.Repo),
		Ref:          ref,
	}, nil
}

type importGitHubInput struct {
	Owner     string
	Repo      string
	Ref       string
	Slug      string
	Framework string
	ProjectID string
}

type importGitHubResult struct {
	Repository   *Repo
	FileCount    int
	BytesWritten int
	SourceURL    string
	Ref          string
}

// decodeBinaryEnvelope unwraps the {_binary, base64, mimeType, size}
// shape the integration runner produces for binary responses.
// ExecuteResult.Data arrives as json.RawMessage; we unmarshal directly
// into the envelope struct.
func decodeBinaryEnvelope(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty response")
	}
	var env struct {
		Binary bool   `json:"_binary"`
		Base64 string `json:"base64"`
		Mime   string `json:"mimeType"`
		Size   int    `json:"size"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
	}
	if !env.Binary || env.Base64 == "" {
		// Reaching here on a 2xx means the runner didn't classify the
		// body as binary. Most likely cause: a catalog drift where the
		// upstream's Content-Type isn't in the binary prefix list.
		return nil, errors.New("response was not a binary envelope; check Content-Type from upstream")
	}
	return base64.StdEncoding.DecodeString(env.Base64)
}

// readGitHubTarball walks a gzipped tar produced by GitHub's
// /tarball endpoint and returns a path → bytes map. Strips the
// leading "<repo>-<sha>/" directory GitHub prepends. Rejects path
// traversal entries; rejects symlinks (Code's FileStore is flat-file,
// not a real filesystem). Skips directory entries — folders exist
// implicitly when a file lives under them.
func readGitHubTarball(body []byte) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytesReader(body))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar entry: %w", err)
		}
		// Skip non-regular entries. Directories are implicit; symlinks
		// would dangle in the FileStore.
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		// Strip the leading "<repo>-<sha>/" GitHub adds, and reject
		// anything that escapes the prefix.
		clean := stripLeadingDir(hdr.Name)
		if clean == "" {
			continue
		}
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") || strings.Contains(clean, "/../") {
			return nil, fmt.Errorf("tar entry escapes root: %q", hdr.Name)
		}
		buf := make([]byte, hdr.Size)
		if _, err := io.ReadFull(tr, buf); err != nil {
			return nil, fmt.Errorf("read %s: %w", clean, err)
		}
		out[clean] = buf
	}
	return out, nil
}

// stripLeadingDir drops the first path component, which for a GitHub
// tarball is always "<repo>-<sha>" — uninteresting noise. Returns ""
// for the directory entry itself.
func stripLeadingDir(name string) string {
	name = strings.TrimPrefix(name, "./")
	if !strings.Contains(name, "/") {
		return ""
	}
	return name[strings.Index(name, "/")+1:]
}

// detectImportFramework picks a Code framework value from the imported
// tree. Mirrors the deploy app's detectFramework but operates on a
// staging map so we don't have to materialise the tree to disk first.
// Conservative — only well-known signatures map to known frameworks;
// everything else returns "" and the caller picks "blank".
func detectImportFramework(files map[string][]byte) string {
	if _, ok := files["go.mod"]; ok {
		return "go"
	}
	if pkg, ok := files["package.json"]; ok {
		// Next.js detection: dependencies / devDependencies contains "next".
		// Cheap substring check — false positives are fine since "nextjs" is
		// a strict superset of node-friendly behaviour and this only sets
		// the panel hint, not what's executed.
		if isNextJS(pkg) {
			return "nextjs"
		}
		// Generic node template falls through to blank for now — Code's
		// framework set is small (blank|nextjs|static|go|python). When
		// the catalog grows a "node" framework, switch this fallback.
		return "blank"
	}
	if _, ok := files["requirements.txt"]; ok {
		return "python"
	}
	if _, ok := files["pyproject.toml"]; ok {
		return "python"
	}
	if _, ok := files["index.html"]; ok {
		return "static"
	}
	return ""
}

func isNextJS(packageJSON []byte) bool {
	// Cheapest reliable signal without parsing JSON — `"next":` followed
	// by a version string almost always identifies the framework. The
	// `\"next\":` form is what npm/yarn/pnpm/bun all serialize.
	return strings.Contains(string(packageJSON), `"next":`)
}

// upstreamErrorString turns an ExecuteResult from the integration
// runner into a one-line user-friendly message. Data is a
// json.RawMessage that's either a JSON object with an "error" /
// "message" field (the runner's own error shape), or the upstream's
// JSON-encoded response body. We try the structured form first, then
// fall back to a truncated raw view.
func upstreamErrorString(res *sdk.ExecuteResult) string {
	if res == nil || len(res.Data) == 0 {
		return "no body"
	}
	var m map[string]any
	if err := json.Unmarshal(res.Data, &m); err == nil && m != nil {
		if e, ok := m["error"].(string); ok && e != "" {
			return e
		}
		if msg, ok := m["message"].(string); ok && msg != "" {
			return msg
		}
	}
	// Last resort — the raw bytes. Cap to 200 chars so a giant 404
	// HTML page from GitHub doesn't drown the panel banner.
	s := string(res.Data)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// bytesReader is a small wrapper letting us avoid pulling bytes
// into the import list of every file that touches a tarball — keeps
// the file's imports tight to the helpers it actually uses.
func bytesReader(b []byte) io.Reader { return &byteReader{buf: b} }

type byteReader struct {
	buf []byte
	pos int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.buf) {
		return 0, io.EOF
	}
	n := copy(p, r.buf[r.pos:])
	r.pos += n
	return n, nil
}
