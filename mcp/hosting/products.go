package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Product is what runs on a tenant: an Apteva container, an arbitrary Docker
// image, WordPress, or a bare machine with SSH access.
type Product struct {
	Key         string           `json:"key"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	RuntimeKind string           `json:"runtime_kind"`
	Status      string           `json:"status"`
	Template    json.RawMessage  `json:"template,omitempty"`
	Metadata    json.RawMessage  `json:"metadata,omitempty"`
	Versions    []ProductVersion `json:"versions,omitempty"`
	CreatedAt   string           `json:"created_at"`
	UpdatedAt   string           `json:"updated_at"`
}

type ProductVersion struct {
	ID                int64           `json:"id"`
	ProductKey        string          `json:"product_key"`
	Version           string          `json:"version"`
	Image             string          `json:"image"`
	DefaultPort       int             `json:"default_port"`
	DefaultHealthPath string          `json:"default_health_path"`
	Template          json.RawMessage `json:"template,omitempty"`
	Status            string          `json:"status"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
}

const (
	RuntimeSingleContainer = "single_container"
	RuntimeSSHAccess       = "ssh_access"
)

type runtimeTemplate struct {
	BlueprintSlug string            `json:"blueprint_slug"`
	Image         string            `json:"image"`
	Port          int               `json:"port"`
	HealthPath    string            `json:"health_path"`
	Env           map[string]string `json:"env"`
	Volumes       []runtimeVolume   `json:"volumes"`
	Labels        map[string]string `json:"labels"`
}

type runtimeVolume struct {
	Name      string `json:"name"`
	MountPath string `json:"mount_path"`
	SizeMB    int64  `json:"size_mb,omitempty"`
}

func (p *Product) runsContainer() bool {
	return p != nil && p.RuntimeKind == RuntimeSingleContainer
}

func productTemplate(product *Product) runtimeTemplate {
	var tpl runtimeTemplate
	if product != nil && len(product.Template) > 0 {
		_ = json.Unmarshal(product.Template, &tpl)
	}
	return tpl
}

func seedProducts(db *sql.DB, defaultImage string) error {
	products := []Product{
		{
			Key:         "apteva",
			Name:        "Apteva Tenant",
			Description: "Managed Apteva tenant instance.",
			RuntimeKind: RuntimeSingleContainer,
			Status:      "active",
			Template: jsonRaw(runtimeTemplate{
				BlueprintSlug: "apteva",
				Image:         defaultImage,
				Port:          5280,
				HealthPath:    "/health",
				Env: map[string]string{
					"PORT":              "5280",
					"APTEVA_BIND":       "0.0.0.0",
					"DB_PATH":           "/data/apteva.db",
					"DATA_DIR":          "/data",
					"APTEVA_APPS_CACHE": "/data/apps",
					"CORE_CMD":          "/usr/local/bin/apteva-core",
				},
				Volumes: []runtimeVolume{{Name: "data", MountPath: "/data"}},
			}),
		},
		{
			Key:         "custom-docker",
			Name:        "Custom Docker Image",
			Description: "Single-container hosting for an arbitrary Docker image, including AI servers such as vLLM or Ollama.",
			RuntimeKind: RuntimeSingleContainer,
			Status:      "active",
			Template: jsonRaw(runtimeTemplate{
				BlueprintSlug: "custom-image",
				Port:          80,
				HealthPath:    "/",
				Volumes:       []runtimeVolume{{Name: "data", MountPath: "/data"}},
			}),
		},
		{
			Key:         "wordpress-single",
			Name:        "WordPress",
			Description: "Single-container WordPress hosting template.",
			RuntimeKind: RuntimeSingleContainer,
			Status:      "active",
			Template: jsonRaw(runtimeTemplate{
				BlueprintSlug: "custom-image",
				Image:         "wordpress:php8.3-apache",
				Port:          80,
				HealthPath:    "/wp-admin/setup-config.php",
				Volumes:       []runtimeVolume{{Name: "html", MountPath: "/var/www/html"}},
			}),
		},
		{
			Key:         "ssh-access",
			Name:        "Dedicated machine",
			Description: "A whole machine with SSH access for one customer. Machine backends only.",
			RuntimeKind: RuntimeSSHAccess,
			Status:      "active",
			Template:    json.RawMessage(`{}`),
		},
	}
	for _, p := range products {
		if _, err := db.Exec(`
			INSERT INTO hosting_products (key, name, description, runtime_kind, status, template_json, metadata_json)
			VALUES (?, ?, ?, ?, ?, ?, '{}')
			ON CONFLICT(key) DO UPDATE SET
				name=excluded.name,
				description=excluded.description,
				runtime_kind=excluded.runtime_kind,
				status=excluded.status,
				template_json=excluded.template_json,
				updated_at=CURRENT_TIMESTAMP`,
			p.Key, p.Name, p.Description, p.RuntimeKind, p.Status, stringOrJSON(p.Template, "{}")); err != nil {
			return err
		}
		tpl := productTemplate(&p)
		if _, err := db.Exec(`
			INSERT INTO hosting_product_versions (product_key, version, image, default_port, default_health_path, template_json, status)
			VALUES (?, 'default', ?, ?, ?, ?, 'active')
			ON CONFLICT(product_key, version) DO UPDATE SET
				image=excluded.image,
				default_port=excluded.default_port,
				default_health_path=excluded.default_health_path,
				template_json=excluded.template_json,
				status=excluded.status,
				updated_at=CURRENT_TIMESTAMP`,
			p.Key, tpl.Image, firstPositiveInt(tpl.Port, 80), firstNonEmpty(tpl.HealthPath, "/"), stringOrJSON(p.Template, "{}")); err != nil {
			return err
		}
	}
	return nil
}

const productSelect = `SELECT key, name, description, runtime_kind, status, template_json, metadata_json, created_at, updated_at FROM hosting_products`

func dbProductList(db *sql.DB) ([]*Product, error) {
	rows, err := db.Query(productSelect + ` ORDER BY key`)
	if err != nil {
		return nil, err
	}
	var out []*Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, p := range out {
		if p.Versions, err = dbProductVersions(db, p.Key); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func dbProductGet(db *sql.DB, key string) (*Product, error) {
	p, err := scanProduct(db.QueryRow(productSelect+` WHERE key=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Versions, err = dbProductVersions(db, p.Key)
	return p, err
}

// productForTx reads a product inside a transaction (without versions).
func productForTx(db querier, key string) (*Product, error) {
	p, err := scanProduct(db.QueryRow(productSelect+` WHERE key=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("product %q not found", key)
	}
	return p, err
}

func scanProduct(row rowScanner) (*Product, error) {
	var p Product
	var tpl, meta string
	if err := row.Scan(&p.Key, &p.Name, &p.Description, &p.RuntimeKind, &p.Status, &tpl, &meta, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Template = json.RawMessage(tpl)
	p.Metadata = json.RawMessage(meta)
	return &p, nil
}

func dbProductVersions(db *sql.DB, productKey string) ([]ProductVersion, error) {
	rows, err := db.Query(`SELECT id, product_key, version, image, default_port, default_health_path, template_json, status, created_at, updated_at FROM hosting_product_versions WHERE product_key=? ORDER BY version`, productKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProductVersion
	for rows.Next() {
		var v ProductVersion
		var tpl string
		if err := rows.Scan(&v.ID, &v.ProductKey, &v.Version, &v.Image, &v.DefaultPort, &v.DefaultHealthPath, &tpl, &v.Status, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.Template = json.RawMessage(tpl)
		out = append(out, v)
	}
	return out, rows.Err()
}

// resolveImage picks the image for a container-running product: explicit
// runtime_config wins, then the profile, then the product template.
func resolveImage(defaultImage string, product *Product, profile *Profile, runtimeConfig map[string]any) (string, error) {
	tpl := productTemplate(product)
	image := firstNonEmpty(strArg(runtimeConfig, "image"), profile.Image, tpl.Image)
	if image == "" && product.Key == "apteva" {
		image = defaultImage
	}
	if image == "" {
		return "", fmt.Errorf("image required for product %q", product.Key)
	}
	return image, nil
}

// containerRunInput builds the Containers request for a tenant. instanceID>0
// places the workload on that Instances host instead of the local Docker host.
func containerRunInput(tenant *Tenant, profile *Profile, product *Product, runtimeConfig map[string]any, instanceID int64) map[string]any {
	tpl := productTemplate(product)
	blueprint := firstNonEmpty(strArg(runtimeConfig, "blueprint_slug"), tpl.BlueprintSlug, "custom-image")
	port := intArg(runtimeConfig, "port", tpl.Port)
	if port <= 0 {
		port = 80
	}
	env := map[string]any{}
	for k, v := range tpl.Env {
		env[k] = v
	}
	for k, v := range stringMapArg(runtimeConfig, "env") {
		env[k] = v
	}
	if product.Key == "apteva" {
		env["PORT"] = firstNonEmpty(mapValueString(env, "PORT"), "5280")
		env["APTEVA_BIND"] = firstNonEmpty(mapValueString(env, "APTEVA_BIND"), "0.0.0.0")
		env["APTEVA_HOSTED"] = "1"
		env["APTEVA_HOSTING_ID"] = tenant.ID
		env["APTEVA_OWNER_EMAIL"] = tenant.OwnerEmail
		if tenant.AptevaVersion != "" {
			env["APTEVA_VERSION"] = tenant.AptevaVersion
		}
	}
	input := map[string]any{
		"name":           "hosting-" + tenant.Slug,
		"blueprint_slug": blueprint,
		"image":          tenant.Image,
		"env":            env,
		"resources": map[string]any{
			"cpu":        profile.CPU,
			"memory_mb":  profile.MemoryMB,
			"storage_mb": profile.StorageMB,
		},
		"labels": mergeStringMaps(tpl.Labels, map[string]string{
			"apteva.hosting.tenant_id":   tenant.ID,
			"apteva.hosting.product_key": product.Key,
			"apteva.hosting.profile_key": profile.Key,
		}),
	}
	if instanceID > 0 {
		input["instance_id"] = instanceID
	}
	bind := "127.0.0.1"
	if instanceID > 0 {
		// On a dedicated machine the workload is the customer's: publish it
		// on the host so the machine's public address reaches it.
		bind = "0.0.0.0"
	}
	if blueprint != "apteva" {
		input["ports"] = []map[string]any{{
			"container_port": port,
			"bind_addr":      bind,
			"protocol":       "tcp",
		}}
		input["health_path"] = firstNonEmpty(strArg(runtimeConfig, "health_path"), tpl.HealthPath, "/")
		if len(tpl.Volumes) > 0 {
			volumes := make([]map[string]any, 0, len(tpl.Volumes))
			for _, v := range tpl.Volumes {
				volumes = append(volumes, map[string]any{
					"name":       firstNonEmpty(v.Name, tenant.Slug+"-data"),
					"mount_path": v.MountPath,
					"size_mb":    firstPositive(v.SizeMB, profile.StorageMB),
				})
			}
			input["volumes"] = volumes
		}
	}
	return input
}

type containerWorkload struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Status       string          `json:"status"`
	PublicURL    string          `json:"public_url"`
	HealthStatus string          `json:"health_status"`
	Ports        []containerPort `json:"ports"`
}

type containerPort struct {
	ContainerPort int    `json:"container_port"`
	HostPort      int    `json:"host_port"`
	BindAddr      string `json:"bind_addr"`
	Protocol      string `json:"protocol"`
}

type workloadResp struct {
	Workload containerWorkload `json:"workload"`
}

func workloadTarget(w *containerWorkload) string {
	if strings.HasPrefix(w.PublicURL, "http://") || strings.HasPrefix(w.PublicURL, "https://") {
		return w.PublicURL
	}
	for _, p := range w.Ports {
		if p.ContainerPort == 5280 && p.HostPort > 0 {
			host := firstNonEmpty(p.BindAddr, "127.0.0.1")
			return fmt.Sprintf("http://%s:%d", host, p.HostPort)
		}
	}
	if len(w.Ports) > 0 && w.Ports[0].HostPort > 0 {
		host := firstNonEmpty(w.Ports[0].BindAddr, "127.0.0.1")
		return fmt.Sprintf("http://%s:%d", host, w.Ports[0].HostPort)
	}
	return ""
}
