package sdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// NativeSurfaceMaxBytes bounds declarative surface documents before decoding.
// Surfaces are UI descriptions, not data payloads; a larger document is almost
// certainly accidental or hostile.
const NativeSurfaceMaxBytes = 256 << 10

const NativeSurfaceContextProject = "project"

var (
	nativeSurfaceIDPattern       = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	nativeSurfaceVersionPattern  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	nativeSurfaceSelectorPattern = regexp.MustCompile(`^\$(?:\.[A-Za-z_][A-Za-z0-9_]*)*$`)
	nativeSurfaceBindingPattern  = regexp.MustCompile(`\{(context|settings|state|item|input|result)(?:\.([A-Za-z_][A-Za-z0-9_]*))?(?:\.[A-Za-z_][A-Za-z0-9_]*)*\}`)
	nativeSurfaceDirectBinding   = regexp.MustCompile(`^\$(context|settings|state|item|input|result)(?:\.[A-Za-z_][A-Za-z0-9_]*)+$`)
)

// NativeSurface is the canonical, code-free UI contract consumed by native
// Apteva clients. It describes where data comes from and how native components
// present it; it never carries executable code, credentials, or records.
type NativeSurface struct {
	Schema         string                              `json:"schema"`
	ID             string                              `json:"id"`
	Version        string                              `json:"version"`
	Title          string                              `json:"title"`
	Description    string                              `json:"description,omitempty"`
	Icon           string                              `json:"icon"`
	Context        NativeSurfaceContext                `json:"context"`
	State          map[string]NativeSurfaceState       `json:"state,omitempty"`
	DataSources    map[string]NativeSurfaceDataSource  `json:"data_sources,omitempty"`
	PrimaryActions []string                            `json:"primary_actions,omitempty"`
	Sections       []NativeSurfaceSection              `json:"sections"`
	Destinations   map[string]NativeSurfaceDestination `json:"destinations,omitempty"`
	Actions        map[string]NativeSurfaceAction      `json:"actions,omitempty"`
	Resources      map[string]NativeSurfaceResource    `json:"resources,omitempty"`
	Presentation   string                              `json:"presentation,omitempty"`
	Blocks         []NativeSurfaceBlock                `json:"blocks,omitempty"`
	Navigation     *NativeSurfaceNavigation            `json:"navigation,omitempty"`
}

// NativeSurfaceNavigation lets a compact dashboard widget open one of the
// declaring app's separately advertised mobile.project_app surfaces.
type NativeSurfaceNavigation struct {
	Surface string `json:"surface"`
}

// NativeSurfaceBlock is the deliberately small dashboard-widget vocabulary.
// Values are selectors into the named source response (or literal text).
type NativeSurfaceBlock struct {
	ID        string                      `json:"id"`
	Type      string                      `json:"type"`
	Title     string                      `json:"title,omitempty"`
	Label     string                      `json:"label,omitempty"`
	Source    string                      `json:"source,omitempty"`
	Value     string                      `json:"value,omitempty"`
	Text      string                      `json:"text,omitempty"`
	Format    string                      `json:"format,omitempty"`
	Icon      string                      `json:"icon,omitempty"`
	Items     string                      `json:"items,omitempty"`
	Item      *NativeSurfaceItemMapping   `json:"item,omitempty"`
	Metrics   []NativeSurfaceBlockMetric  `json:"metrics,omitempty"`
	Limit     int                         `json:"limit,omitempty"`
	Action    string                      `json:"action,omitempty"`
	Empty     *NativeSurfaceSemanticState `json:"empty,omitempty"`
	VisibleIf string                      `json:"visible_if,omitempty"`
}

type NativeSurfaceBlockMetric struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Format string `json:"format,omitempty"`
	Icon   string `json:"icon,omitempty"`
}

type NativeSurfaceContext struct {
	Scope string `json:"scope"`
}

type NativeSurfaceState struct {
	Type        string          `json:"type"`
	Default     json.RawMessage `json:"default,omitempty"`
	Persistence string          `json:"persistence,omitempty"`
}

type NativeSurfaceDataSource struct {
	Request    NativeSurfaceRequest     `json:"request"`
	Response   NativeSurfaceResponse    `json:"response"`
	Pagination *NativeSurfacePagination `json:"pagination,omitempty"`
	Refresh    NativeSurfaceRefresh     `json:"refresh,omitempty"`
}

type NativeSurfaceRequest struct {
	Method   string         `json:"method"`
	Path     string         `json:"path"`
	Encoding string         `json:"encoding,omitempty"`
	Query    map[string]any `json:"query,omitempty"`
	Body     map[string]any `json:"body,omitempty"`
}

type NativeSurfaceResponse struct {
	Items      string `json:"items,omitempty"`
	Item       string `json:"item,omitempty"`
	ID         string `json:"id,omitempty"`
	Result     string `json:"result,omitempty"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type NativeSurfacePagination struct {
	Type         string `json:"type"`
	RequestKey   string `json:"request_key,omitempty"`
	ResponsePath string `json:"response_path,omitempty"`
	PageSize     int    `json:"page_size,omitempty"`
}

type NativeSurfaceRefresh struct {
	OnAppear      bool `json:"on_appear,omitempty"`
	PullToRefresh bool `json:"pull_to_refresh,omitempty"`
}

type NativeSurfaceSection struct {
	ID           string                        `json:"id"`
	Kind         string                        `json:"kind"`
	Title        string                        `json:"title,omitempty"`
	Description  string                        `json:"description,omitempty"`
	Source       string                        `json:"source,omitempty"`
	Presentation string                        `json:"presentation,omitempty"`
	Search       *NativeSurfaceSearch          `json:"search,omitempty"`
	Filters      []NativeSurfaceFilter         `json:"filters,omitempty"`
	Item         *NativeSurfaceItemMapping     `json:"item,omitempty"`
	Metrics      []NativeSurfaceMetricMapping  `json:"metrics,omitempty"`
	Timeline     *NativeSurfaceTimelineMapping `json:"timeline,omitempty"`
	Fields       []NativeSurfaceField          `json:"fields,omitempty"`
	Preview      *NativeSurfacePreview         `json:"preview,omitempty"`
	Actions      []string                      `json:"actions,omitempty"`
	Empty        *NativeSurfaceSemanticState   `json:"empty,omitempty"`
	Loading      *NativeSurfaceSemanticState   `json:"loading,omitempty"`
	Error        *NativeSurfaceSemanticState   `json:"error,omitempty"`
	Component    string                        `json:"component,omitempty"`
	Chat         *NativeSurfaceChatComponent   `json:"chat,omitempty"`
}

// NativeSurfaceChatComponent is a portable chat experience. The host owns the
// native interaction model while the declaring app owns every route, mapping,
// action, and stream event name.
type NativeSurfaceChatComponent struct {
	ConversationsSource string                           `json:"conversations_source"`
	MessagesSource      string                           `json:"messages_source"`
	SelectionState      string                           `json:"selection_state"`
	SendAction          string                           `json:"send_action"`
	CreateAction        string                           `json:"create_action,omitempty"`
	MarkSeenAction      string                           `json:"mark_seen_action,omitempty"`
	Conversation        NativeSurfaceConversationMapping `json:"conversation"`
	Message             NativeSurfaceMessageMapping      `json:"message"`
	Subscription        *NativeSurfaceSubscription       `json:"subscription,omitempty"`
	Live                *NativeSurfaceChatLiveMapping    `json:"live,omitempty"`
}

// NativeSurfaceChatLiveMapping maps ephemeral response, progress, and tool
// activity frames without coupling native hosts to an app's payload shape.
type NativeSurfaceChatLiveMapping struct {
	ID       string                            `json:"id,omitempty"`
	AgentID  string                            `json:"agent_id,omitempty"`
	ThreadID string                            `json:"thread_id,omitempty"`
	RunID    string                            `json:"run_id,omitempty"`
	Text     string                            `json:"text,omitempty"`
	Phase    string                            `json:"phase,omitempty"`
	Done     string                            `json:"done,omitempty"`
	Progress *NativeSurfaceChatProgressMapping `json:"progress,omitempty"`
	Activity *NativeSurfaceChatActivityMapping `json:"activity,omitempty"`
}

type NativeSurfaceChatProgressMapping struct {
	Value          string `json:"value,omitempty"`
	Phase          string `json:"phase,omitempty"`
	Revision       string `json:"revision,omitempty"`
	ToolName       string `json:"tool_name,omitempty"`
	CallID         string `json:"call_id,omitempty"`
	AfterMessageID string `json:"after_message_id,omitempty"`
}

type NativeSurfaceChatActivityMapping struct {
	Value      string `json:"value,omitempty"`
	ID         string `json:"id,omitempty"`
	Revision   string `json:"revision,omitempty"`
	Name       string `json:"name,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Status     string `json:"status,omitempty"`
	DurationMS string `json:"duration_ms,omitempty"`
}

type NativeSurfaceConversationMapping struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Subtitle  string `json:"subtitle,omitempty"`
	Preview   string `json:"preview,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	Unread    string `json:"unread,omitempty"`
}

type NativeSurfaceMessageMapping struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	Body           string `json:"body"`
	Author         string `json:"author,omitempty"`
	Role           string `json:"role,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	Status         string `json:"status,omitempty"`
}

type NativeSurfaceSubscription struct {
	Request     NativeSurfaceRequest                 `json:"request"`
	CursorQuery string                               `json:"cursor_query,omitempty"`
	Events      map[string]NativeSurfaceEventMapping `json:"events"`
}

type NativeSurfaceEventMapping struct {
	Operation string `json:"operation"`
	Source    string `json:"source"`
	Value     string `json:"value,omitempty"`
	ID        string `json:"id,omitempty"`
}

type NativeSurfaceSearch struct {
	State       string `json:"state"`
	Placeholder string `json:"placeholder,omitempty"`
	QueryKey    string `json:"query_key,omitempty"`
}

type NativeSurfaceFilter struct {
	ID       string                      `json:"id"`
	Type     string                      `json:"type"`
	State    string                      `json:"state"`
	Label    string                      `json:"label"`
	QueryKey string                      `json:"query_key,omitempty"`
	Options  []NativeSurfaceOption       `json:"options,omitempty"`
	Source   string                      `json:"source,omitempty"`
	Mapping  *NativeSurfaceOptionMapping `json:"mapping,omitempty"`
}

type NativeSurfaceOption struct {
	Label string `json:"label"`
	Value any    `json:"value"`
	Match string `json:"match,omitempty"`
}

type NativeSurfaceOptionMapping struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type NativeSurfaceItemMapping struct {
	ID          string                `json:"id"`
	Title       string                `json:"title"`
	Subtitle    []NativeSurfaceValue  `json:"subtitle,omitempty"`
	Icon        *NativeSurfaceValue   `json:"icon,omitempty"`
	Badge       *NativeSurfaceValue   `json:"badge,omitempty"`
	Preview     *NativeSurfacePreview `json:"preview,omitempty"`
	Destination string                `json:"destination,omitempty"`
	Actions     []string              `json:"actions,omitempty"`
}

type NativeSurfaceMetricMapping struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Value  string `json:"value"`
	Hint   string `json:"hint,omitempty"`
	Icon   string `json:"icon,omitempty"`
	Format string `json:"format,omitempty"`
}

type NativeSurfaceTimelineMapping struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	Time   string `json:"time,omitempty"`
	Icon   string `json:"icon,omitempty"`
}

type NativeSurfaceValue struct {
	Value    string `json:"value"`
	Label    string `json:"label,omitempty"`
	Format   string `json:"format,omitempty"`
	Fallback string `json:"fallback,omitempty"`
}

type NativeSurfaceField struct {
	ID     string `json:"id,omitempty"`
	Label  string `json:"label"`
	Value  string `json:"value"`
	Format string `json:"format,omitempty"`
	Hidden bool   `json:"hidden,omitempty"`
}

type NativeSurfacePreview struct {
	Path         string `json:"path"`
	Name         string `json:"name,omitempty"`
	ContentType  string `json:"content_type,omitempty"`
	ShareAction  string `json:"share_action,omitempty"`
	DeleteAction string `json:"delete_action,omitempty"`
}

type NativeSurfaceSemanticState struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Retry       bool   `json:"retry,omitempty"`
}

type NativeSurfaceDestination struct {
	Type     string                 `json:"type"`
	Title    string                 `json:"title"`
	Source   string                 `json:"source,omitempty"`
	Sections []NativeSurfaceSection `json:"sections"`
	Actions  []string               `json:"actions,omitempty"`
}

type NativeSurfaceAction struct {
	Type         string                     `json:"type"`
	Label        string                     `json:"label"`
	Icon         string                     `json:"icon,omitempty"`
	Placements   []string                   `json:"placements,omitempty"`
	Destructive  bool                       `json:"destructive,omitempty"`
	Request      *NativeSurfaceRequest      `json:"request,omitempty"`
	Fields       []NativeSurfaceInput       `json:"fields,omitempty"`
	Confirmation *NativeSurfaceConfirmation `json:"confirmation,omitempty"`
	Result       string                     `json:"result,omitempty"`
	Success      *NativeSurfaceSuccess      `json:"success,omitempty"`
}

type NativeSurfaceInput struct {
	Name        string                `json:"name"`
	Type        string                `json:"type"`
	Label       string                `json:"label,omitempty"`
	Placeholder string                `json:"placeholder,omitempty"`
	Required    bool                  `json:"required,omitempty"`
	Default     json.RawMessage       `json:"default,omitempty"`
	Options     []NativeSurfaceOption `json:"options,omitempty"`
	Resource    string                `json:"resource,omitempty"`
}

type NativeSurfaceConfirmation struct {
	Title        string `json:"title"`
	Message      string `json:"message,omitempty"`
	ConfirmLabel string `json:"confirm_label,omitempty"`
}

type NativeSurfaceSuccess struct {
	Refresh []string `json:"refresh,omitempty"`
	Dismiss bool     `json:"dismiss,omitempty"`
	Toast   string   `json:"toast,omitempty"`
}

type NativeSurfaceResource struct {
	Type    string                       `json:"type"`
	Label   string                       `json:"label"`
	Source  string                       `json:"source"`
	Mapping NativeSurfaceResourceMapping `json:"mapping"`
}

type NativeSurfaceResourceMapping struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Parent string `json:"parent,omitempty"`
}

// ParseNativeSurface decodes one strict JSON surface document. Unknown fields,
// trailing JSON, over-sized documents, and invalid contracts fail closed.
func ParseNativeSurface(data []byte) (*NativeSurface, error) {
	if len(data) > NativeSurfaceMaxBytes {
		return nil, fmt.Errorf("native surface exceeds %d bytes", NativeSurfaceMaxBytes)
	}
	var surface NativeSurface
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&surface); err != nil {
		return nil, fmt.Errorf("parse native surface json: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("parse native surface json: trailing value")
		}
		return nil, fmt.Errorf("parse native surface json: trailing data: %w", err)
	}
	if err := ValidateNativeSurface(&surface); err != nil {
		return nil, err
	}
	return &surface, nil
}

// ValidateNativeSurface validates all local references and the v1 security
// boundary. Hosts still enforce authentication, project scope, and app routing.
func ValidateNativeSurface(surface *NativeSurface) error {
	if surface == nil {
		return errors.New("native surface required")
	}
	if surface.Schema != NativeSurfaceSchemaCurrent {
		return fmt.Errorf("native surface schema %q unsupported (expected %s)", surface.Schema, NativeSurfaceSchemaCurrent)
	}
	if !isSlug(surface.ID) {
		return errors.New("native surface id must be a lowercase slug (a-z0-9-)")
	}
	if !nativeSurfaceVersionPattern.MatchString(surface.Version) {
		return errors.New("native surface version must be semver")
	}
	if strings.TrimSpace(surface.Title) == "" || strings.TrimSpace(surface.Icon) == "" {
		return errors.New("native surface title and icon required")
	}
	if surface.Context.Scope != NativeSurfaceContextProject {
		return fmt.Errorf("native surface context.scope %q unsupported (expected project)", surface.Context.Scope)
	}
	switch surface.Presentation {
	case "", "page":
		if len(surface.Sections) == 0 {
			return errors.New("native page surface requires at least one section")
		}
	case "widget":
		if len(surface.Blocks) == 0 {
			return errors.New("native widget surface requires at least one block")
		}
	default:
		return fmt.Errorf("native surface presentation %q unsupported", surface.Presentation)
	}

	for name, state := range surface.State {
		if !nativeSurfaceIDPattern.MatchString(name) {
			return fmt.Errorf("state %q must be a lowercase identifier", name)
		}
		if err := validateNativeState(name, state); err != nil {
			return err
		}
	}
	for name, source := range surface.DataSources {
		if !nativeSurfaceIDPattern.MatchString(name) {
			return fmt.Errorf("data source %q must be a lowercase identifier", name)
		}
		if err := validateNativeDataSource(name, source, surface.State); err != nil {
			return err
		}
	}

	sectionIDs := map[string]bool{}
	for i, section := range surface.Sections {
		if err := validateNativeSection(section, fmt.Sprintf("sections[%d]", i), surface, sectionIDs); err != nil {
			return err
		}
		sectionIDs[section.ID] = true
	}
	blockIDs := map[string]bool{}
	for i, block := range surface.Blocks {
		if err := validateNativeBlock(block, fmt.Sprintf("blocks[%d]", i), surface, blockIDs); err != nil {
			return err
		}
		blockIDs[block.ID] = true
	}
	if surface.Navigation != nil {
		if surface.Presentation != "widget" || !isSlug(surface.Navigation.Surface) {
			return errors.New("native surface navigation requires a widget presentation and a surface slug")
		}
	}
	for name, destination := range surface.Destinations {
		if !nativeSurfaceIDPattern.MatchString(name) {
			return fmt.Errorf("destination %q must be a lowercase identifier", name)
		}
		if destination.Type != "detail" {
			return fmt.Errorf("destination %q type must be detail", name)
		}
		if strings.TrimSpace(destination.Title) == "" || len(destination.Sections) == 0 {
			return fmt.Errorf("destination %q requires title and sections", name)
		}
		if destination.Source != "" && surface.DataSources[destination.Source].Request.Path == "" {
			return fmt.Errorf("destination %q references unknown source %q", name, destination.Source)
		}
		if destination.Source != "" && surface.DataSources[destination.Source].Response.Item == "" {
			return fmt.Errorf("destination %q source %q requires an item response mapping", name, destination.Source)
		}
		if err := validateNativeBindings(destination.Title, surface.State); err != nil {
			return fmt.Errorf("destination %q title: %w", name, err)
		}
		localIDs := map[string]bool{}
		for i, section := range destination.Sections {
			if err := validateNativeSection(section, fmt.Sprintf("destinations.%s.sections[%d]", name, i), surface, localIDs); err != nil {
				return err
			}
			localIDs[section.ID] = true
		}
		if err := validateNativeActionRefs(destination.Actions, "destination "+name, surface.Actions); err != nil {
			return err
		}
	}
	for name, action := range surface.Actions {
		if !nativeSurfaceIDPattern.MatchString(name) {
			return fmt.Errorf("action %q must be a lowercase identifier", name)
		}
		if err := validateNativeAction(name, action, surface, sectionIDs); err != nil {
			return err
		}
	}
	if err := validateNativeActionRefs(surface.PrimaryActions, "primary_actions", surface.Actions); err != nil {
		return err
	}
	for name, resource := range surface.Resources {
		if !nativeSurfaceIDPattern.MatchString(name) {
			return fmt.Errorf("resource %q must be a lowercase identifier", name)
		}
		if resource.Type != "tree" && resource.Type != "list" {
			return fmt.Errorf("resource %q type must be tree or list", name)
		}
		if strings.TrimSpace(resource.Label) == "" || surface.DataSources[resource.Source].Request.Path == "" {
			return fmt.Errorf("resource %q requires label and a known source", name)
		}
		if !validSelector(resource.Mapping.ID) || !validSelector(resource.Mapping.Label) ||
			(resource.Mapping.Parent != "" && !validSelector(resource.Mapping.Parent)) {
			return fmt.Errorf("resource %q has an invalid response mapping", name)
		}
	}
	return nil
}

func validateNativeBlock(block NativeSurfaceBlock, prefix string, surface *NativeSurface, seen map[string]bool) error {
	if !nativeSurfaceIDPattern.MatchString(block.ID) || seen[block.ID] {
		return fmt.Errorf("%s has invalid or duplicate id %q", prefix, block.ID)
	}
	switch block.Type {
	case "metric", "metrics", "text", "status", "progress", "list", "empty_state", "divider", "action":
	default:
		return fmt.Errorf("%s type %q unsupported", prefix, block.Type)
	}
	if block.Source != "" && surface.DataSources[block.Source].Request.Path == "" {
		return fmt.Errorf("%s references unknown source %q", prefix, block.Source)
	}
	if block.Limit < 0 || block.Limit > 20 {
		return fmt.Errorf("%s limit must be between 1 and 20", prefix)
	}
	if block.VisibleIf != "" && !regexp.MustCompile(`^\$settings\.[A-Za-z_][A-Za-z0-9_]*$`).MatchString(block.VisibleIf) {
		return fmt.Errorf("%s visible_if must reference one boolean widget setting", prefix)
	}
	for _, value := range []string{block.Value, block.Items} {
		if value != "" && !validSelector(value) {
			return fmt.Errorf("%s has invalid selector %q", prefix, value)
		}
	}
	if block.Type == "metric" && (block.Source == "" || block.Value == "") {
		return fmt.Errorf("%s metric requires source and value", prefix)
	}
	if block.Type == "metrics" {
		if block.Source == "" || len(block.Metrics) == 0 {
			return fmt.Errorf("%s metrics requires source and mappings", prefix)
		}
		for i, metric := range block.Metrics {
			if strings.TrimSpace(metric.Label) == "" || !validSelector(metric.Value) {
				return fmt.Errorf("%s metrics[%d] requires label and value selector", prefix, i)
			}
		}
	}
	if block.Type == "list" {
		if block.Source == "" || block.Items == "" || block.Item == nil || block.Item.ID == "" || block.Item.Title == "" {
			return fmt.Errorf("%s list requires source, items, and item mapping", prefix)
		}
	}
	if block.Type == "action" {
		if block.Action == "" || surface.Actions[block.Action].Type == "" {
			return fmt.Errorf("%s action references an unknown action", prefix)
		}
	}
	return nil
}

// ValidateNativeSurfaceForDescriptor additionally pins a downloaded document
// to the manifest descriptor that advertised it.
func ValidateNativeSurfaceForDescriptor(surface *NativeSurface, descriptor UISurface) error {
	if err := ValidateNativeSurface(surface); err != nil {
		return err
	}
	if descriptor.Schema != surface.Schema {
		return fmt.Errorf("surface schema %q does not match descriptor %q", surface.Schema, descriptor.Schema)
	}
	if descriptor.ID != surface.ID {
		return fmt.Errorf("surface id %q does not match descriptor %q", surface.ID, descriptor.ID)
	}
	return nil
}

func validateNativeState(name string, state NativeSurfaceState) error {
	switch state.Type {
	case "string", "boolean", "integer", "number", "string_list":
	default:
		return fmt.Errorf("state %q has unsupported type %q", name, state.Type)
	}
	switch state.Persistence {
	case "", "session", "project":
	default:
		return fmt.Errorf("state %q has unsupported persistence %q", name, state.Persistence)
	}
	if len(state.Default) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(state.Default, &value); err != nil {
		return fmt.Errorf("state %q default: %w", name, err)
	}
	if !nativeValueMatchesType(value, state.Type) {
		return fmt.Errorf("state %q default does not match type %s", name, state.Type)
	}
	return nil
}

func nativeValueMatchesType(value any, typ string) bool {
	switch typ {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer":
		v, ok := value.(float64)
		return ok && v == float64(int64(v))
	case "number":
		_, ok := value.(float64)
		return ok
	case "string_list":
		values, ok := value.([]any)
		if !ok {
			return false
		}
		for _, value := range values {
			if _, ok := value.(string); !ok {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func validateNativeDataSource(name string, source NativeSurfaceDataSource, state map[string]NativeSurfaceState) error {
	if err := validateNativeRequest(source.Request, "data source "+name, state); err != nil {
		return err
	}
	selectors := []string{source.Response.Items, source.Response.Item, source.Response.ID, source.Response.Result, source.Response.NextCursor}
	if source.Response.Items == "" && source.Response.Item == "" && source.Response.Result == "" {
		return fmt.Errorf("data source %q response requires items, item, or result", name)
	}
	for _, selector := range selectors {
		if selector != "" && !validSelector(selector) {
			return fmt.Errorf("data source %q has invalid response selector %q", name, selector)
		}
	}
	if source.Pagination != nil {
		pagination := source.Pagination
		switch pagination.Type {
		case "none":
		case "cursor":
			if pagination.RequestKey == "" || pagination.ResponsePath == "" || !validSelector(pagination.ResponsePath) {
				return fmt.Errorf("data source %q cursor pagination requires request_key and response_path", name)
			}
		case "page":
			if pagination.RequestKey == "" {
				return fmt.Errorf("data source %q page pagination requires request_key", name)
			}
		default:
			return fmt.Errorf("data source %q pagination type %q unsupported", name, pagination.Type)
		}
		if pagination.PageSize < 0 || pagination.PageSize > 200 {
			return fmt.Errorf("data source %q page_size must be between 1 and 200", name)
		}
	}
	return nil
}

func validateNativeRequest(request NativeSurfaceRequest, prefix string, state map[string]NativeSurfaceState) error {
	switch request.Method {
	case "GET", "POST", "PATCH", "DELETE":
	default:
		return fmt.Errorf("%s request method %q unsupported", prefix, request.Method)
	}
	if !validNativePath(request.Path) {
		return fmt.Errorf("%s request path must be app-relative and traversal-free", prefix)
	}
	switch request.Encoding {
	case "", "json", "multipart":
	default:
		return fmt.Errorf("%s request encoding %q unsupported", prefix, request.Encoding)
	}
	if containsProjectIdentity(request.Query) || containsProjectIdentity(request.Body) {
		return fmt.Errorf("%s must not declare project_id; the host injects project context", prefix)
	}
	if err := validateNativeBindings(request.Path, state); err != nil {
		return fmt.Errorf("%s path: %w", prefix, err)
	}
	for _, values := range []map[string]any{request.Query, request.Body} {
		if err := walkNativeValues(values, func(value string) error { return validateNativeBindings(value, state) }); err != nil {
			return fmt.Errorf("%s binding: %w", prefix, err)
		}
	}
	return nil
}

func validateNativeSection(section NativeSurfaceSection, prefix string, surface *NativeSurface, seen map[string]bool) error {
	if !nativeSurfaceIDPattern.MatchString(section.ID) || seen[section.ID] {
		return fmt.Errorf("%s has invalid or duplicate id %q", prefix, section.ID)
	}
	switch section.Kind {
	case "collection", "metrics", "timeline", "properties", "file_preview", "text", "component":
	default:
		return fmt.Errorf("%s kind %q unsupported", prefix, section.Kind)
	}
	if section.Source != "" && surface.DataSources[section.Source].Request.Path == "" {
		return fmt.Errorf("%s references unknown source %q", prefix, section.Source)
	}
	if section.Kind == "collection" {
		if section.Source == "" || section.Item == nil {
			return fmt.Errorf("%s collection requires source and item mapping", prefix)
		}
		switch section.Presentation {
		case "", "list", "grid", "adaptive":
		default:
			return fmt.Errorf("%s presentation %q unsupported", prefix, section.Presentation)
		}
		if section.Item.ID == "" || section.Item.Title == "" {
			return fmt.Errorf("%s item requires id and title", prefix)
		}
		if surface.DataSources[section.Source].Response.Items == "" {
			return fmt.Errorf("%s source %q requires an items response mapping", prefix, section.Source)
		}
		for _, value := range []string{section.Item.ID, section.Item.Title} {
			if err := validateNativeBindings(value, surface.State); err != nil {
				return fmt.Errorf("%s item mapping: %w", prefix, err)
			}
		}
		values := append([]NativeSurfaceValue{}, section.Item.Subtitle...)
		if section.Item.Icon != nil {
			values = append(values, *section.Item.Icon)
		}
		if section.Item.Badge != nil {
			values = append(values, *section.Item.Badge)
		}
		for _, value := range values {
			if err := validateNativeValue(value, surface.State); err != nil {
				return fmt.Errorf("%s item mapping: %w", prefix, err)
			}
		}
		if section.Item.Destination != "" && surface.Destinations[section.Item.Destination].Type == "" {
			return fmt.Errorf("%s item references unknown destination %q", prefix, section.Item.Destination)
		}
		if err := validateNativeActionRefs(section.Item.Actions, prefix+" item", surface.Actions); err != nil {
			return err
		}
	}
	if section.Kind == "component" {
		if section.Component != "chat/v1" || section.Chat == nil {
			return fmt.Errorf("%s component must declare chat/v1 and chat configuration", prefix)
		}
		if err := validateNativeChatComponent(*section.Chat, prefix, surface); err != nil {
			return err
		}
	}
	if section.Search != nil {
		state, ok := surface.State[section.Search.State]
		if !ok {
			return fmt.Errorf("%s search references unknown state %q", prefix, section.Search.State)
		}
		if state.Type != "string" {
			return fmt.Errorf("%s search state %q must be string", prefix, section.Search.State)
		}
	}
	for i, filter := range section.Filters {
		if !nativeSurfaceIDPattern.MatchString(filter.ID) || strings.TrimSpace(filter.Label) == "" {
			return fmt.Errorf("%s filters[%d] requires a valid id and label", prefix, i)
		}
		if _, ok := surface.State[filter.State]; !ok {
			return fmt.Errorf("%s filter %q references unknown state %q", prefix, filter.ID, filter.State)
		}
		switch filter.Type {
		case "choice", "resource_picker", "toggle":
		default:
			return fmt.Errorf("%s filter %q type %q unsupported", prefix, filter.ID, filter.Type)
		}
		if filter.Type == "toggle" && surface.State[filter.State].Type != "boolean" {
			return fmt.Errorf("%s filter %q toggle state must be boolean", prefix, filter.ID)
		}
		if filter.Type == "choice" && len(filter.Options) == 0 && filter.Source == "" {
			return fmt.Errorf("%s filter %q choice requires options or source", prefix, filter.ID)
		}
		if filter.Type == "resource_picker" && filter.Source == "" {
			return fmt.Errorf("%s filter %q resource_picker requires source", prefix, filter.ID)
		}
		if filter.Source != "" {
			if surface.DataSources[filter.Source].Request.Path == "" || filter.Mapping == nil ||
				!validSelector(filter.Mapping.Value) || !validSelector(filter.Mapping.Label) {
				return fmt.Errorf("%s filter %q has invalid dynamic options", prefix, filter.ID)
			}
		}
	}
	if section.Preview != nil {
		if !validNativePath(section.Preview.Path) {
			return fmt.Errorf("%s preview path invalid", prefix)
		}
		if section.Preview.ShareAction != "" && surface.Actions[section.Preview.ShareAction].Type == "" {
			return fmt.Errorf("%s preview share_action unknown", prefix)
		}
		if section.Preview.DeleteAction != "" && surface.Actions[section.Preview.DeleteAction].Type == "" {
			return fmt.Errorf("%s preview delete_action unknown", prefix)
		}
		for _, value := range []string{section.Preview.Path, section.Preview.Name, section.Preview.ContentType} {
			if value != "" {
				if err := validateNativeBindings(value, surface.State); err != nil {
					return fmt.Errorf("%s preview: %w", prefix, err)
				}
			}
		}
	}
	for _, field := range section.Fields {
		if strings.TrimSpace(field.Label) == "" || strings.TrimSpace(field.Value) == "" {
			return fmt.Errorf("%s fields require label and value", prefix)
		}
		if field.Format != "" && !validNativeFormat(field.Format) {
			return fmt.Errorf("%s field format %q unsupported", prefix, field.Format)
		}
		if err := validateNativeBindings(field.Value, surface.State); err != nil {
			return fmt.Errorf("%s field: %w", prefix, err)
		}
	}
	switch section.Kind {
	case "metrics":
		if len(section.Metrics) == 0 {
			return fmt.Errorf("%s metrics requires at least one metric mapping", prefix)
		}
	case "timeline":
		if section.Source == "" || section.Timeline == nil {
			return fmt.Errorf("%s timeline requires source and timeline mapping", prefix)
		}
	case "properties":
		if len(section.Fields) == 0 {
			return fmt.Errorf("%s properties requires fields", prefix)
		}
	case "file_preview":
		if section.Preview == nil {
			return fmt.Errorf("%s file_preview requires preview", prefix)
		}
	case "text":
		if strings.TrimSpace(section.Title) == "" && strings.TrimSpace(section.Description) == "" {
			return fmt.Errorf("%s text requires title or description", prefix)
		}
	}
	return validateNativeActionRefs(section.Actions, prefix, surface.Actions)
}

func validateNativeChatComponent(chat NativeSurfaceChatComponent, prefix string, surface *NativeSurface) error {
	conversationSource, conversationsOK := surface.DataSources[chat.ConversationsSource]
	messageSource, messagesOK := surface.DataSources[chat.MessagesSource]
	if !conversationsOK || conversationSource.Response.Items == "" {
		return fmt.Errorf("%s chat conversations_source must reference an items data source", prefix)
	}
	if !messagesOK || messageSource.Response.Items == "" {
		return fmt.Errorf("%s chat messages_source must reference an items data source", prefix)
	}
	if state, ok := surface.State[chat.SelectionState]; !ok || state.Type != "string" {
		return fmt.Errorf("%s chat selection_state must reference string state", prefix)
	}
	for label, action := range map[string]string{
		"send_action":      chat.SendAction,
		"create_action":    chat.CreateAction,
		"mark_seen_action": chat.MarkSeenAction,
	} {
		if label == "send_action" && action == "" {
			return fmt.Errorf("%s chat send_action required", prefix)
		}
		if action != "" && surface.Actions[action].Type == "" {
			return fmt.Errorf("%s chat %s references unknown action %q", prefix, label, action)
		}
	}
	selectors := map[string]string{
		"conversation.id":         chat.Conversation.ID,
		"conversation.title":      chat.Conversation.Title,
		"conversation.subtitle":   chat.Conversation.Subtitle,
		"conversation.preview":    chat.Conversation.Preview,
		"conversation.updated_at": chat.Conversation.UpdatedAt,
		"conversation.unread":     chat.Conversation.Unread,
		"message.id":              chat.Message.ID,
		"message.conversation_id": chat.Message.ConversationID,
		"message.body":            chat.Message.Body,
		"message.author":          chat.Message.Author,
		"message.role":            chat.Message.Role,
		"message.created_at":      chat.Message.CreatedAt,
		"message.status":          chat.Message.Status,
	}
	for name, selector := range selectors {
		required := name == "conversation.id" || name == "conversation.title" || name == "message.id" || name == "message.conversation_id" || name == "message.body"
		if required && selector == "" {
			return fmt.Errorf("%s chat %s mapping required", prefix, name)
		}
		if selector != "" && !validSelector(selector) {
			return fmt.Errorf("%s chat %s has invalid selector %q", prefix, name, selector)
		}
	}
	if chat.Live != nil {
		liveSelectors := map[string]string{
			"live.id": chat.Live.ID, "live.agent_id": chat.Live.AgentID,
			"live.thread_id": chat.Live.ThreadID, "live.run_id": chat.Live.RunID,
			"live.text": chat.Live.Text, "live.phase": chat.Live.Phase, "live.done": chat.Live.Done,
		}
		if chat.Live.Progress != nil {
			liveSelectors["live.progress.value"] = chat.Live.Progress.Value
			liveSelectors["live.progress.phase"] = chat.Live.Progress.Phase
			liveSelectors["live.progress.revision"] = chat.Live.Progress.Revision
			liveSelectors["live.progress.tool_name"] = chat.Live.Progress.ToolName
			liveSelectors["live.progress.call_id"] = chat.Live.Progress.CallID
			liveSelectors["live.progress.after_message_id"] = chat.Live.Progress.AfterMessageID
		}
		if chat.Live.Activity != nil {
			liveSelectors["live.activity.value"] = chat.Live.Activity.Value
			liveSelectors["live.activity.id"] = chat.Live.Activity.ID
			liveSelectors["live.activity.revision"] = chat.Live.Activity.Revision
			liveSelectors["live.activity.name"] = chat.Live.Activity.Name
			liveSelectors["live.activity.reason"] = chat.Live.Activity.Reason
			liveSelectors["live.activity.status"] = chat.Live.Activity.Status
			liveSelectors["live.activity.duration_ms"] = chat.Live.Activity.DurationMS
		}
		for name, selector := range liveSelectors {
			if selector != "" && !validSelector(selector) {
				return fmt.Errorf("%s chat %s has invalid selector %q", prefix, name, selector)
			}
		}
	}
	if chat.Subscription == nil {
		return nil
	}
	if chat.Subscription.Request.Method != "GET" {
		return fmt.Errorf("%s chat subscription request must use GET", prefix)
	}
	if err := validateNativeRequest(chat.Subscription.Request, prefix+" chat subscription", surface.State); err != nil {
		return err
	}
	if len(chat.Subscription.Events) == 0 {
		return fmt.Errorf("%s chat subscription requires event mappings", prefix)
	}
	for event, mapping := range chat.Subscription.Events {
		if strings.TrimSpace(event) == "" {
			return fmt.Errorf("%s chat subscription event name required", prefix)
		}
		switch mapping.Operation {
		case "append", "prepend", "upsert", "remove", "invalidate", "set_activity":
		default:
			return fmt.Errorf("%s chat subscription event %q has unsupported operation %q", prefix, event, mapping.Operation)
		}
		if _, ok := surface.DataSources[mapping.Source]; !ok {
			return fmt.Errorf("%s chat subscription event %q references unknown source %q", prefix, event, mapping.Source)
		}
		if mapping.Operation != "invalidate" && mapping.Value == "" {
			return fmt.Errorf("%s chat subscription event %q requires value", prefix, event)
		}
		if mapping.Value != "" && !validSelector(mapping.Value) {
			return fmt.Errorf("%s chat subscription event %q has invalid value selector", prefix, event)
		}
		if mapping.ID != "" && !validSelector(mapping.ID) {
			return fmt.Errorf("%s chat subscription event %q has invalid id selector", prefix, event)
		}
	}
	return nil
}

func validateNativeAction(name string, action NativeSurfaceAction, surface *NativeSurface, sectionIDs map[string]bool) error {
	switch action.Type {
	case "request", "form", "file_upload", "open_url", "copy_result", "share_result", "confirm":
	default:
		return fmt.Errorf("action %q type %q unsupported", name, action.Type)
	}
	if strings.TrimSpace(action.Label) == "" {
		return fmt.Errorf("action %q label required", name)
	}
	for _, placement := range action.Placements {
		switch placement {
		case "primary", "section", "item", "detail":
		default:
			return fmt.Errorf("action %q placement %q unsupported", name, placement)
		}
	}
	if action.Request == nil {
		return fmt.Errorf("action %q request required", name)
	}
	if err := validateNativeRequest(*action.Request, "action "+name, surface.State); err != nil {
		return err
	}
	if action.Destructive && action.Confirmation == nil {
		return fmt.Errorf("action %q is destructive and requires confirmation", name)
	}
	if action.Type == "confirm" && action.Confirmation == nil {
		return fmt.Errorf("action %q confirm requires confirmation", name)
	}
	if action.Type == "file_upload" && action.Request.Encoding != "multipart" {
		return fmt.Errorf("action %q file_upload request encoding must be multipart", name)
	}
	if action.Confirmation != nil && strings.TrimSpace(action.Confirmation.Title) == "" {
		return fmt.Errorf("action %q confirmation title required", name)
	}
	fileInputs := 0
	inputNames := map[string]bool{}
	for _, field := range action.Fields {
		if !nativeSurfaceIDPattern.MatchString(field.Name) || inputNames[field.Name] {
			return fmt.Errorf("action %q has invalid or duplicate input %q", name, field.Name)
		}
		inputNames[field.Name] = true
		switch field.Type {
		case "string", "multiline", "password", "url", "integer", "number", "boolean", "choice", "string_list", "file", "resource":
		default:
			return fmt.Errorf("action %q input %q type %q unsupported", name, field.Name, field.Type)
		}
		if field.Type == "file" && field.Required {
			fileInputs++
		}
		if field.Type == "resource" && surface.Resources[field.Resource].Source == "" {
			return fmt.Errorf("action %q input %q references unknown resource %q", name, field.Name, field.Resource)
		}
		if field.Type == "choice" && len(field.Options) == 0 {
			return fmt.Errorf("action %q input %q choice requires options", name, field.Name)
		}
		if len(field.Default) > 0 {
			var defaultValue any
			if err := json.Unmarshal(field.Default, &defaultValue); err != nil {
				return fmt.Errorf("action %q input %q default: %w", name, field.Name, err)
			}
			if binding, ok := defaultValue.(string); ok && (strings.HasPrefix(binding, "$") || strings.Contains(binding, "{")) {
				if err := validateNativeBindings(binding, surface.State); err != nil {
					return fmt.Errorf("action %q input %q default: %w", name, field.Name, err)
				}
			} else if !nativeValueMatchesType(defaultValue, nativeInputStateType(field.Type)) {
				return fmt.Errorf("action %q input %q default does not match type %s", name, field.Name, field.Type)
			}
		}
	}
	if action.Type == "file_upload" && fileInputs != 1 {
		return fmt.Errorf("action %q file_upload requires exactly one required file input", name)
	}
	if action.Result != "" && !validSelector(action.Result) {
		return fmt.Errorf("action %q result selector invalid", name)
	}
	if action.Success != nil {
		for _, ref := range action.Success.Refresh {
			if !sectionIDs[ref] && surface.DataSources[ref].Request.Path == "" {
				return fmt.Errorf("action %q refresh references unknown section or source %q", name, ref)
			}
		}
	}
	return nil
}

func validateNativeValue(value NativeSurfaceValue, state map[string]NativeSurfaceState) error {
	if strings.TrimSpace(value.Value) == "" {
		return errors.New("value required")
	}
	if value.Format != "" && !validNativeFormat(value.Format) {
		return fmt.Errorf("format %q unsupported", value.Format)
	}
	return validateNativeBindings(value.Value, state)
}

func validNativeFormat(format string) bool {
	switch format {
	case "file_size", "relative_time", "date_time", "file_icon", "tags", "number", "currency":
		return true
	default:
		return false
	}
}

func nativeInputStateType(inputType string) string {
	switch inputType {
	case "integer", "number", "boolean", "string_list":
		return inputType
	default:
		return "string"
	}
}

func validateNativeActionRefs(refs []string, prefix string, actions map[string]NativeSurfaceAction) error {
	seen := map[string]bool{}
	for _, ref := range refs {
		if actions[ref].Type == "" {
			return fmt.Errorf("%s references unknown action %q", prefix, ref)
		}
		if seen[ref] {
			return fmt.Errorf("%s contains duplicate action %q", prefix, ref)
		}
		seen[ref] = true
	}
	return nil
}

func validNativePath(value string) bool {
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.Contains(value, "://") || strings.ContainsAny(value, "?#\\") || hasPathTraversal(value) {
		return false
	}
	return true
}

func validSelector(value string) bool { return nativeSurfaceSelectorPattern.MatchString(value) }

func validateNativeBindings(value string, state map[string]NativeSurfaceState) error {
	if strings.HasPrefix(value, "$") && !nativeSurfaceDirectBinding.MatchString(value) {
		return fmt.Errorf("unsupported binding expression %q", value)
	}
	if strings.HasPrefix(value, "$state.") {
		name := strings.TrimPrefix(value, "$state.")
		if strings.Contains(name, ".") {
			name = strings.SplitN(name, ".", 2)[0]
		}
		if _, ok := state[name]; !ok {
			return fmt.Errorf("unknown state %q", name)
		}
	}
	for _, match := range nativeSurfaceBindingPattern.FindAllStringSubmatch(value, -1) {
		if match[1] == "state" {
			if match[2] == "" {
				return errors.New("state binding requires a key")
			}
			if _, ok := state[match[2]]; !ok {
				return fmt.Errorf("unknown state %q", match[2])
			}
		}
	}
	withoutBindings := nativeSurfaceBindingPattern.ReplaceAllString(value, "")
	if strings.ContainsAny(withoutBindings, "{}") {
		return fmt.Errorf("unsupported template expression %q", value)
	}
	return nil
}

func containsProjectIdentity(values map[string]any) bool {
	for key, value := range values {
		lower := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
		if lower == "project_id" || lower == "x_apteva_project_id" {
			return true
		}
		switch nested := value.(type) {
		case map[string]any:
			if containsProjectIdentity(nested) {
				return true
			}
		case []any:
			for _, item := range nested {
				if object, ok := item.(map[string]any); ok && containsProjectIdentity(object) {
					return true
				}
			}
		}
	}
	return false
}

func walkNativeValues(value any, visit func(string) error) error {
	switch typed := value.(type) {
	case string:
		return visit(typed)
	case map[string]any:
		for _, child := range typed {
			if err := walkNativeValues(child, visit); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := walkNativeValues(child, visit); err != nil {
				return err
			}
		}
	}
	return nil
}
