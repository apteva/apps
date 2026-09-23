# Processes 0.16.0

Processes steps are generic again. A procedure defines step instructions,
roles, expected outputs, dependencies, and optional timing; it does not define
a native work/approval type.

## What changed

- Removed `kind` from the step authoring schema and editor.
- Removed native approval decisions, approve/reject controls, approval badges,
  approval-specific events, and approval-based run failure or dependency gates.
- `approval_requirements` remains frozen procedure policy. An agent can be told
  in an ordinary step to obtain approval through the appropriate communication
  or integration tool and record the evidence in its output.
- Every unbound role now defaults to the assignment owner. A generic step can
  still be explicitly assigned to a human project operator.
- Existing procedure versions and run rows that contain legacy approval metadata
  are migrated and displayed as ordinary steps.

The deterministic graph layout, safe draft/assignment activation lifecycle,
app-provisioned workers with inherited agent MCP servers, persistent sequential
workers, run history, execution tool activity, and global overview remain
unchanged.
