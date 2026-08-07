package main

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type MobileStoreConfig struct {
	ID             int64  `json:"id"`
	DeploymentID   int64  `json:"deployment_id"`
	EnvironmentID  int64  `json:"environment_id"`
	Platform       string `json:"platform"`
	Provider       string `json:"provider"`
	DesiredJSON    string `json:"desired_json"`
	ObservedJSON   string `json:"observed_json"`
	ValidationJSON string `json:"validation_json"`
	DesiredHash    string `json:"desired_hash"`
	AppliedHash    string `json:"applied_hash"`
	Status         string `json:"status"`
	LastError      string `json:"last_error,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type StoreDocument struct {
	SchemaVersion      int                          `json:"schema_version,omitempty"`
	VersionName        string                       `json:"version_name"`
	DefaultLocale      string                       `json:"default_locale,omitempty"`
	ReleaseMode        string                       `json:"release_mode,omitempty"`
	EarliestReleaseAt  string                       `json:"earliest_release_at,omitempty"`
	Copyright          string                       `json:"copyright,omitempty"`
	UsesIDFA           *bool                        `json:"uses_idfa,omitempty"`
	Localizations      map[string]StoreLocalization `json:"localizations,omitempty"`
	Assets             []StoreAsset                 `json:"assets,omitempty"`
	Review             StoreReview                  `json:"review,omitempty"`
	Classification     StoreClassification          `json:"classification,omitempty"`
	Distribution       StoreDistribution            `json:"distribution,omitempty"`
	Privacy            StorePrivacy                 `json:"privacy,omitempty"`
	ManualRequirements []StoreManualRequirement     `json:"manual_requirements,omitempty"`
	ProviderExtensions map[string]any               `json:"provider_extensions,omitempty"`
}

type StoreLocalization struct {
	Title            string   `json:"title,omitempty"`
	Subtitle         string   `json:"subtitle,omitempty"`
	ShortDescription string   `json:"short_description,omitempty"`
	Description      string   `json:"description,omitempty"`
	Keywords         []string `json:"keywords,omitempty"`
	SupportURL       string   `json:"support_url,omitempty"`
	MarketingURL     string   `json:"marketing_url,omitempty"`
	PromotionalText  string   `json:"promotional_text,omitempty"`
	WhatsNew         string   `json:"whats_new,omitempty"`
	VideoURL         string   `json:"video_url,omitempty"`
}

type StoreAsset struct {
	ID            string `json:"id"`
	Locale        string `json:"locale"`
	Kind          string `json:"kind"`
	DisplayTarget string `json:"display_target,omitempty"`
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	Order         int    `json:"order,omitempty"`
	MIME          string `json:"mime,omitempty"`
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`
}

type StoreReview struct {
	FirstName           string `json:"first_name,omitempty"`
	LastName            string `json:"last_name,omitempty"`
	Email               string `json:"email,omitempty"`
	Phone               string `json:"phone,omitempty"`
	Notes               string `json:"notes,omitempty"`
	DemoAccountRequired bool   `json:"demo_account_required,omitempty"`
	DemoUsername        string `json:"demo_username,omitempty"`
	DemoPassword        string `json:"demo_password,omitempty"`
	DemoPasswordSet     bool   `json:"demo_password_set,omitempty"`
}

type StoreClassification struct {
	PrimaryCategory   string             `json:"primary_category,omitempty"`
	SecondaryCategory string             `json:"secondary_category,omitempty"`
	ContentRating     StoreContentRating `json:"content_rating,omitempty"`
	// AgeDeclaration is a provider-specific compatibility escape hatch. New
	// clients should use ContentRating and let the provider adapter translate it.
	AgeDeclaration map[string]any `json:"age_declaration,omitempty"`
}

type StoreContentRating struct {
	Violence              string `json:"violence,omitempty"`
	SexualContent         string `json:"sexual_content,omitempty"`
	Profanity             string `json:"profanity,omitempty"`
	Drugs                 string `json:"drugs,omitempty"`
	GamblingSimulation    string `json:"gambling_simulation,omitempty"`
	Contests              string `json:"contests,omitempty"`
	Weapons               string `json:"weapons,omitempty"`
	HorrorFear            string `json:"horror_fear,omitempty"`
	MedicalInformation    string `json:"medical_information,omitempty"`
	HealthWellness        string `json:"health_wellness,omitempty"`
	MatureThemes          string `json:"mature_themes,omitempty"`
	UnrestrictedWebAccess bool   `json:"unrestricted_web_access,omitempty"`
	RealMoneyGambling     bool   `json:"real_money_gambling,omitempty"`
	LootBoxes             bool   `json:"loot_boxes,omitempty"`
	Advertising           bool   `json:"advertising,omitempty"`
	MessagingChat         bool   `json:"messaging_chat,omitempty"`
	UserGeneratedContent  bool   `json:"user_generated_content,omitempty"`
	ParentalControls      bool   `json:"parental_controls,omitempty"`
	AgeAssurance          bool   `json:"age_assurance,omitempty"`
	SocialMedia           bool   `json:"social_media,omitempty"`
	SocialMediaAgeGate    bool   `json:"social_media_age_gate,omitempty"`
}

type StoreDistribution struct {
	Territories     []string       `json:"territories,omitempty"`
	PriceTier       string         `json:"price_tier,omitempty"`
	PhasedRelease   bool           `json:"phased_release,omitempty"`
	RolloutFraction float64        `json:"rollout_fraction,omitempty"`
	Provider        map[string]any `json:"provider,omitempty"`
}

type StorePrivacy struct {
	PolicyURL          string          `json:"policy_url,omitempty"`
	ChoicesURL         string          `json:"choices_url,omitempty"`
	DataSafetyCSV      string          `json:"data_safety_csv,omitempty"`
	Declarations       map[string]any  `json:"declarations,omitempty"`
	ManualAttestations map[string]bool `json:"manual_attestations,omitempty"`
}

type StoreManualRequirement struct {
	Code      string `json:"code"`
	Label     string `json:"label"`
	Completed bool   `json:"completed"`
	URL       string `json:"url,omitempty"`
}

type StoreFinding struct {
	Code        string `json:"code"`
	Severity    string `json:"severity"`
	Scope       string `json:"scope"`
	Locale      string `json:"locale,omitempty"`
	Field       string `json:"field,omitempty"`
	Message     string `json:"message"`
	Automatable bool   `json:"automatable"`
	Action      string `json:"action,omitempty"`
}

type StorePreflight struct {
	Ready    bool           `json:"ready"`
	Errors   int            `json:"errors"`
	Warnings int            `json:"warnings"`
	Findings []StoreFinding `json:"findings"`
}

type StorePlan struct {
	Provider    string         `json:"provider"`
	Platform    string         `json:"platform"`
	DesiredHash string         `json:"desired_hash"`
	NoOp        bool           `json:"no_op"`
	Operations  []StorePlanOp  `json:"operations"`
	Preflight   StorePreflight `json:"preflight"`
}

type StorePlanOp struct {
	Scope  string `json:"scope"`
	Action string `json:"action"`
	Count  int    `json:"count,omitempty"`
}

func mobileStoreProvider(platform string) string {
	if platform == "ios" {
		return "app_store_connect"
	}
	if platform == "android" {
		return "google_play"
	}
	return ""
}

func defaultStoreDocument(platform string) StoreDocument {
	doc := StoreDocument{
		SchemaVersion: 1, DefaultLocale: "en-US", ReleaseMode: "manual",
		Localizations:  map[string]StoreLocalization{"en-US": {}},
		Classification: StoreClassification{AgeDeclaration: map[string]any{}},
		Privacy: StorePrivacy{
			Declarations: map[string]any{}, ManualAttestations: map[string]bool{},
		},
		ProviderExtensions: map[string]any{},
	}
	if platform == "android" {
		doc.ReleaseMode = "immediate"
	}
	return doc
}

func parseStoreDocument(raw string, platform string) (StoreDocument, error) {
	doc := defaultStoreDocument(platform)
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "{}" {
		return doc, nil
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return StoreDocument{}, fmt.Errorf("invalid store document: %w", err)
	}
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = 1
	}
	if doc.SchemaVersion != 1 {
		return StoreDocument{}, fmt.Errorf("unsupported store schema_version %d", doc.SchemaVersion)
	}
	releaseMode, err := normalizeStoreReleaseMode(platform, doc.ReleaseMode)
	if err != nil {
		return StoreDocument{}, err
	}
	doc.ReleaseMode = releaseMode
	if doc.ReleaseMode == "scheduled" {
		if _, err := time.Parse(time.RFC3339, doc.EarliestReleaseAt); err != nil {
			legacy, legacyErr := time.Parse("2006-01-02T15:04", doc.EarliestReleaseAt)
			if legacyErr != nil {
				return StoreDocument{}, errors.New("earliest_release_at must be an RFC3339 timestamp for scheduled release")
			}
			doc.EarliestReleaseAt = legacy.UTC().Format(time.RFC3339)
		}
	} else {
		doc.EarliestReleaseAt = ""
	}
	doc.DefaultLocale = defaultStr(strings.TrimSpace(doc.DefaultLocale), "en-US")
	if doc.Localizations == nil {
		doc.Localizations = map[string]StoreLocalization{}
	}
	if doc.Privacy.Declarations == nil {
		doc.Privacy.Declarations = map[string]any{}
	}
	if doc.Privacy.ManualAttestations == nil {
		doc.Privacy.ManualAttestations = map[string]bool{}
	}
	if doc.ProviderExtensions == nil {
		doc.ProviderExtensions = map[string]any{}
	}
	return doc, nil
}

func normalizeStoreReleaseMode(platform, raw string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	if platform == "ios" {
		mode = defaultStr(mode, "manual")
		if mode == "automatic" {
			mode = "after_approval"
		}
		switch mode {
		case "manual", "after_approval", "scheduled":
			return mode, nil
		default:
			return "", errors.New("iOS release_mode must be manual, after_approval, or scheduled")
		}
	}
	mode = defaultStr(mode, "immediate")
	// Older Deploy versions persisted "automatic" for Android. It meant an
	// immediate rollout, so normalize it without breaking existing listings.
	if mode == "automatic" {
		mode = "immediate"
	}
	switch mode {
	case "immediate", "staged":
		return mode, nil
	default:
		return "", errors.New("Android release_mode must be immediate or staged")
	}
}

func canonicalStoreDocument(doc StoreDocument) (string, string, error) {
	// Review passwords are one-shot inputs to store apply and are never
	// persisted in the desired listing document.
	doc.Review.DemoPassword = ""
	body, err := json.Marshal(doc)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(body)
	return string(body), hex.EncodeToString(sum[:]), nil
}

func dbGetMobileStoreConfig(db *sql.DB, deploymentID, environmentID int64, platform string) (*MobileStoreConfig, error) {
	provider := mobileStoreProvider(platform)
	row := db.QueryRow(`
		SELECT id, deployment_id, environment_id, platform, provider,
		       desired_json, observed_json, validation_json,
		       desired_hash, applied_hash, status, last_error,
		       created_at, updated_at
		  FROM mobile_store_configs
		 WHERE deployment_id = ? AND environment_id = ? AND platform = ? AND provider = ?
	`, deploymentID, environmentID, platform, provider)
	var cfg MobileStoreConfig
	err := row.Scan(
		&cfg.ID, &cfg.DeploymentID, &cfg.EnvironmentID, &cfg.Platform, &cfg.Provider,
		&cfg.DesiredJSON, &cfg.ObservedJSON, &cfg.ValidationJSON,
		&cfg.DesiredHash, &cfg.AppliedHash, &cfg.Status, &cfg.LastError,
		&cfg.CreatedAt, &cfg.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &cfg, err
}

func dbUpsertMobileStoreConfig(db *sql.DB, d *Deployment, doc StoreDocument) (*MobileStoreConfig, error) {
	if d == nil || mobileStoreProvider(d.TargetKind) == "" {
		return nil, errors.New("mobile deployment required")
	}
	desiredJSON, desiredHash, err := canonicalStoreDocument(doc)
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	_, err = db.Exec(`
		INSERT INTO mobile_store_configs (
			deployment_id, environment_id, platform, provider, desired_json,
			desired_hash, status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, 'draft', ?, ?)
		ON CONFLICT(deployment_id, environment_id, platform, provider) DO UPDATE SET
			desired_json = excluded.desired_json,
			desired_hash = excluded.desired_hash,
			status = CASE
				WHEN mobile_store_configs.applied_hash = excluded.desired_hash THEN 'applied'
				ELSE 'draft'
			END,
			last_error = '',
			updated_at = excluded.updated_at
	`, d.ID, d.EnvironmentID, d.TargetKind, mobileStoreProvider(d.TargetKind), desiredJSON, desiredHash, now, now)
	if err != nil {
		return nil, err
	}
	return dbGetMobileStoreConfig(db, d.ID, d.EnvironmentID, d.TargetKind)
}

func dbUpdateMobileStoreState(db *sql.DB, id int64, status, observedJSON, validationJSON, appliedHash, lastError string) error {
	_, err := db.Exec(`
		UPDATE mobile_store_configs
		   SET status = ?,
		       observed_json = CASE WHEN ? = '' THEN observed_json ELSE ? END,
		       validation_json = CASE WHEN ? = '' THEN validation_json ELSE ? END,
		       applied_hash = CASE WHEN ? = '' THEN applied_hash ELSE ? END,
		       last_error = ?,
		       updated_at = ?
		 WHERE id = ?
	`, status, observedJSON, observedJSON, validationJSON, validationJSON,
		appliedHash, appliedHash, lastError, nowUTC(), id)
	return err
}

func (a *App) mobileStoreConfig(d *Deployment) (*MobileStoreConfig, StoreDocument, error) {
	cfg, err := dbGetMobileStoreConfig(globalCtx.AppDB(), d.ID, d.EnvironmentID, d.TargetKind)
	if err != nil {
		return nil, StoreDocument{}, err
	}
	if cfg == nil {
		return nil, defaultStoreDocument(d.TargetKind), nil
	}
	doc, err := parseStoreDocument(cfg.DesiredJSON, d.TargetKind)
	return cfg, doc, err
}

func (a *App) storePreflight(d *Deployment, build *Build, strict bool) (StorePreflight, error) {
	cfg, doc, err := a.mobileStoreConfig(d)
	if err != nil {
		return StorePreflight{}, err
	}
	out := validateStoreDocument(a.dataDir, d, build, cfg, doc, strict)
	if cfg != nil {
		status := "ready"
		if !out.Ready {
			status = "blocked"
		}
		_ = dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, status, "", mustJSON(out), "", "")
	}
	return out, nil
}

func validateStoreDocument(dataDir string, d *Deployment, build *Build, cfg *MobileStoreConfig, doc StoreDocument, strict bool) StorePreflight {
	findings := []StoreFinding{}
	add := func(code, severity, scope, locale, field, message, action string, automatable bool) {
		findings = append(findings, StoreFinding{
			Code: code, Severity: severity, Scope: scope, Locale: locale, Field: field,
			Message: message, Action: action, Automatable: automatable,
		})
	}
	if cfg == nil {
		add("store.not_configured", "error", "listing", "", "", "Store listing is not configured.", "Save the Store listing before submitting for review.", true)
	} else {
		if strings.TrimSpace(doc.VersionName) == "" {
			add("version.required", "error", "version", "", "version_name", "Store version is required.", "Set the marketing version used by the binary.", true)
		}
		if build != nil {
			if manifest, err := readArtifactManifest(build); err == nil && manifest.VersionName != "" && doc.VersionName != "" && manifest.VersionName != doc.VersionName {
				add("version.binary_mismatch", "error", "version", "", "version_name",
					fmt.Sprintf("Listing version %s does not match binary version %s.", doc.VersionName, manifest.VersionName),
					"Rebuild with the listing marketing version or update the editable listing.", true)
			}
		}
		if len(doc.Localizations) == 0 {
			add("localization.required", "error", "localization", "", "", "At least one localization is required.", "Add the default store locale.", true)
		}
		for locale, loc := range doc.Localizations {
			if strings.TrimSpace(loc.Description) == "" {
				add("description.required", "error", "localization", locale, "description", "Description is required.", "Add a localized description.", true)
			}
			if strings.TrimSpace(loc.Title) == "" {
				add("title.required", "error", "localization", locale, "title", "Store title is required.", "Add a localized title.", true)
			}
			if d.TargetKind == "android" {
				if strings.TrimSpace(loc.ShortDescription) == "" {
					add("short_description.required", "error", "localization", locale, "short_description", "Play short description is required.", "Add a localized short description.", true)
				}
			} else {
				if strings.TrimSpace(loc.SupportURL) == "" {
					add("support_url.required", "error", "localization", locale, "support_url", "Support URL is required for App Store review.", "Add a localized support URL.", true)
				}
				if len(loc.Keywords) == 0 {
					add("keywords.required", "error", "localization", locale, "keywords", "App Store keywords are required.", "Add localized keywords.", true)
				}
			}
		}
		for _, finding := range validateStoreAssets(dataDir, d, build, doc) {
			findings = append(findings, finding)
		}
		if d.TargetKind == "ios" {
			if doc.Review.FirstName == "" || doc.Review.LastName == "" || doc.Review.Email == "" || doc.Review.Phone == "" {
				add("review_contact.required", "error", "review", "", "contact", "Apple review contact name, email, and phone are required.", "Complete the review contact.", true)
			}
			if doc.Review.DemoAccountRequired && (doc.Review.DemoUsername == "" || (!doc.Review.DemoPasswordSet && doc.Review.DemoPassword == "")) {
				add("review_demo.required", "error", "review", "", "demo_account", "Demo credentials are required when login is needed.", "Provide review demo credentials.", true)
			}
			if doc.Classification.PrimaryCategory == "" {
				add("category.required", "error", "classification", "", "primary_category", "Primary App Store category is required.", "Select a category.", true)
			}
			if !storeContentRatingComplete(doc.Classification.ContentRating) && len(doc.Classification.AgeDeclaration) == 0 {
				add("age_rating.required", "error", "classification", "", "content_rating", "Apple age-rating declarations are required.", "Complete the age-rating questionnaire.", true)
			}
			if !doc.Privacy.ManualAttestations["apple_privacy_published"] {
				add("privacy.apple_manual", "error", "privacy", "", "apple_privacy_published", "Apple privacy answers must be reviewed and published in App Store Connect.", "Complete and attest the App Privacy questionnaire.", false)
			}
		} else {
			if doc.ReleaseMode == "staged" && (doc.Distribution.RolloutFraction <= 0 || doc.Distribution.RolloutFraction >= 1) {
				add("rollout_fraction.required", "error", "distribution", "", "rollout_fraction", "A staged Google Play rollout requires a fraction greater than 0 and less than 1.", "Choose the initial production rollout percentage.", true)
			}
			legacyAppContentReady := doc.Privacy.ManualAttestations["google_app_content_complete"]
			if doc.Classification.PrimaryCategory == "" && !legacyAppContentReady {
				add("category.required", "error", "classification", "", "primary_category", "Google Play category is required.", "Select a Play category before completing App Content.", false)
			}
			if !storeContentRatingComplete(doc.Classification.ContentRating) && !legacyAppContentReady {
				add("content_rating.required", "error", "classification", "", "content_rating", "The content-rating questionnaire is incomplete.", "Complete every content-rating field, then submit the answers in Play Console.", false)
			}
			if doc.Privacy.DataSafetyCSV == "" && !doc.Privacy.ManualAttestations["google_data_safety_published"] {
				add("privacy.google_data_safety", "error", "privacy", "", "data_safety_csv", "Google Data Safety must be provided through CSV or attested as already published.", "Add the Play Data Safety CSV.", true)
			}
			if !doc.Privacy.ManualAttestations["google_app_content_complete"] {
				add("google.app_content_manual", "error", "compliance", "", "google_app_content_complete", "Google Play category, content rating, and App Content declarations require confirmation in Play Console.", "Complete and attest the Play Console requirements.", false)
			}
		}
		if strings.TrimSpace(doc.Privacy.PolicyURL) == "" {
			add("privacy_policy.required", "error", "privacy", "", "policy_url", "A privacy-policy URL is required.", "Add the published privacy-policy URL.", true)
		}
		providerDistribution := doc.Distribution.Provider
		availabilityVerified := providerReadinessVerified(cfg, "availability") || boolMapValue(providerDistribution, "availability_configured")
		pricingVerified := providerReadinessVerified(cfg, "pricing") || boolMapValue(providerDistribution, "pricing_configured")
		if len(doc.Distribution.Territories) == 0 && !availabilityVerified {
			add("availability.required", "error", "distribution", "", "territories", "Store availability has not been configured or verified.", "Set territories or attest the existing provider availability.", d.TargetKind == "ios")
		}
		if doc.Distribution.PriceTier == "" && !pricingVerified {
			add("pricing.required", "error", "distribution", "", "price_tier", "Store pricing has not been configured or verified.", "Set FREE/a provider price point or attest the existing provider pricing.", d.TargetKind == "ios")
		}
		if len(doc.Distribution.Territories) > 0 && d.TargetKind == "ios" &&
			providerExtensionBody(doc, "app_store_connect", "availability_body") == nil &&
			!availabilityVerified {
			add("availability.apple_payload", "error", "distribution", "", "provider_extensions", "Apple availability requires a provider payload or verification of the existing setting.", "Provide app_store_connect.availability_body or attest the current availability.", true)
		}
		if doc.Distribution.PriceTier != "" && doc.Distribution.PriceTier != "FREE" && d.TargetKind == "ios" &&
			providerExtensionBody(doc, "app_store_connect", "price_schedule_body") == nil &&
			!pricingVerified {
			add("pricing.apple_payload", "error", "distribution", "", "provider_extensions", "Paid Apple pricing requires an AppPriceSchedule provider payload.", "Provide app_store_connect.price_schedule_body or attest the current pricing.", true)
		}
		if doc.Distribution.PriceTier == "FREE" && d.TargetKind == "ios" &&
			!pricingVerified {
			add("pricing.apple_free_verify", "error", "distribution", "", "pricing_configured", "The app's free pricing setting has not been verified.", "Verify the existing App Store pricing.", false)
		}
		if d.TargetKind == "android" && len(doc.Distribution.Territories) > 0 &&
			!availabilityVerified {
			add("availability.google_manual", "error", "distribution", "", "availability_configured", "Google Play availability changes are not writable through the publishing API.", "Configure countries in Play Console and verify the existing setting.", false)
		}
		if d.TargetKind == "android" && doc.Distribution.PriceTier != "" &&
			!pricingVerified {
			add("pricing.google_manual", "error", "distribution", "", "pricing_configured", "Google Play app pricing is not writable through the publishing API.", "Configure pricing in Play Console and verify the existing setting.", false)
		}
		for _, requirement := range doc.ManualRequirements {
			if !requirement.Completed {
				add("manual."+requirement.Code, "error", "manual", "", requirement.Code, requirement.Label+" is not complete.", "Complete the provider-console requirement and mark it complete.", false)
			}
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		order := map[string]int{"error": 0, "warning": 1, "info": 2}
		if order[findings[i].Severity] != order[findings[j].Severity] {
			return order[findings[i].Severity] < order[findings[j].Severity]
		}
		return findings[i].Code < findings[j].Code
	})
	out := StorePreflight{Findings: findings}
	for _, finding := range findings {
		if finding.Severity == "error" {
			out.Errors++
		}
		if finding.Severity == "warning" {
			out.Warnings++
		}
	}
	out.Ready = out.Errors == 0 && (!strict || cfg != nil)
	return out
}

func (a *App) storePlan(d *Deployment, build *Build, strict bool) (StorePlan, error) {
	cfg, doc, err := a.mobileStoreConfig(d)
	if err != nil {
		return StorePlan{}, err
	}
	preflight := validateStoreDocument(a.dataDir, d, build, cfg, doc, strict)
	plan := StorePlan{
		Provider: mobileStoreProvider(d.TargetKind), Platform: d.TargetKind,
		Preflight: preflight,
	}
	if cfg == nil {
		return plan, nil
	}
	plan.DesiredHash = cfg.DesiredHash
	plan.NoOp = cfg.AppliedHash != "" && cfg.AppliedHash == cfg.DesiredHash
	if plan.NoOp {
		return plan, nil
	}
	plan.Operations = append(plan.Operations,
		StorePlanOp{Scope: "version", Action: "ensure"},
		StorePlanOp{Scope: "localizations", Action: "upsert", Count: len(doc.Localizations)},
	)
	if len(doc.Assets) > 0 {
		plan.Operations = append(plan.Operations, StorePlanOp{Scope: "media", Action: "synchronize", Count: len(doc.Assets)})
	}
	plan.Operations = append(plan.Operations,
		StorePlanOp{Scope: "review", Action: "upsert"},
		StorePlanOp{Scope: "classification", Action: "upsert"},
		StorePlanOp{Scope: "privacy", Action: "reconcile"},
		StorePlanOp{Scope: "distribution", Action: "reconcile"},
	)
	return plan, nil
}

func (a *App) applyStoreConfig(d *Deployment, build *Build, strict bool) (*MobileStoreConfig, error) {
	return a.applyStoreConfigWithReviewSecret(d, build, strict, "")
}

func (a *App) applyStoreConfigWithReviewSecret(d *Deployment, build *Build, strict bool, reviewPassword string) (*MobileStoreConfig, error) {
	cfg, doc, err := a.mobileStoreConfig(d)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, errors.New("store listing is not configured")
	}
	if strings.TrimSpace(reviewPassword) != "" {
		doc.Review.DemoPassword = reviewPassword
		doc.Review.DemoPasswordSet = true
	}
	// Refresh readable provider state before validating. This lets a first
	// apply rely on provider-confirmed pricing/availability without requiring
	// a separate manual Sync click.
	if fresh, _, observeErr := a.observeStoreConfig(d); observeErr == nil && fresh != nil {
		cfg = fresh
	}
	preflight := validateStoreDocument(a.dataDir, d, build, cfg, doc, strict)
	if !preflight.Ready {
		_ = dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, "blocked", "", mustJSON(preflight), "", "store preflight failed")
		return nil, fmt.Errorf("store preflight failed with %d error(s)", preflight.Errors)
	}
	if cfg.AppliedHash == cfg.DesiredHash && strings.TrimSpace(reviewPassword) == "" {
		fresh, _, observeErr := a.observeStoreConfig(d)
		return fresh, observeErr
	}
	_ = dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, "applying", "", mustJSON(preflight), "", "")
	var applied map[string]any
	switch d.TargetKind {
	case "ios":
		applied, err = a.applyAppleStoreConfig(d, doc)
	case "android":
		applied, err = a.applyGoogleStoreConfig(d, doc)
	default:
		err = fmt.Errorf("unsupported store platform %q", d.TargetKind)
	}
	if err != nil {
		_ = dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, "failed", "", mustJSON(preflight), "", err.Error())
		return nil, err
	}
	if strings.TrimSpace(reviewPassword) != "" {
		doc.Review.DemoPassword = ""
		doc.Review.DemoPasswordSet = true
		cfg, err = dbUpsertMobileStoreConfig(globalCtx.AppDB(), d, doc)
		if err != nil {
			return nil, err
		}
	}
	var observed map[string]any
	switch d.TargetKind {
	case "ios":
		observed, err = a.observeAppleStoreConfig(d, doc)
	case "android":
		observed, err = a.observeGoogleStoreConfig(d, doc)
	}
	if err != nil {
		_ = dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, "failed", "", mustJSON(preflight), "", "store applied but provider verification failed: "+err.Error())
		return nil, fmt.Errorf("store applied but provider verification failed: %w", err)
	}
	if observed == nil {
		observed = map[string]any{}
	}
	observed["last_apply"] = applied
	observed["applied_at"] = nowUTC()
	observed["desired_hash"] = cfg.DesiredHash
	verifiedCfg := *cfg
	verifiedCfg.ObservedJSON = mustJSON(observed)
	verified := validateStoreDocument(a.dataDir, d, build, &verifiedCfg, doc, strict)
	status := "applied"
	lastError := ""
	if !verified.Ready {
		status = "blocked"
		lastError = "provider verification did not satisfy store preflight"
	}
	if err := dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, status, mustJSON(observed), mustJSON(verified), cfg.DesiredHash, lastError); err != nil {
		return nil, err
	}
	return dbGetMobileStoreConfig(globalCtx.AppDB(), d.ID, d.EnvironmentID, d.TargetKind)
}

func (a *App) observeStoreConfig(d *Deployment) (*MobileStoreConfig, map[string]any, error) {
	cfg, doc, err := a.mobileStoreConfig(d)
	if err != nil {
		return nil, nil, err
	}
	if cfg == nil {
		return nil, nil, errors.New("store listing is not configured")
	}
	var observed map[string]any
	switch d.TargetKind {
	case "ios":
		observed, err = a.observeAppleStoreConfig(d, doc)
	case "android":
		observed, err = a.observeGoogleStoreConfig(d, doc)
	default:
		err = fmt.Errorf("unsupported store platform %q", d.TargetKind)
	}
	if err != nil {
		_ = dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, "failed", "", "", "", err.Error())
		return nil, nil, err
	}
	observed["observed_at"] = nowUTC()
	status := cfg.Status
	if status == "failed" {
		status = "draft"
	}
	if err := dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, status, mustJSON(observed), "", "", ""); err != nil {
		return nil, nil, err
	}
	cfg, err = dbGetMobileStoreConfig(globalCtx.AppDB(), d.ID, d.EnvironmentID, d.TargetKind)
	return cfg, observed, err
}

func (a *App) observeAppleStoreConfig(d *Deployment, doc StoreDocument) (map[string]any, error) {
	bound, err := boundIntegration("app_store")
	if err != nil {
		return nil, err
	}
	target, err := parseMobileTargetConfig(d.TargetConfigJSON)
	if err != nil {
		return nil, err
	}
	appID := target.AppStoreAppID
	if appID == "" {
		apps, err := executeIntegration(bound, "list_apps", map[string]any{"bundle_id": target.BundleID, "limit": 2})
		if err != nil {
			return nil, err
		}
		appID = firstJSONAPIID(apps)
	}
	if appID == "" {
		return nil, errors.New("App Store Connect app record not found")
	}
	versions, err := executeIntegration(bound, "list_app_versions", map[string]any{
		"app_id": appID, "platform": "IOS", "version_string": doc.VersionName, "limit": 10,
	})
	if err != nil {
		return nil, err
	}
	versionID := firstJSONAPIID(versions)
	observed := map[string]any{
		"app_id": appID, "version_id": versionID, "versions": decodeJSONValue(versions),
		"localizations": map[string]any{}, "readiness": map[string]any{},
	}
	if versionID == "" {
		observed["readiness"].(map[string]any)["listing"] = readinessCheck(false, "provider", "The configured store version does not exist yet.")
	} else {
		localizations := observed["localizations"].(map[string]any)
		localizationsReady := len(doc.Localizations) > 0
		for locale := range doc.Localizations {
			raw, err := executeIntegration(bound, "list_version_localizations", map[string]any{"version_id": versionID, "locale": locale})
			if err != nil {
				return nil, err
			}
			value := decodeJSONValue(raw)
			localizations[locale] = value
			localizationsReady = localizationsReady && jsonValueHasData(value)
		}
		readiness := observed["readiness"].(map[string]any)
		readiness["listing"] = readinessCheck(localizationsReady, "provider", "App Store version and requested localizations were read from Apple.")
		if review, err := executeIntegration(bound, "get_version_review_detail", map[string]any{"version_id": versionID}); err == nil {
			value := decodeJSONValue(review)
			observed["review"] = value
			readiness["review"] = readinessCheck(jsonValueHasData(value), "provider", "App Review details were read from Apple.")
		}
	}
	readiness := observed["readiness"].(map[string]any)
	appInfoID := ""
	if infos, err := executeIntegration(bound, "list_app_infos", map[string]any{"app_id": appID, "limit": 10}); err == nil {
		appInfoID = firstJSONAPIID(infos)
		value := decodeJSONValue(infos)
		observed["app_infos"] = value
		readiness["classification"] = readinessCheck(jsonValueHasData(value), "provider", "App information was read from Apple.")
	}
	if appInfoID != "" {
		age, err := executeIntegration(bound, "get_app_age_rating_declaration", map[string]any{"app_info_id": appInfoID})
		if err == nil {
			value := decodeJSONValue(age)
			observed["age_rating"] = value
			readiness["classification"] = readinessCheck(jsonValueHasData(value), "provider", "Age-rating declarations were read from Apple.")
		}
	}
	if pricing, err := executeIntegration(bound, "get_app_price_schedule", map[string]any{"app_id": appID, "include": "baseTerritory,manualPrices"}); err == nil {
		value := decodeJSONValue(pricing)
		observed["pricing"] = value
		readiness["pricing"] = readinessCheck(jsonValueHasData(value), "provider", "Pricing was read from Apple.")
	}
	if availability, err := executeIntegration(bound, "get_app_availability", map[string]any{"app_id": appID}); err == nil {
		value := decodeJSONValue(availability)
		observed["availability"] = value
		readiness["availability"] = readinessCheck(jsonValueHasData(value), "provider", "Availability was read from Apple.")
	}
	return observed, nil
}

func (a *App) observeGoogleStoreConfig(d *Deployment, doc StoreDocument) (map[string]any, error) {
	bound, err := boundIntegration("play_store")
	if err != nil {
		return nil, err
	}
	target, err := parseMobileTargetConfig(d.TargetConfigJSON)
	if err != nil {
		return nil, err
	}
	created, err := executeIntegration(bound, "create_edit", map[string]any{"packageName": target.PackageName})
	if err != nil {
		return nil, err
	}
	editID := jsonStringAt(created, "id")
	if editID == "" {
		return nil, errors.New("Google Play create_edit response missing id")
	}
	defer func() {
		_, _ = executeIntegration(bound, "delete_edit", map[string]any{"packageName": target.PackageName, "editId": editID})
	}()
	details, err := executeIntegration(bound, "get_app_details", map[string]any{"packageName": target.PackageName, "editId": editID})
	if err != nil {
		return nil, err
	}
	listings, err := executeIntegration(bound, "list_store_listings", map[string]any{"packageName": target.PackageName, "editId": editID})
	if err != nil {
		return nil, err
	}
	images := map[string]any{}
	seen := map[string]bool{}
	for _, asset := range doc.Assets {
		imageType := googleImageType(asset)
		if imageType == "" {
			continue
		}
		locale := defaultStr(asset.Locale, doc.DefaultLocale)
		key := locale + "/" + imageType
		if seen[key] {
			continue
		}
		seen[key] = true
		raw, err := executeIntegration(bound, "list_listing_images", map[string]any{
			"packageName": target.PackageName, "editId": editID, "language": locale, "imageType": imageType,
		})
		if err != nil {
			return nil, err
		}
		images[key] = decodeJSONValue(raw)
	}
	var availability any
	availabilityVerified := false
	if raw, availabilityErr := executeIntegration(bound, "get_country_availability", map[string]any{
		"packageName": target.PackageName, "editId": editID, "track": "production",
	}); availabilityErr == nil {
		availability = decodeJSONValue(raw)
		if value, ok := availability.(map[string]any); ok {
			_, availabilityVerified = value["countries"]
		}
	}
	listingValue := decodeJSONValue(listings)
	imagesReady := len(doc.Assets) == 0
	if len(doc.Assets) > 0 {
		imagesReady = len(images) > 0
		for _, value := range images {
			imagesReady = imagesReady && jsonValueHasData(value)
		}
	}
	return map[string]any{
		"package_name": target.PackageName, "details": decodeJSONValue(details),
		"listings": listingValue, "images": images, "availability": availability,
		"readiness": map[string]any{
			"listing":      readinessCheck(jsonValueHasData(listingValue), "provider", "Play Store listings were read from Google."),
			"media":        readinessCheck(imagesReady, "provider", "Configured Play media was read from Google."),
			"pricing":      readinessCheck(boolMapValue(doc.Distribution.Provider, "pricing_configured"), "manual", "Google Play pricing is not writable through the publishing API."),
			"availability": readinessCheck(availabilityVerified, "provider", "Production country availability was read from Google Play."),
		},
	}, nil
}

func decodeJSONValue(raw json.RawMessage) any {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	return value
}

func boolMapValue(values map[string]any, key string) bool {
	if values == nil {
		return false
	}
	value, _ := values[key].(bool)
	return value
}

func storeContentRatingComplete(rating StoreContentRating) bool {
	for _, value := range []string{
		rating.Violence, rating.SexualContent, rating.Profanity, rating.Drugs,
		rating.GamblingSimulation, rating.Contests, rating.Weapons, rating.HorrorFear,
		rating.MedicalInformation, rating.HealthWellness, rating.MatureThemes,
	} {
		switch strings.ToUpper(strings.TrimSpace(value)) {
		case "NONE", "INFREQUENT", "FREQUENT", "INFREQUENT_OR_MILD", "FREQUENT_OR_INTENSE":
		default:
			return false
		}
	}
	return true
}

func appleAgeDeclaration(classification StoreClassification) map[string]any {
	attributes := map[string]any{}
	for key, value := range classification.AgeDeclaration {
		attributes[key] = value
	}
	if !storeContentRatingComplete(classification.ContentRating) {
		return attributes
	}
	rating := classification.ContentRating
	for key, value := range map[string]string{
		"violenceCartoonOrFantasy":                    rating.Violence,
		"violenceRealistic":                           rating.Violence,
		"violenceRealisticProlongedGraphicOrSadistic": rating.Violence,
		"sexualContentOrNudity":                       rating.SexualContent,
		"sexualContentGraphicAndNudity":               rating.SexualContent,
		"profanityOrCrudeHumor":                       rating.Profanity,
		"alcoholTobaccoOrDrugUseOrReferences":         rating.Drugs,
		"gamblingSimulated":                           rating.GamblingSimulation,
		"contests":                                    rating.Contests,
		"gunsOrOtherWeapons":                          rating.Weapons,
		"horrorOrFearThemes":                          rating.HorrorFear,
		"medicalOrTreatmentInformation":               rating.MedicalInformation,
		"healthOrWellnessTopics":                      rating.HealthWellness,
		"matureOrSuggestiveThemes":                    rating.MatureThemes,
	} {
		attributes[key] = normalizeAppleRatingLevel(value)
	}
	attributes["unrestrictedWebAccess"] = rating.UnrestrictedWebAccess
	attributes["gambling"] = rating.RealMoneyGambling
	attributes["lootBox"] = rating.LootBoxes
	attributes["advertising"] = rating.Advertising
	attributes["messagingAndChat"] = rating.MessagingChat
	attributes["userGeneratedContent"] = rating.UserGeneratedContent
	attributes["parentalControls"] = rating.ParentalControls
	attributes["ageAssurance"] = rating.AgeAssurance
	attributes["socialMedia"] = rating.SocialMedia
	attributes["socialMediaAgeRestricted"] = rating.SocialMediaAgeGate
	return attributes
}

func normalizeAppleRatingLevel(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "INFREQUENT_OR_MILD":
		return "INFREQUENT"
	case "FREQUENT_OR_INTENSE":
		return "FREQUENT"
	default:
		return strings.ToUpper(strings.TrimSpace(value))
	}
}

func providerReadinessVerified(cfg *MobileStoreConfig, key string) bool {
	if cfg == nil || strings.TrimSpace(cfg.ObservedJSON) == "" {
		return false
	}
	var observed struct {
		Readiness map[string]struct {
			Status string `json:"status"`
		} `json:"readiness"`
	}
	if json.Unmarshal([]byte(cfg.ObservedJSON), &observed) != nil {
		return false
	}
	return strings.EqualFold(observed.Readiness[key].Status, "verified")
}

func readinessCheck(verified bool, source, message string) map[string]any {
	status := "unknown"
	if verified {
		status = "verified"
	}
	return map[string]any{"status": status, "source": source, "message": message}
}

func jsonValueHasData(value any) bool {
	values, ok := value.(map[string]any)
	if !ok {
		return value != nil
	}
	data, exists := values["data"]
	if !exists || data == nil {
		return false
	}
	if list, ok := data.([]any); ok {
		return len(list) > 0
	}
	return true
}

func providerExtensionBody(doc StoreDocument, provider, key string) map[string]any {
	raw, ok := doc.ProviderExtensions[provider]
	if !ok {
		return nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	body, _ := values[key].(map[string]any)
	return body
}

func jsonStringFromValue(value any, path ...string) string {
	for _, key := range path {
		values, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		value = values[key]
	}
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func (a *App) applyAppleStoreConfig(d *Deployment, doc StoreDocument) (map[string]any, error) {
	bound, err := boundIntegration("app_store")
	if err != nil {
		return nil, err
	}
	target, err := parseMobileTargetConfig(d.TargetConfigJSON)
	if err != nil {
		return nil, err
	}
	appID := target.AppStoreAppID
	if appID == "" {
		apps, err := executeIntegration(bound, "list_apps", map[string]any{"bundle_id": target.BundleID, "limit": 2})
		if err != nil {
			return nil, err
		}
		appID = firstJSONAPIID(apps)
	}
	if appID == "" {
		return nil, errors.New("App Store Connect app record not found")
	}
	versionID, err := ensureAppleStoreVersion(bound, appID, doc)
	if err != nil {
		return nil, err
	}
	localizationIDs := map[string]string{}
	for locale, loc := range doc.Localizations {
		id, err := upsertAppleVersionLocalization(bound, versionID, locale, loc)
		if err != nil {
			return nil, err
		}
		localizationIDs[locale] = id
	}
	reviewDetailID, err := a.applyAppleReviewDetail(bound, versionID, doc.Review)
	if err != nil {
		return nil, err
	}
	if err := a.applyAppleMetadata(bound, appID, doc); err != nil {
		return nil, err
	}
	for _, asset := range doc.Assets {
		if !strings.Contains(asset.Kind, "screenshot") && asset.Kind != "app_preview" && asset.Kind != "review_attachment" {
			continue
		}
		if asset.Kind == "review_attachment" {
			err = a.uploadAppleReviewAttachment(bound, d, reviewDetailID, asset)
		} else {
			localizationID := localizationIDs[defaultStr(asset.Locale, doc.DefaultLocale)]
			if localizationID == "" {
				return nil, fmt.Errorf("asset %s references unknown locale %s", asset.ID, asset.Locale)
			}
			if asset.Kind == "app_preview" {
				err = a.uploadApplePreview(bound, d, localizationID, asset)
			} else {
				err = a.uploadAppleScreenshot(bound, d, localizationID, asset)
			}
		}
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"app_id": appID, "version_id": versionID,
		"localizations": localizationIDs, "assets": len(doc.Assets),
	}, nil
}

func ensureAppleStoreVersion(bound *sdk.BoundIntegration, appID string, doc StoreDocument) (string, error) {
	versions, err := executeIntegration(bound, "list_app_versions", map[string]any{
		"app_id": appID, "platform": "IOS", "limit": 50,
	})
	if err != nil {
		return "", err
	}
	var payload struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				VersionString string `json:"versionString"`
				State         string `json:"appStoreState"`
			} `json:"attributes"`
		} `json:"data"`
	}
	_ = json.Unmarshal(versions, &payload)
	for _, version := range payload.Data {
		if version.Attributes.VersionString == doc.VersionName {
			return version.ID, nil
		}
	}
	for _, version := range payload.Data {
		switch version.Attributes.State {
		case "PREPARE_FOR_SUBMISSION", "READY_FOR_REVIEW", "DEVELOPER_REJECTED", "METADATA_REJECTED", "REJECTED":
			return "", fmt.Errorf("editable App Store version %s already exists; binary/listing version %s must be aligned before Deploy creates another version", version.Attributes.VersionString, doc.VersionName)
		}
	}
	releaseType := map[string]string{
		"manual":         "MANUAL",
		"after_approval": "AFTER_APPROVAL",
		"scheduled":      "SCHEDULED",
	}[doc.ReleaseMode]
	if releaseType == "" {
		return "", fmt.Errorf("unsupported App Store release mode %q", doc.ReleaseMode)
	}
	created, err := executeIntegration(bound, "create_app_version", map[string]any{
		"app_id": appID, "platform": "IOS", "versionString": doc.VersionName,
		"releaseType": releaseType, "earliestReleaseDate": doc.EarliestReleaseAt,
		"copyright": doc.Copyright, "usesIdfa": doc.UsesIDFA,
	})
	if err != nil {
		return "", err
	}
	id := jsonStringAt(created, "data", "id")
	if id == "" {
		return "", errors.New("create App Store version response missing id")
	}
	return id, nil
}

func upsertAppleVersionLocalization(bound *sdk.BoundIntegration, versionID, locale string, loc StoreLocalization) (string, error) {
	listed, err := executeIntegration(bound, "list_version_localizations", map[string]any{
		"version_id": versionID, "locale": locale,
	})
	if err != nil {
		return "", err
	}
	id := firstJSONAPIID(listed)
	input := map[string]any{
		"description": loc.Description, "keywords": strings.Join(loc.Keywords, ","),
		"marketingUrl": loc.MarketingURL, "promotionalText": loc.PromotionalText,
		"supportUrl": loc.SupportURL, "whatsNew": loc.WhatsNew,
	}
	if id == "" {
		input["version_id"] = versionID
		input["locale"] = locale
		created, err := executeIntegration(bound, "create_version_localization", input)
		if err != nil {
			return "", err
		}
		id = jsonStringAt(created, "data", "id")
	} else {
		input["localization_id"] = id
		_, err = executeIntegration(bound, "update_version_localization", input)
	}
	return id, err
}

func (a *App) applyAppleReviewDetail(bound *sdk.BoundIntegration, versionID string, review StoreReview) (string, error) {
	current, err := executeIntegration(bound, "get_version_review_detail", map[string]any{"version_id": versionID})
	if err != nil {
		return "", err
	}
	id := jsonStringAt(current, "data", "id")
	input := map[string]any{
		"contactFirstName": review.FirstName, "contactLastName": review.LastName,
		"contactEmail": review.Email, "contactPhone": review.Phone,
		"notes": review.Notes, "demoAccountRequired": review.DemoAccountRequired,
		"demoAccountName": review.DemoUsername,
	}
	if review.DemoPassword != "" {
		input["demoAccountPassword"] = review.DemoPassword
	}
	if id == "" {
		input["version_id"] = versionID
		created, createErr := executeIntegration(bound, "create_version_review_detail", input)
		err = createErr
		id = jsonStringAt(created, "data", "id")
	} else {
		input["review_detail_id"] = id
		_, err = executeIntegration(bound, "update_version_review_detail", input)
	}
	return id, err
}

func (a *App) applyAppleMetadata(bound *sdk.BoundIntegration, appID string, doc StoreDocument) error {
	infos, err := executeIntegration(bound, "list_app_infos", map[string]any{"app_id": appID, "limit": 10})
	if err != nil {
		return err
	}
	infoID := firstJSONAPIID(infos)
	if infoID != "" && doc.Classification.PrimaryCategory != "" {
		input := map[string]any{
			"app_info_id": infoID, "primary_category_id": doc.Classification.PrimaryCategory,
		}
		if doc.Classification.SecondaryCategory != "" {
			input["secondary_category_id"] = doc.Classification.SecondaryCategory
		}
		if _, err := executeIntegration(bound, "update_app_info_categories", input); err != nil {
			return err
		}
	}
	if infoID != "" {
		for locale, loc := range doc.Localizations {
			listed, err := executeIntegration(bound, "list_app_info_localizations", map[string]any{"app_info_id": infoID, "locale": locale})
			if err != nil {
				return err
			}
			input := map[string]any{
				"name": loc.Title, "subtitle": loc.Subtitle,
				"privacyPolicyUrl": doc.Privacy.PolicyURL, "privacyChoicesUrl": doc.Privacy.ChoicesURL,
			}
			if id := firstJSONAPIID(listed); id != "" {
				input["localization_id"] = id
				_, err = executeIntegration(bound, "update_app_info_localization", input)
			} else {
				input["app_info_id"] = infoID
				input["locale"] = locale
				_, err = executeIntegration(bound, "create_app_info_localization", input)
			}
			if err != nil {
				return err
			}
		}
	}
	if body := providerExtensionBody(doc, "app_store_connect", "price_schedule_body"); body != nil {
		if _, err := executeIntegration(bound, "create_app_price_schedule", map[string]any{"body": body}); err != nil {
			return err
		}
	}
	if body := providerExtensionBody(doc, "app_store_connect", "availability_body"); body != nil {
		availabilityID := jsonStringFromValue(body, "data", "id")
		if availabilityID == "" {
			_, err = executeIntegration(bound, "create_app_availability", map[string]any{"body": body})
		} else {
			_, err = executeIntegration(bound, "update_app_availability", map[string]any{
				"availability_id": availabilityID, "body": body,
			})
		}
		if err != nil {
			return err
		}
	}
	if attributes := appleAgeDeclaration(doc.Classification); len(attributes) > 0 {
		if infoID == "" {
			return errors.New("App Store Connect has no App Info resource for age-rating declarations")
		}
		age, err := executeIntegration(bound, "get_app_age_rating_declaration", map[string]any{"app_info_id": infoID})
		if err != nil {
			return err
		}
		if id := jsonStringAt(age, "data", "id"); id != "" {
			_, err = executeIntegration(bound, "update_age_rating_declaration", map[string]any{
				"age_rating_id": id,
				"body": map[string]any{
					"data": map[string]any{
						"type": "ageRatingDeclarations", "id": id,
						"attributes": attributes,
					},
				},
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) uploadAppleScreenshot(bound *sdk.BoundIntegration, d *Deployment, localizationID string, asset StoreAsset) error {
	path, err := resolveStoreAssetPath(a.dataDir, d, asset.Path)
	if err != nil {
		return err
	}
	name := storeUploadFilename(path, asset.SHA256)
	target := appleScreenshotDisplayTarget(asset)
	sets, err := executeIntegration(bound, "list_screenshot_sets", map[string]any{
		"localization_id": localizationID, "display_type": target, "limit": 20,
	})
	if err != nil {
		return err
	}
	setID := firstJSONAPIID(sets)
	if setID == "" {
		created, err := executeIntegration(bound, "create_screenshot_set", map[string]any{
			"localization_id": localizationID, "screenshotDisplayType": target,
		})
		if err != nil {
			return err
		}
		setID = jsonStringAt(created, "data", "id")
	}
	existing, err := executeIntegration(bound, "list_screenshots", map[string]any{"set_id": setID, "limit": 200})
	if err != nil {
		return err
	}
	if jsonAPIHasFilename(existing, name) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	reserved, err := executeIntegration(bound, "reserve_screenshot", map[string]any{
		"set_id": setID, "fileName": name, "fileSize": info.Size(),
	})
	if err != nil {
		return err
	}
	id := jsonStringAt(reserved, "data", "id")
	if id == "" {
		return errors.New("screenshot reservation response missing id")
	}
	if err := uploadAppleAssetOperations(path, reserved); err != nil {
		return err
	}
	checksum, err := fileMD5(path)
	if err != nil {
		return err
	}
	_, err = executeIntegration(bound, "commit_screenshot", map[string]any{
		"screenshot_id": id, "uploaded": true, "sourceFileChecksum": checksum,
	})
	return err
}

func (a *App) uploadApplePreview(bound *sdk.BoundIntegration, d *Deployment, localizationID string, asset StoreAsset) error {
	path, err := resolveStoreAssetPath(a.dataDir, d, asset.Path)
	if err != nil {
		return err
	}
	name := storeUploadFilename(path, asset.SHA256)
	target := defaultStr(asset.DisplayTarget, "IPHONE_67")
	sets, err := executeIntegration(bound, "list_preview_sets", map[string]any{
		"localization_id": localizationID, "preview_type": target, "limit": 20,
	})
	if err != nil {
		return err
	}
	setID := firstJSONAPIID(sets)
	if setID == "" {
		created, err := executeIntegration(bound, "create_preview_set", map[string]any{
			"localization_id": localizationID, "previewType": target,
		})
		if err != nil {
			return err
		}
		setID = jsonStringAt(created, "data", "id")
	}
	existing, err := executeIntegration(bound, "list_previews", map[string]any{"set_id": setID, "limit": 50})
	if err != nil {
		return err
	}
	if jsonAPIHasFilename(existing, name) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	reserved, err := executeIntegration(bound, "reserve_preview", map[string]any{
		"set_id": setID, "fileName": name, "fileSize": info.Size(),
		"mimeType": defaultStr(mime.TypeByExtension(filepath.Ext(path)), "video/mp4"),
	})
	if err != nil {
		return err
	}
	id := jsonStringAt(reserved, "data", "id")
	if id == "" {
		return errors.New("app preview reservation response missing id")
	}
	if err := uploadAppleAssetOperations(path, reserved); err != nil {
		return err
	}
	checksum, err := fileMD5(path)
	if err != nil {
		return err
	}
	_, err = executeIntegration(bound, "commit_preview", map[string]any{
		"preview_id": id, "uploaded": true, "sourceFileChecksum": checksum,
	})
	return err
}

func (a *App) uploadAppleReviewAttachment(bound *sdk.BoundIntegration, d *Deployment, reviewDetailID string, asset StoreAsset) error {
	if reviewDetailID == "" {
		return errors.New("App Review detail response missing id")
	}
	path, err := resolveStoreAssetPath(a.dataDir, d, asset.Path)
	if err != nil {
		return err
	}
	name := storeUploadFilename(path, asset.SHA256)
	existing, err := executeIntegration(bound, "list_review_attachments", map[string]any{
		"review_detail_id": reviewDetailID, "limit": 50,
	})
	if err != nil {
		return err
	}
	if jsonAPIHasFilename(existing, name) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	reserved, err := executeIntegration(bound, "reserve_review_attachment", map[string]any{
		"review_detail_id": reviewDetailID, "fileName": name, "fileSize": info.Size(),
	})
	if err != nil {
		return err
	}
	id := jsonStringAt(reserved, "data", "id")
	if id == "" {
		return errors.New("review attachment reservation response missing id")
	}
	if err := uploadAppleAssetOperations(path, reserved); err != nil {
		return err
	}
	checksum, err := fileMD5(path)
	if err != nil {
		return err
	}
	_, err = executeIntegration(bound, "commit_review_attachment", map[string]any{
		"attachment_id": id, "uploaded": true, "sourceFileChecksum": checksum,
	})
	return err
}

func uploadAppleAssetOperations(path string, raw json.RawMessage) error {
	var payload struct {
		Data struct {
			Attributes struct {
				UploadOperations []struct {
					Method         string `json:"method"`
					URL            string `json:"url"`
					Length         int64  `json:"length"`
					Offset         int64  `json:"offset"`
					RequestHeaders []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"requestHeaders"`
				} `json:"uploadOperations"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	if len(payload.Data.Attributes.UploadOperations) == 0 {
		return errors.New("Apple asset reservation returned no upload operations")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	client := &http.Client{Timeout: 10 * time.Minute}
	for _, operation := range payload.Data.Attributes.UploadOperations {
		section := io.NewSectionReader(file, operation.Offset, operation.Length)
		req, err := http.NewRequest(defaultStr(operation.Method, http.MethodPut), operation.URL, section)
		if err != nil {
			return err
		}
		req.ContentLength = operation.Length
		for _, header := range operation.RequestHeaders {
			req.Header.Set(header.Name, header.Value)
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("Apple asset upload returned HTTP %d: %s", resp.StatusCode, truncateString(string(body), 800))
		}
	}
	return nil
}

func (a *App) applyGoogleStoreConfig(d *Deployment, doc StoreDocument) (map[string]any, error) {
	bound, err := boundIntegration("play_store")
	if err != nil {
		return nil, err
	}
	target, err := parseMobileTargetConfig(d.TargetConfigJSON)
	if err != nil {
		return nil, err
	}
	if target.PackageName == "" {
		return nil, errors.New("Google Play package_name is required")
	}
	created, err := executeIntegration(bound, "create_edit", map[string]any{"packageName": target.PackageName})
	if err != nil {
		return nil, err
	}
	editID := jsonStringAt(created, "id")
	if editID == "" {
		return nil, errors.New("Google Play create_edit response missing id")
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = executeIntegration(bound, "delete_edit", map[string]any{"packageName": target.PackageName, "editId": editID})
		}
	}()
	if err := a.applyGoogleStoreConfigToEdit(bound, d, target.PackageName, editID, doc); err != nil {
		return nil, err
	}
	if _, err := executeIntegration(bound, "validate_edit", map[string]any{"packageName": target.PackageName, "editId": editID}); err != nil {
		return nil, fmt.Errorf("validate Play edit: %w", err)
	}
	if _, err := executeIntegration(bound, "commit_edit", map[string]any{"packageName": target.PackageName, "editId": editID}); err != nil {
		return nil, fmt.Errorf("commit Play edit: %w", err)
	}
	committed = true
	if doc.Privacy.DataSafetyCSV != "" {
		if _, err := executeIntegration(bound, "update_data_safety", map[string]any{
			"packageName": target.PackageName, "safetyLabels": doc.Privacy.DataSafetyCSV,
		}); err != nil {
			return nil, err
		}
	}
	return map[string]any{"package_name": target.PackageName, "edit_id": editID, "assets": len(doc.Assets)}, nil
}

func (a *App) applyGoogleStoreConfigToEdit(bound *sdk.BoundIntegration, d *Deployment, packageName, editID string, doc StoreDocument) error {
	defaultLocale := defaultStr(doc.DefaultLocale, "en-US")
	defaultLoc := doc.Localizations[defaultLocale]
	if _, err := executeIntegration(bound, "update_app_details", map[string]any{
		"packageName": packageName, "editId": editID, "defaultLanguage": defaultLocale,
		"contactWebsite": firstNonEmpty(defaultLoc.SupportURL, doc.Privacy.PolicyURL),
		"contactEmail":   doc.Review.Email, "contactPhone": doc.Review.Phone,
	}); err != nil {
		return err
	}
	for locale, loc := range doc.Localizations {
		if _, err := executeIntegration(bound, "update_store_listing", map[string]any{
			"packageName": packageName, "editId": editID, "language": locale,
			"title": loc.Title, "shortDescription": loc.ShortDescription,
			"fullDescription": loc.Description, "video": loc.VideoURL,
		}); err != nil {
			return err
		}
	}
	grouped := map[string][]StoreAsset{}
	for _, asset := range doc.Assets {
		imageType := googleImageType(asset)
		if imageType == "" {
			continue
		}
		key := defaultStr(asset.Locale, defaultLocale) + "\x00" + imageType
		grouped[key] = append(grouped[key], asset)
	}
	for key, assets := range grouped {
		parts := strings.SplitN(key, "\x00", 2)
		if _, err := executeIntegration(bound, "delete_all_listing_images", map[string]any{
			"packageName": packageName, "editId": editID, "language": parts[0], "imageType": parts[1],
		}); err != nil {
			return err
		}
		sort.SliceStable(assets, func(i, j int) bool { return assets[i].Order < assets[j].Order })
		for _, asset := range assets {
			path, err := resolveStoreAssetPath(a.dataDir, d, asset.Path)
			if err != nil {
				return err
			}
			if _, err := uploadGooglePlayImage(bound, packageName, editID, parts[0], parts[1], path); err != nil {
				return err
			}
		}
	}
	return nil
}

func uploadGooglePlayImage(bound *sdk.BoundIntegration, packageName, editID, locale, imageType, path string) (json.RawMessage, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(googlePlayUploadBaseURL, "/") +
		"/applications/" + urlPathEscape(packageName) +
		"/edits/" + urlPathEscape(editID) +
		"/listings/" + urlPathEscape(locale) + "/" + urlPathEscape(imageType) + "?uploadType=media"
	for attempt := 0; attempt < 2; attempt++ {
		creds, err := globalCtx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
		if err != nil {
			return nil, err
		}
		token := firstNonEmpty(creds.Fields["token"], creds.Fields["access_token"], creds.Fields["bearer_token"])
		if token == "" {
			return nil, errors.New("Google Play connection has no OAuth access token")
		}
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", defaultStr(mime.TypeByExtension(filepath.Ext(path)), "application/octet-stream"))
		resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
		if err != nil {
			return nil, err
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			_, _ = executeIntegration(bound, "get_edit", map[string]any{"packageName": packageName, "editId": editID})
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("Google Play image upload returned HTTP %d: %s", resp.StatusCode, truncateString(string(raw), 800))
		}
		return raw, nil
	}
	return nil, errors.New("Google Play image upload authorization failed after refresh")
}

func googleImageType(asset StoreAsset) string {
	switch asset.Kind {
	case "icon":
		return "icon"
	case "feature_graphic":
		return "featureGraphic"
	case "phone_screenshot":
		return "phoneScreenshots"
	case "tablet_screenshot":
		if asset.DisplayTarget == "tablet_10" {
			return "tenInchScreenshots"
		}
		return "sevenInchScreenshots"
	case "tv_screenshot":
		return "tvScreenshots"
	case "wear_screenshot":
		return "wearScreenshots"
	}
	return ""
}

func (a *App) saveStoreAsset(d *Deployment, filename string, src io.Reader) (StoreAsset, error) {
	filename = filepath.Base(strings.TrimSpace(filename))
	if filename == "" || filename == "." {
		return StoreAsset{}, errors.New("asset filename required")
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	rel := filepath.Join("store-assets", strconv.FormatInt(d.ID, 10), strconv.FormatInt(d.EnvironmentID, 10), id, filename)
	abs := filepath.Join(a.dataDir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return StoreAsset{}, err
	}
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return StoreAsset{}, err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(src, 100<<20))
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.RemoveAll(filepath.Dir(abs))
		return StoreAsset{}, copyErr
	}
	if closeErr != nil {
		return StoreAsset{}, closeErr
	}
	if n >= 100<<20 {
		_ = os.RemoveAll(filepath.Dir(abs))
		return StoreAsset{}, errors.New("asset exceeds 100 MB")
	}
	asset := StoreAsset{ID: id, Path: filepath.ToSlash(rel), SHA256: hex.EncodeToString(hash.Sum(nil))}
	if metadata, inspectErr := inspectStoreAssetFile(abs); inspectErr == nil {
		asset.MIME, asset.Width, asset.Height = metadata.MIME, metadata.Width, metadata.Height
	}
	return asset, nil
}

func (a *App) pruneUnreferencedStoreAssets(d *Deployment, doc StoreDocument) {
	if d == nil {
		return
	}
	root := filepath.Join(a.dataDir, "store-assets", strconv.FormatInt(d.ID, 10), strconv.FormatInt(d.EnvironmentID, 10))
	referenced := map[string]bool{}
	for _, asset := range doc.Assets {
		if path, err := resolveStoreAssetPath(a.dataDir, d, asset.Path); err == nil {
			referenced[filepath.Clean(path)] = true
		}
	}
	dirs := []string{}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		if !referenced[filepath.Clean(path)] {
			_ = os.Remove(path)
		}
		return nil
	})
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		if dir != root {
			_ = os.Remove(dir)
		}
	}
}

func resolveStoreAssetPath(dataDir string, d *Deployment, rel string) (string, error) {
	rel = filepath.Clean(filepath.FromSlash(strings.TrimSpace(rel)))
	if rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid store asset path %q", rel)
	}
	prefix := filepath.Join("store-assets", strconv.FormatInt(d.ID, 10), strconv.FormatInt(d.EnvironmentID, 10)) + string(filepath.Separator)
	if !strings.HasPrefix(rel, prefix) {
		return "", fmt.Errorf("store asset path is outside this deployment environment")
	}
	abs := filepath.Join(dataDir, rel)
	if info, err := os.Stat(abs); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("store asset is unavailable: %s", filepath.ToSlash(rel))
	}
	return abs, nil
}

func fileMD5(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func storeUploadFilename(path, sum string) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), ext)
	if len(sum) > 12 {
		sum = sum[:12]
	}
	return base + "-" + sum + ext
}

func jsonAPIHasFilename(raw json.RawMessage, name string) bool {
	var payload struct {
		Data []struct {
			Attributes struct {
				FileName string `json:"fileName"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return false
	}
	for _, item := range payload.Data {
		if item.Attributes.FileName == name {
			return true
		}
	}
	return false
}

func urlPathEscape(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "/", "%2F"), " ", "%20")
}

func (a *App) releaseApprovedMobileVersion(rel *Release) (*Release, error) {
	if rel == nil || rel.Provider != "app_store_connect" {
		return nil, errors.New("approved release must be an App Store release")
	}
	var meta mobileReleaseMeta
	if err := json.Unmarshal([]byte(defaultStr(rel.ReleaseMetaJSON, "{}")), &meta); err != nil {
		return nil, err
	}
	if meta.AppStoreVersionID == "" {
		return nil, errors.New("release has no App Store version id")
	}
	if rel.ExternalStatus != "pending_apple_release" && rel.ExternalStatus != "approved_pending_release" {
		return nil, fmt.Errorf("App Store version is not waiting for manual release (state=%s)", rel.ExternalStatus)
	}
	bound, err := boundIntegration("app_store")
	if err != nil {
		return nil, err
	}
	if _, err := executeIntegration(bound, "request_version_release", map[string]any{"version_id": meta.AppStoreVersionID}); err != nil {
		return nil, err
	}
	_ = dbUpdateRelease(globalCtx.AppDB(), rel.ID, map[string]any{"external_status": "release_requested"})
	_ = dbAppendReleaseEvent(globalCtx.AppDB(), rel.ID, "manual_release_requested", mustJSON(map[string]any{"version_id": meta.AppStoreVersionID}))
	return dbGetRelease(globalCtx.AppDB(), rel.ID)
}
