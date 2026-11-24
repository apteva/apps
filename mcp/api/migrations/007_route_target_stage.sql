-- Function route stage (0.15.0): the Functions stage a function route
-- invokes. May reference {{stage.name}} / {{stage.vars.<key>}}; resolved per
-- stage-served request. Empty = the function's active version.
ALTER TABLE api_routes ADD COLUMN target_stage TEXT NOT NULL DEFAULT '';
ALTER TABLE api_configuration_routes ADD COLUMN target_stage TEXT NOT NULL DEFAULT '';
ALTER TABLE api_request_logs ADD COLUMN target_stage TEXT NOT NULL DEFAULT '';
