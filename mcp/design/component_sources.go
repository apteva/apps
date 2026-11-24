package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type resolvedComponentPayload struct {
	PartID               string            `json:"part_id,omitempty"`
	OperationID          string            `json:"operation_id,omitempty"`
	ParameterExpressions map[string]string `json:"parameter_expressions,omitempty"`
	SourcePartID         string            `json:"source_part_id,omitempty"`
	DesignID             int64             `json:"design_id"`
	DesignName           string            `json:"design_name"`
	RevisionID           int64             `json:"revision_id"`
	RevisionNumber       int               `json:"revision_number"`
	SourceSHA256         string            `json:"source_sha256"`
	Definition           json.RawMessage   `json:"definition"`
	Parameters           json.RawMessage   `json:"parameters"`
}

// materializeComponentSources resolves immutable cross-design links into a
// runner-only payload. The persisted revision remains compact and contains
// only pinned source coordinates and hashes.
func (s *Service) materializeComponentSources(ownerRevisionID int64, canonical []byte) ([]byte, []ComponentDependency, error) {
	stack := map[int64]bool{}
	if ownerRevisionID > 0 {
		stack[ownerRevisionID] = true
	}
	count := 0
	return s.materializeDefinition(ownerRevisionID, canonical, stack, &count)
}

func (s *Service) materializeDefinition(ownerRevisionID int64, canonical []byte, stack map[int64]bool, count *int) ([]byte, []ComponentDependency, error) {
	var assetErr error
	canonical, assetErr = s.materializeAssets(canonical)
	if assetErr != nil {
		return nil, nil, assetErr
	}
	if len(stack) > 16 {
		return nil, nil, errors.New("component dependency depth exceeds 16 revisions")
	}
	var definition DesignDefinition
	if err := json.Unmarshal(canonical, &definition); err != nil {
		return nil, nil, fmt.Errorf("decode component definition: %w", err)
	}
	resolved := make([]resolvedComponentPayload, 0)
	operationComponents := map[string]resolvedComponentPayload{}
	dependencies := make([]ComponentDependency, 0)
	for _, part := range definition.Parts {
		if part.Source == nil {
			continue
		}
		payload, edges, err := s.resolveComponentSource(ownerRevisionID, fmt.Sprintf("part %q", part.ID), part.ID, "", *part.Source, stack, count)
		if err != nil {
			return nil, nil, err
		}
		resolved = append(resolved, payload)
		dependencies = append(dependencies, edges...)
	}
	for _, operation := range definition.Operations {
		if kind, _ := operation["type"].(string); kind != "component" {
			continue
		}
		id, _ := operation["id"].(string)
		source, err := operationComponentSource(operation)
		if err != nil {
			return nil, nil, fmt.Errorf("component operation %q: %w", id, err)
		}
		payload, edges, err := s.resolveComponentSource(ownerRevisionID, fmt.Sprintf("component operation %q", id), "", id, source, stack, count)
		if err != nil {
			return nil, nil, err
		}
		operationComponents[id] = payload
		dependencies = append(dependencies, edges...)
	}
	if len(resolved) == 0 && len(operationComponents) == 0 {
		return canonical, dependencies, nil
	}
	var runtime map[string]any
	if err := json.Unmarshal(canonical, &runtime); err != nil {
		return nil, nil, err
	}
	if len(resolved) != 0 {
		runtime["_resolved_components"] = resolved
	}
	if len(operationComponents) != 0 {
		runtime["_resolved_operation_components"] = operationComponents
	}
	materialized, err := json.Marshal(runtime)
	return materialized, dependencies, err
}

// operationComponentSource reads a component operation's source and
// parameter overrides into the same shape a linked part uses.
func operationComponentSource(operation map[string]any) (PartSourceReference, error) {
	var source PartSourceReference
	raw, err := json.Marshal(operation["source"])
	if err != nil {
		return source, err
	}
	if err := json.Unmarshal(raw, &source); err != nil {
		return source, fmt.Errorf("invalid source: %w", err)
	}
	if params, ok := operation["parameters"].(map[string]any); ok && len(params) != 0 {
		if source.Parameters == nil {
			source.Parameters = map[string]any{}
		}
		for name, value := range params {
			source.Parameters[name] = value
		}
	}
	return source, nil
}

// resolveComponentSource pins one link (a linked part or a component
// operation) to its immutable source revision and applies its parameter
// overrides. Numeric/boolean overrides are merged and range-checked here;
// string overrides are expressions over the owner's parameters, which only
// the runner can evaluate, so they travel as parameter_expressions and the
// child build range-checks the result.
func (s *Service) resolveComponentSource(ownerRevisionID int64, label, partID, operationID string, source PartSourceReference, stack map[int64]bool, count *int) (resolvedComponentPayload, []ComponentDependency, error) {
	var empty resolvedComponentPayload
	*count = *count + 1
	if *count > 64 {
		return empty, nil, errors.New("component dependency graph exceeds 64 links")
	}
	revision, err := s.store.GetRevision(s.project, source.RevisionID)
	if err != nil {
		return empty, nil, fmt.Errorf("resolve %s revision %d: %w", label, source.RevisionID, err)
	}
	if revision.DesignID != source.DesignID {
		return empty, nil, fmt.Errorf("%s source revision %d does not belong to design %d", label, source.RevisionID, source.DesignID)
	}
	if revision.SourceSHA256 != source.SourceSHA256 {
		return empty, nil, fmt.Errorf("%s source hash mismatch for revision %d", label, source.RevisionID)
	}
	if stack[revision.ID] {
		return empty, nil, fmt.Errorf("component dependency cycle reaches revision %d through %s", revision.ID, label)
	}
	design, err := s.store.GetDesign(s.project, source.DesignID)
	if err != nil {
		return empty, nil, fmt.Errorf("resolve %s design %d: %w", label, source.DesignID, err)
	}
	sourceCanonical, sourceDefinition, err := normalizeDefinition(revision.Definition, s.maxOperations)
	if err != nil {
		return empty, nil, fmt.Errorf("%s source revision is invalid: %w", label, err)
	}
	if source.PartID != "" && !definitionHasPart(sourceDefinition, source.PartID) {
		return empty, nil, fmt.Errorf("%s source revision has no part %q", label, source.PartID)
	}
	sourceParameters, expressions, err := mergeLinkParameters(revision.Parameters, source.Parameters, sourceDefinition)
	if err != nil {
		return empty, nil, fmt.Errorf("%s parameters are invalid: %w", label, err)
	}
	nextStack := cloneRevisionStack(stack)
	nextStack[revision.ID] = true
	materialized, nested, err := s.materializeDefinition(revision.ID, sourceCanonical, nextStack, count)
	if err != nil {
		return empty, nil, err
	}
	payload := resolvedComponentPayload{
		PartID: partID, OperationID: operationID, SourcePartID: source.PartID, DesignID: design.ID, DesignName: design.Name,
		RevisionID: revision.ID, RevisionNumber: revision.RevisionNumber, SourceSHA256: revision.SourceSHA256,
		Definition: materialized, Parameters: sourceParameters, ParameterExpressions: expressions,
	}
	edges := append([]ComponentDependency{{
		OwnerRevisionID: ownerRevisionID, OwnerPartID: partID, OwnerOperationID: operationID,
		SourceDesignID: design.ID, SourceDesignName: design.Name,
		SourceRevisionID: revision.ID, SourceRevisionNumber: revision.RevisionNumber,
		SourceSHA256: revision.SourceSHA256, SourcePartID: source.PartID,
	}}, nested...)
	return payload, edges, nil
}

// mergeLinkParameters overlays per-link overrides on the source revision's
// stored parameter values.
func mergeLinkParameters(stored json.RawMessage, overrides map[string]any, definition *DesignDefinition) (json.RawMessage, map[string]string, error) {
	base, err := normalizeParameters(stored, definition)
	if err != nil {
		return nil, nil, err
	}
	if len(overrides) == 0 {
		return base, nil, nil
	}
	values := map[string]any{}
	if err := json.Unmarshal(base, &values); err != nil {
		return nil, nil, err
	}
	expressions := map[string]string{}
	for name, value := range overrides {
		if _, ok := definition.Parameters[name]; !ok {
			return nil, nil, fmt.Errorf("source design has no parameter %q", name)
		}
		if text, ok := value.(string); ok {
			if strings.TrimSpace(text) == "" || len(text) > 2000 {
				return nil, nil, fmt.Errorf("parameter %s expression must be 1-2000 characters", name)
			}
			expressions[name] = text
			delete(values, name)
			continue
		}
		values[name] = value
	}
	body, err := json.Marshal(values)
	if err != nil {
		return nil, nil, err
	}
	merged, err := normalizeParameters(body, definition)
	if err != nil {
		return nil, nil, err
	}
	if len(expressions) == 0 {
		expressions = nil
	}
	return merged, expressions, nil
}

func definitionHasPart(definition *DesignDefinition, id string) bool {
	for _, part := range definition.Parts {
		if part.ID == id {
			return true
		}
	}
	return false
}

func cloneRevisionStack(input map[int64]bool) map[int64]bool {
	output := make(map[int64]bool, len(input)+1)
	for id, present := range input {
		output[id] = present
	}
	return output
}

type ComponentSourceUpdate struct {
	PartID         string `json:"part_id,omitempty"`
	OperationID    string `json:"operation_id,omitempty"`
	DesignID       int64  `json:"design_id"`
	FromRevisionID int64  `json:"from_revision_id"`
	ToRevisionID   int64  `json:"to_revision_id"`
	SourceSHA256   string `json:"source_sha256"`
}

func (s *Service) RefreshComponentSources(designID, parentID int64, note, author string) (*Revision, []ComponentSourceUpdate, error) {
	target, err := s.store.GetDesign(s.project, designID)
	if err != nil {
		return nil, nil, err
	}
	if target.CurrentRevisionID != parentID {
		return nil, nil, fmt.Errorf("%w: expected %d, current is %d", errRevisionConflict, parentID, target.CurrentRevisionID)
	}
	parent, err := s.store.GetRevision(s.project, parentID)
	if err != nil {
		return nil, nil, err
	}
	if parent.DesignID != designID {
		return nil, nil, errors.New("expected parent does not belong to design")
	}
	_, definition, err := normalizeDefinition(parent.Definition, s.maxOperations)
	if err != nil {
		return nil, nil, err
	}
	updates := []ComponentSourceUpdate{}
	for index := range definition.Parts {
		source := definition.Parts[index].Source
		if source == nil {
			continue
		}
		design, err := s.store.GetDesign(s.project, source.DesignID)
		if err != nil {
			return nil, nil, fmt.Errorf("refresh part %q: %w", definition.Parts[index].ID, err)
		}
		if design.CurrentRevisionID == source.RevisionID {
			continue
		}
		current := design.CurrentRevision
		if current == nil {
			return nil, nil, fmt.Errorf("source design %d has no current revision", design.ID)
		}
		updates = append(updates, ComponentSourceUpdate{
			PartID: definition.Parts[index].ID, DesignID: design.ID,
			FromRevisionID: source.RevisionID, ToRevisionID: current.ID, SourceSHA256: current.SourceSHA256,
		})
		source.RevisionID = current.ID
		source.SourceSHA256 = current.SourceSHA256
	}
	for _, operation := range definition.Operations {
		if kind, _ := operation["type"].(string); kind != "component" {
			continue
		}
		id, _ := operation["id"].(string)
		sourceMap, _ := operation["source"].(map[string]any)
		source, err := operationComponentSource(operation)
		if err != nil || sourceMap == nil {
			return nil, nil, fmt.Errorf("refresh component operation %q: invalid source", id)
		}
		design, err := s.store.GetDesign(s.project, source.DesignID)
		if err != nil {
			return nil, nil, fmt.Errorf("refresh component operation %q: %w", id, err)
		}
		if design.CurrentRevisionID == source.RevisionID {
			continue
		}
		current := design.CurrentRevision
		if current == nil {
			return nil, nil, fmt.Errorf("source design %d has no current revision", design.ID)
		}
		updates = append(updates, ComponentSourceUpdate{
			OperationID: id, DesignID: design.ID,
			FromRevisionID: source.RevisionID, ToRevisionID: current.ID, SourceSHA256: current.SourceSHA256,
		})
		sourceMap["revision_id"] = current.ID
		sourceMap["source_sha256"] = current.SourceSHA256
	}
	if len(updates) == 0 {
		return parent, updates, nil
	}
	body, err := json.Marshal(definition)
	if err != nil {
		return nil, nil, err
	}
	canonical, _, err := normalizeDefinition(body, s.maxOperations)
	if err != nil {
		return nil, nil, err
	}
	if _, _, err := s.materializeComponentSources(0, canonical); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(note) == "" {
		note = fmt.Sprintf("Refresh %d linked component revision(s)", len(updates))
	}
	revision, err := s.store.CreateRevision(s.project, CreateRevisionInput{
		DesignID: designID, ExpectedParent: parentID, Definition: canonical, Parameters: parent.Parameters,
		Note: note, Author: author,
	})
	return revision, updates, err
}

// ComponentDependent is one design whose current revision links to a source
// design through a linked part or a component operation.
type ComponentDependent struct {
	DesignID          int64          `json:"design_id"`
	DesignName        string         `json:"design_name"`
	CurrentRevisionID int64          `json:"current_revision_id"`
	Links             []ComponentPin `json:"links"`
	Stale             bool           `json:"stale"`
}

type ComponentPin struct {
	PartID           string `json:"part_id,omitempty"`
	OperationID      string `json:"operation_id,omitempty"`
	PinnedRevisionID int64  `json:"pinned_revision_id"`
	CurrentSource    int64  `json:"source_current_revision_id"`
	Stale            bool   `json:"stale"`
}

// ComponentDependents lists active designs whose current revision links
// directly to sourceDesignID.
func (s *Service) ComponentDependents(sourceDesignID int64) ([]ComponentDependent, error) {
	source, err := s.store.GetDesign(s.project, sourceDesignID)
	if err != nil {
		return nil, err
	}
	heads, err := s.store.ActiveDesignHeads(s.project)
	if err != nil {
		return nil, err
	}
	out := []ComponentDependent{}
	for _, head := range heads {
		if head.DesignID == sourceDesignID {
			continue
		}
		var definition DesignDefinition
		if json.Unmarshal(head.Definition, &definition) != nil {
			continue
		}
		pins := []ComponentPin{}
		for _, part := range definition.Parts {
			if part.Source != nil && part.Source.DesignID == sourceDesignID {
				pins = append(pins, ComponentPin{PartID: part.ID, PinnedRevisionID: part.Source.RevisionID})
			}
		}
		for _, operation := range definition.Operations {
			if kind, _ := operation["type"].(string); kind != "component" {
				continue
			}
			ref, err := operationComponentSource(operation)
			if err == nil && ref.DesignID == sourceDesignID {
				id, _ := operation["id"].(string)
				pins = append(pins, ComponentPin{OperationID: id, PinnedRevisionID: ref.RevisionID})
			}
		}
		if len(pins) == 0 {
			continue
		}
		dependent := ComponentDependent{DesignID: head.DesignID, DesignName: head.Name, CurrentRevisionID: head.RevisionID, Links: pins}
		for index := range dependent.Links {
			dependent.Links[index].CurrentSource = source.CurrentRevisionID
			dependent.Links[index].Stale = dependent.Links[index].PinnedRevisionID != source.CurrentRevisionID
			dependent.Stale = dependent.Stale || dependent.Links[index].Stale
		}
		out = append(out, dependent)
	}
	return out, nil
}

// ComponentRefreshResult reports what a bulk refresh did to one design.
type ComponentRefreshResult struct {
	DesignID       int64                   `json:"design_id"`
	DesignName     string                  `json:"design_name"`
	Status         string                  `json:"status"` // refreshed | up_to_date | failed
	FromRevisionID int64                   `json:"from_revision_id"`
	RevisionID     int64                   `json:"revision_id,omitempty"`
	Updates        []ComponentSourceUpdate `json:"updates,omitempty"`
	Error          string                  `json:"error,omitempty"`
}

// RefreshAllComponentDependents advances every design that links to
// sourceDesignID to the source's current revision, one new immutable
// revision per design. With transitive, designs that link to a refreshed
// design are refreshed too. Each design succeeds or fails on its own; a
// failure (for example an override now outside the source's range) is
// reported and does not stop the others.
func (s *Service) RefreshAllComponentDependents(sourceDesignID int64, transitive bool, note, author string) ([]ComponentRefreshResult, error) {
	if _, err := s.store.GetDesign(s.project, sourceDesignID); err != nil {
		return nil, err
	}
	results := []ComponentRefreshResult{}
	index := map[int64]int{}
	queue := []int64{sourceDesignID}
	seenSource := map[int64]bool{sourceDesignID: true}
	for len(queue) > 0 && len(results) < 200 {
		current := queue[0]
		queue = queue[1:]
		dependents, err := s.ComponentDependents(current)
		if err != nil {
			return results, err
		}
		for _, dependent := range dependents {
			result := ComponentRefreshResult{DesignID: dependent.DesignID, DesignName: dependent.DesignName, FromRevisionID: dependent.CurrentRevisionID}
			revision, updates, err := s.RefreshComponentSources(dependent.DesignID, dependent.CurrentRevisionID, note, author)
			switch {
			case err != nil:
				result.Status, result.Error = "failed", err.Error()
			case len(updates) == 0:
				result.Status, result.RevisionID = "up_to_date", revision.ID
			default:
				result.Status, result.RevisionID, result.Updates = "refreshed", revision.ID, updates
			}
			if position, ok := index[dependent.DesignID]; ok {
				previous := results[position]
				if result.Status == "up_to_date" && previous.Status == "refreshed" {
					continue
				}
				if result.Status == "refreshed" && previous.Status == "refreshed" {
					result.FromRevisionID = previous.FromRevisionID
					result.Updates = append(previous.Updates, result.Updates...)
				}
				results[position] = result
			} else {
				index[dependent.DesignID] = len(results)
				results = append(results, result)
			}
			if transitive && result.Status == "refreshed" && !seenSource[dependent.DesignID] {
				seenSource[dependent.DesignID] = true
				queue = append(queue, dependent.DesignID)
			}
		}
	}
	return results, nil
}
