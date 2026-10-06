-- Preserve the decision that produced cached bytes, rather than recomputing it
-- with newer evidence or leaving a reused render without resolved parameters.
ALTER TABLE render_result_cache ADD COLUMN resolved_params TEXT;
