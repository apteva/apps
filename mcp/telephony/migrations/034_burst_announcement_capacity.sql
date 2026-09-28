-- Capacity checks must not scan ended call history on each suppressed attempt.
CREATE INDEX idx_burst_announcements_active ON calls(project_id)
WHERE handling_reason='burst_suppressed' AND announcement_text<>''
AND status NOT IN ('completed','failed','no-answer','busy','canceled');
