package clickguard

import (
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

type WorkflowObservation struct {
	URL      string `json:"url"`
	Timezone string `json:"timezone"`
	Date     string `json:"date"`
	Time     string `json:"time"`
}

func inspectWorkflowScript(script string, policies []computer.WorkflowConstraint) string {
	if len(policies) == 0 {
		return script
	}
	raw, _ := json.Marshal(policies)
	return fmt.Sprintf(`(function(){var target=%s;var policies=%s;
target.workflow_observations=policies.map(function(p){
 function nativeValue(selector,type){try{var nodes=document.querySelectorAll(selector);if(nodes.length!==1||nodes[0].tagName!=='INPUT'||nodes[0].type!==type||nodes[0].disabled)return '';return nodes[0].value;}catch(e){return '';}}
 return {url:location.href,timezone:Intl.DateTimeFormat().resolvedOptions().timeZone,date:nativeValue(p.date_selector,'date'),time:nativeValue(p.time_selector,'time')};
});return target;})()`, script, raw)
}

func validateWorkflow(target Target, options Options) error {
	for i, p := range options.WorkflowConstraints {
		reject := func(code string) error {
			return &ConsequenceError{Code: code, Target: target, DetectedEffect: CanonicalEffect(target.DestructiveEffect), ExpectedEffect: options.ExpectedEffect, ConfirmConsequence: options.ConfirmConsequence, WorkflowID: p.ID, AllowedEffect: p.AllowedEffect, ScheduledAt: p.ScheduledAt}
		}
		if target.OpaqueFrame {
			return reject("workflow_target_unverifiable")
		}
		if !target.Dangerous && target.DestructiveEffect == "" {
			continue
		}
		if CanonicalEffect(target.DestructiveEffect) != p.AllowedEffect {
			return reject("workflow_effect_mismatch")
		}
		if i >= len(target.WorkflowObservations) {
			return reject("workflow_schedule_unverifiable")
		}
		o := target.WorkflowObservations[i]
		u, err := url.Parse(o.URL)
		if err != nil {
			return reject("workflow_resource_mismatch")
		}
		u.RawQuery, u.Fragment = "", ""
		if u.String() != p.ResourceURL {
			return reject("workflow_resource_mismatch")
		}
		at, err := time.Parse(time.RFC3339, p.ScheduledAt)
		loc, zoneErr := time.LoadLocation(p.Timezone)
		if err != nil || zoneErr != nil || !at.After(time.Now()) || o.Timezone != p.Timezone {
			return reject("workflow_schedule_mismatch")
		}
		local := at.In(loc)
		if o.Date != local.Format("2006-01-02") || (o.Time != local.Format("15:04") && o.Time != local.Format("15:04:05")) {
			return reject("workflow_schedule_mismatch")
		}
	}
	return nil
}
