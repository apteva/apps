package main

import (
	"errors"
	"fmt"
	"github.com/andybalholm/cascadia"
	sdk "github.com/apteva/app-sdk"
	"golang.org/x/net/html"
	"sort"
	"strings"
	"time"
)

type actorReadDiagnostic struct {
	At                string         `json:"at"`
	Target            map[string]any `json:"target,omitempty"`
	BeforeIDs         []string       `json:"before_ids"`
	AfterIDs          []string       `json:"after_ids"`
	NewIDs            []string       `json:"new_ids"`
	DispatchAttempted bool           `json:"dispatch_attempted"`
	DispatchAccepted  bool           `json:"dispatch_accepted"`
	LoadingObserved   bool           `json:"loading_observed"`
	Loading           bool           `json:"loading"`
	Polls             int            `json:"polls"`
	ControlPresent    bool           `json:"control_present"`
	ErrorText         string         `json:"error_text,omitempty"`
	Status            string         `json:"status"`
	StopReason        string         `json:"stop_reason,omitempty"`
}

func sortedReadIDs(ids map[string]bool) []string {
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
func readPageKeys(root *html.Node, v actorReadView) ([]string, error) {
	sel, err := cascadia.Compile(v.Items)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, n := range cascadia.QueryAll(root, sel) {
		x, err := extractNodeItem(n, map[string]actorField{v.KeyField: v.Fields[v.KeyField]}, "")
		if err != nil {
			return nil, err
		}
		key := stringFromAny(x[v.KeyField])
		if key == "" {
			return nil, errors.New("empty pagination key")
		}
		ids[key] = true
	}
	return sortedReadIDs(ids), nil
}
func readStopReason(err error) string {
	switch {
	case errors.Is(err, errReadPaginationStalled):
		return "no_change_control_present"
	case errors.Is(err, errReadCollectionBound):
		return "read_bound_reached"
	case strings.Contains(err.Error(), "displays an error"):
		return "site_error"
	case strings.Contains(err.Error(), "loading"):
		return "loading_timeout"
	default:
		return "inspection_failed"
	}
}
func (e *actorExecution) observeReadDiagnostic(v actorReadView, root *html.Node) {
	d := e.activeReadDiagnostic
	if d == nil {
		return
	}
	d.Polls++
	d.Loading = readHas(root, v.LoadingSelector)
	d.LoadingObserved = d.LoadingObserved || d.Loading
	d.ControlPresent = readHas(root, v.Pagination.Next.Selector)
	if ids, err := readPageKeys(root, v); err == nil {
		e.updateReadAfter(v, ids)
	}
	if v.ErrorSelector != "" {
		sel, err := cascadia.Compile(v.ErrorSelector)
		if err == nil {
			if n := cascadia.Query(root, sel); n != nil {
				d.ErrorText = truncateString(htmlNodeText(n), 1000)
				d.Status = "incomplete"
				d.StopReason = "site_error"
			}
		}
	}
	if d.Loading {
		d.StopReason = "loading_observed"
	}
}
func (e *actorExecution) updateReadAfter(v actorReadView, ids []string) {
	d := e.activeReadDiagnostic
	if d == nil {
		return
	}
	d.AfterIDs = append([]string{}, ids...)
	d.NewIDs = []string{}
	before := map[string]bool{}
	for _, id := range d.BeforeIDs {
		before[id] = true
	}
	for _, id := range ids {
		if !before[id] {
			d.NewIDs = append(d.NewIDs, id)
		}
	}
}
func (e *actorExecution) dispatchReadNext(v actorReadView, result *actorViewCoverage, ids []string) error {
	d := &actorReadDiagnostic{At: time.Now().UTC().Format(time.RFC3339Nano), BeforeIDs: append([]string{}, ids...), AfterIDs: append([]string{}, ids...), NewIDs: []string{}, Status: "pending", ControlPresent: true}
	result.Pagination = append(result.Pagination, d)
	e.activeReadDiagnostic = d
	var err error
	if v.Pagination.ClickMode == "selector" {
		err = e.clickReadSelector(v.Pagination.Next)
	} else {
		err = e.clickReadNavigation(v.Pagination.Next, v.Pagination.RevealFrom)
	}
	if err != nil {
		d.Status = "incomplete"
		d.StopReason = "click_rejected"
		d.ErrorText = truncateString(err.Error(), 1000)
		result.StopReason = d.StopReason
	}
	return err
}
func (e *actorExecution) clickReadSelector(l actorLocator) error {
	doc, err := e.completeRawDOM()
	if err != nil {
		return err
	}
	root, err := html.Parse(strings.NewReader(doc.HTML))
	if err != nil {
		return err
	}
	sel, err := cascadia.Compile(l.Selector)
	if err != nil {
		return err
	}
	nodes := cascadia.QueryAll(root, sel)
	if len(nodes) != 1 {
		return errors.New("pagination selector is missing or ambiguous")
	}
	n := nodes[0]
	if strings.TrimSpace(htmlNodeText(n)) != l.Text {
		return errors.New("pagination selector exact label mismatch")
	}
	id, _ := htmlAttribute(n, "id")
	onclick, _ := htmlAttribute(n, "onclick")
	d := e.activeReadDiagnostic
	d.Target = map[string]any{"mode": "selector", "selector": l.Selector, "dom_id": id, "tag": n.Data, "name": l.Text, "onclick": truncateString(onclick, 500)}
	d.DispatchAttempted = true
	e.persistProgress("dispatching exact pagination element")
	var out map[string]any
	err = sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, map[string]any{"session_id": e.session.SessionID, "action": "click", "selector": l.Selector, "expected_text": l.Text, "expected_effect": "navigation_only"}), &out)
	if err != nil {
		return err
	}
	d.DispatchAccepted = true
	if err = e.finishInteraction(out); err != nil {
		return fmt.Errorf("pagination interaction: %w", err)
	}
	return nil
}
