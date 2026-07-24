package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	zrokNamespace   = "public"
	zrokAPIEndpoint = "https://api-v2.zrok.io"
)

type ZrokState struct {
	ConnectionID int64  `json:"-"`
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	PublicURL    string `json:"public_url"`
	CreatedAt    string `json:"created_at,omitempty"`
}

type zrokProvider struct{ app *App }

func (p *zrokProvider) Name() string       { return providerNameZrok }
func (p *zrokProvider) Snapshot() Snapshot { return p.app.mgr.Snapshot() }
func (p *zrokProvider) Stop() error        { return p.app.mgr.Stop() }

func (p *zrokProvider) Configured(ctx *sdk.AppCtx) bool {
	if ctx == nil || ctx.AppDB() == nil {
		return false
	}
	state, _ := dbZrokState(ctx.AppDB())
	return state != nil
}

func (p *zrokProvider) Start(ctx *sdk.AppCtx) (string, error) {
	target := p.app.resolveTargetURL(ctx)
	if err := validateTargetURL(target); err != nil {
		return "", err
	}
	state, err := dbZrokState(ctx.AppDB())
	if err != nil {
		return "", fmt.Errorf("read zrok configuration: %w", err)
	}
	if state == nil {
		return "", errors.New("zrok is not configured — reserve a stable name in the Live Link panel first")
	}
	connectionID, token, err := zrokCredentials(ctx)
	if err != nil {
		return "", err
	}
	if connectionID != state.ConnectionID {
		return "", errors.New("the bound zrok connection changed — restore the original connection or delete the existing zrok name before reconfiguring")
	}
	publicURL, err := zrokResolveName(context.Background(), token, state.Namespace, state.Name)
	if err != nil {
		return "", fmt.Errorf("resolve zrok public URL: %w", err)
	}
	if state.PublicURL != publicURL {
		state.PublicURL = publicURL
		if err := dbPutZrokState(ctx.AppDB(), state); err != nil {
			return "", fmt.Errorf("update zrok public URL: %w", err)
		}
	}
	if err := ensureZrokEnvironment(ctx.DataDir(), token); err != nil {
		return "", fmt.Errorf("enable zrok environment: %w", err)
	}
	binary, err := resolveZrokBinary(ctx.Config().Get("zrok2_path"), ctx.DataDir(), false, ctx.Logger().Info)
	if err != nil {
		return "", err
	}

	runID, err := dbInsertRun(ctx.AppDB(), p.Name(), target, string(ModeZrok))
	if err != nil {
		return "", fmt.Errorf("insert run: %w", err)
	}
	params := StartParams{
		Binary: binary, Target: target, Mode: ModeZrok, RunID: runID,
		Hostname: state.PublicURL, ZrokName: state.Name,
		ZrokNamespace: state.Namespace, ZrokHome: zrokHome(ctx.DataDir()),
	}
	if err := p.app.mgr.Start(params); err != nil {
		_, _ = ctx.AppDB().Exec(
			`UPDATE runs SET status = 'failed', finished_at = CURRENT_TIMESTAMP, exit_reason = ? WHERE id = ?`,
			err.Error(), runID)
		return "", err
	}
	return target, nil
}

func (p *zrokProvider) Destroy(ctx *sdk.AppCtx) (bool, error) {
	state, err := dbZrokState(ctx.AppDB())
	if err != nil || state == nil {
		return false, err
	}
	connectionID, token, err := zrokCredentials(ctx)
	if err != nil {
		return false, err
	}
	if connectionID != state.ConnectionID {
		return false, errors.New("the bound zrok connection does not own this name; restore the original connection before deleting it")
	}
	if err := zrokDeleteName(context.Background(), token, state.Namespace, state.Name); err != nil {
		return false, err
	}
	if err := dbDeleteZrokState(ctx.AppDB()); err != nil {
		return false, fmt.Errorf("delete zrok state: %w", err)
	}
	if err := os.RemoveAll(zrokHome(ctx.DataDir())); err != nil {
		return false, fmt.Errorf("remove zrok environment: %w", err)
	}
	return true, nil
}

func (a *App) configureZrok(ctx *sdk.AppCtx, raw json.RawMessage) (*ZrokState, error) {
	var config struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("invalid zrok config: %w", err)
	}
	name, err := normalizeZrokName(config.Name)
	if err != nil {
		return nil, err
	}
	connectionID, token, err := zrokCredentials(ctx)
	if err != nil {
		return nil, err
	}
	existing, err := dbZrokState(ctx.AppDB())
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.ConnectionID != connectionID {
		return nil, errors.New("the bound zrok connection changed — restore the original connection and delete its reserved name first")
	}
	if existing != nil && existing.Name == name {
		publicURL, err := zrokResolveName(context.Background(), token, existing.Namespace, existing.Name)
		if err != nil {
			return nil, fmt.Errorf("resolve zrok public URL: %w", err)
		}
		if existing.PublicURL != publicURL {
			existing.PublicURL = publicURL
			if err := dbPutZrokState(ctx.AppDB(), existing); err != nil {
				return nil, fmt.Errorf("update zrok public URL: %w", err)
			}
		}
		if err := dbSetActiveProvider(ctx.AppDB(), providerNameZrok); err != nil {
			return nil, err
		}
		return existing, nil
	}
	if err := ensureZrokEnvironment(ctx.DataDir(), token); err != nil {
		return nil, fmt.Errorf("enable zrok environment: %w", err)
	}
	if err := zrokCreateName(context.Background(), token, zrokNamespace, name); err != nil {
		return nil, fmt.Errorf("reserve zrok name %q: %w", name, err)
	}
	publicURL, err := zrokResolveName(context.Background(), token, zrokNamespace, name)
	if err != nil {
		_ = zrokDeleteName(context.Background(), token, zrokNamespace, name)
		return nil, fmt.Errorf("resolve zrok public URL: %w", err)
	}
	next := &ZrokState{
		ConnectionID: connectionID,
		Namespace:    zrokNamespace,
		Name:         name,
		PublicURL:    publicURL,
	}
	if err := dbPutZrokState(ctx.AppDB(), next); err != nil {
		_ = zrokDeleteName(context.Background(), token, zrokNamespace, name)
		return nil, fmt.Errorf("persist zrok configuration: %w", err)
	}
	if existing != nil {
		if err := zrokDeleteName(context.Background(), token, existing.Namespace, existing.Name); err != nil {
			return nil, fmt.Errorf("new zrok name is ready, but the old name could not be deleted: %w", err)
		}
	}
	if err := dbSetActiveProvider(ctx.AppDB(), providerNameZrok); err != nil {
		return nil, err
	}
	return next, nil
}

func zrokCredentials(ctx *sdk.AppCtx) (int64, string, error) {
	bound := ctx.IntegrationFor("zrok")
	if bound == nil || bound.ConnectionID == 0 {
		return 0, "", errors.New("bind a zrok connection to this Live Link install first")
	}
	creds, err := ctx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
	if err != nil {
		return 0, "", fmt.Errorf("read zrok credentials: %w", err)
	}
	token := strings.TrimSpace(credValue(creds, "enable_token"))
	if token == "" {
		return 0, "", errors.New("the bound zrok connection has an empty enable token — reconnect it with a valid token")
	}
	return bound.ConnectionID, token, nil
}

func zrokHome(dataDir string) string {
	return filepath.Join(dataDir, "zrok-home")
}

type zrokNativeEnvironment struct {
	AccountToken string `json:"zrok_token"`
	ZitiIdentity string `json:"ziti_identity"`
	APIEndpoint  string `json:"api_endpoint"`
}

func ensureZrokEnvironment(dataDir, token string) error {
	if dataDir == "" {
		return errors.New("no APTEVA_DATA_DIR available")
	}
	home := zrokHome(dataDir)
	root := filepath.Join(home, ".zrok2")
	envPath := filepath.Join(root, "environment.json")
	identityPath := filepath.Join(root, "identities", "environment.json")
	if data, err := os.ReadFile(envPath); err == nil {
		var current zrokNativeEnvironment
		if json.Unmarshal(data, &current) == nil &&
			subtle.ConstantTimeCompare([]byte(current.AccountToken), []byte(token)) == 1 &&
			current.ZitiIdentity != "" {
			if fi, statErr := os.Stat(identityPath); statErr == nil && fi.Mode().IsRegular() && fi.Size() > 0 {
				return nil
			}
		}
	}

	// This directory is dedicated to this app install. Clear an incomplete or
	// differently-bound native environment before enabling the current token.
	if err := os.RemoveAll(home); err != nil {
		return err
	}
	enabled, err := zrokEnableEnvironment(context.Background(), token)
	if err != nil {
		return err
	}
	if enabled.Identity == "" || enabled.Config == "" {
		return errors.New("zrok enable returned an incomplete environment")
	}
	if err := os.MkdirAll(filepath.Dir(identityPath), 0o700); err != nil {
		return err
	}
	if err := writePrivateJSON(filepath.Join(root, "metadata.json"), map[string]string{"v": "v0.4"}); err != nil {
		return err
	}
	if err := writePrivateFile(identityPath, []byte(enabled.Config)); err != nil {
		return err
	}
	return writePrivateJSON(envPath, zrokNativeEnvironment{
		AccountToken: token, ZitiIdentity: enabled.Identity, APIEndpoint: zrokAPIEndpoint,
	})
}

func writePrivateJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(path, append(data, '\n'))
}

func writePrivateFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

type zrokEnableResponse struct {
	Identity string `json:"identity"`
	Config   string `json:"cfg"`
}

type zrokName struct {
	NamespaceToken string `json:"namespaceToken"`
	NamespaceName  string `json:"namespaceName"`
	Name           string `json:"name"`
}

var (
	zrokEnableEnvironment = func(ctx context.Context, token string) (*zrokEnableResponse, error) {
		out := &zrokEnableResponse{}
		err := zrokAPIRequest(ctx, http.MethodPost, "/enable", token,
			map[string]string{"description": "Apteva Live Link", "host": "apteva-live-link"}, out)
		return out, err
	}
	zrokCreateName = func(ctx context.Context, token, namespace, name string) error {
		return zrokAPIRequest(ctx, http.MethodPost, "/share/name", token,
			map[string]string{"namespaceToken": namespace, "name": name}, nil)
	}
	zrokDeleteName = func(ctx context.Context, token, namespace, name string) error {
		return zrokAPIRequest(ctx, http.MethodDelete, "/share/name", token,
			map[string]string{"namespaceToken": namespace, "name": name}, nil)
	}
	zrokResolveName = func(ctx context.Context, token, namespace, name string) (string, error) {
		var names []zrokName
		if err := zrokAPIRequest(ctx, http.MethodGet, "/share/names/"+url.PathEscape(namespace), token, nil, &names); err != nil {
			return "", err
		}
		for _, candidate := range names {
			if candidate.NamespaceToken == namespace && candidate.Name == name {
				return zrokPublicURL(name, candidate.NamespaceName)
			}
		}
		return "", fmt.Errorf("reserved name %q was not returned by namespace %q", name, namespace)
	}
)

func zrokPublicURL(name, namespaceName string) (string, error) {
	hostSuffix := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(namespaceName), "."))
	if hostSuffix == "" || len(name)+1+len(hostSuffix) > 253 {
		return "", errors.New("zrok returned an invalid namespace hostname")
	}
	for _, label := range strings.Split(hostSuffix, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("zrok returned an invalid namespace hostname")
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", errors.New("zrok returned an invalid namespace hostname")
			}
		}
	}
	return "https://" + name + "." + hostSuffix, nil
}

func zrokAPIRequest(ctx context.Context, method, path, token string, body, out any) error {
	var requestBody io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		requestBody = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, zrokAPIEndpoint+"/api/v2"+path, requestBody)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/zrok.v1+json")
	req.Header.Set("Accept", "application/zrok.v1+json")
	req.Header.Set("X-TOKEN", token)
	req.Header.Set("User-Agent", "apteva-live-link/0.6")
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if next.URL.Scheme != "https" || next.URL.Hostname() != "api-v2.zrok.io" {
				return errors.New("zrok API redirect refused")
			}
			if len(via) >= 3 {
				return errors.New("too many zrok API redirects")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, 4<<20)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(limited, 2048))
		safe := strings.ReplaceAll(strings.TrimSpace(string(msg)), token, "[REDACTED]")
		if resp.StatusCode == http.StatusNotFound && method == http.MethodDelete {
			return nil
		}
		return fmt.Errorf("zrok API HTTP %d: %s", resp.StatusCode, safe)
	}
	if out == nil {
		_, err = io.Copy(io.Discard, limited)
		return err
	}
	return json.NewDecoder(limited).Decode(out)
}

func dbZrokState(db *sql.DB) (*ZrokState, error) {
	row := db.QueryRow(`SELECT connection_id, namespace, name, public_url, created_at FROM zrok_state WHERE id = 1`)
	state := &ZrokState{}
	if err := row.Scan(&state.ConnectionID, &state.Namespace, &state.Name, &state.PublicURL, &state.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return state, nil
}

func dbPutZrokState(db *sql.DB, state *ZrokState) error {
	_, err := db.Exec(
		`INSERT INTO zrok_state (id, connection_id, namespace, name, public_url)
		 VALUES (1, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   connection_id = excluded.connection_id,
		   namespace = excluded.namespace,
		   name = excluded.name,
		   public_url = excluded.public_url,
		   updated_at = CURRENT_TIMESTAMP`,
		state.ConnectionID, state.Namespace, state.Name, state.PublicURL)
	return err
}

func dbDeleteZrokState(db *sql.DB) error {
	_, err := db.Exec(`DELETE FROM zrok_state WHERE id = 1`)
	return err
}
