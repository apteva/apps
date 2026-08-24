-- Separate planned work, soft submission due dates, and hard worker access.
ALTER TABLE gigs ADD COLUMN scheduled_for TIMESTAMP;
ALTER TABLE gigs ADD COLUMN due_at TIMESTAMP;
ALTER TABLE gigs ADD COLUMN overdue_at TIMESTAMP;
ALTER TABLE gigs ADD COLUMN access_expires_at TIMESTAMP;
ALTER TABLE gigs ADD COLUMN access_expiry_source TEXT NOT NULL DEFAULT 'none';

ALTER TABLE gig_assignments ADD COLUMN access_expiry_source TEXT NOT NULL DEFAULT 'none';
ALTER TABLE template_versions ADD COLUMN default_access_grace_days INTEGER;

-- deadline_at remains a compatibility mirror of due_at during the migration
-- window. Existing dates become soft due dates.
UPDATE gigs SET due_at=deadline_at WHERE due_at IS NULL AND deadline_at IS NOT NULL;

-- Preserve independently configured expirations. The legacy implementation
-- always copied deadline_at exactly; equality is therefore the provenance
-- marker for an inherited expiry.
UPDATE gig_assignments
   SET access_expiry_source=CASE
       WHEN token_expires_at IS NULL THEN 'none'
       WHEN EXISTS (
         SELECT 1 FROM gigs g
          WHERE g.id=gig_assignments.gig_id
            AND g.deadline_at IS NOT NULL
            AND datetime(g.deadline_at)=datetime(gig_assignments.token_expires_at)
       ) THEN 'due'
       ELSE 'custom'
   END;

-- Active links inherited from the old deadline must remain operational after
-- upgrade. New relative-to-due expirations are opt-in.
UPDATE gig_assignments
   SET token_expires_at=NULL, access_expiry_source='none'
 WHERE gig_id IN (SELECT id FROM gigs WHERE status IN ('open','offered','accepted','submitted'))
   AND access_expiry_source='due';

-- Recover assignments revoked by the old deadline sweep. Matching the gig's
-- completed timestamp and inherited expiry avoids resurrecting older workers
-- that were withdrawn by reassignment or first-come completion.
UPDATE gig_assignments
   SET status=CASE
         WHEN EXISTS (SELECT 1 FROM gig_submissions s WHERE s.assignment_id=gig_assignments.id) THEN 'submitted'
         WHEN responded_at IS NOT NULL THEN 'accepted'
         ELSE 'offered'
       END,
       token_expires_at=NULL,
       token_revoked_at=NULL,
       access_expiry_source='none'
 WHERE status='withdrawn'
   AND gig_id IN (
     SELECT g.id FROM gigs g
      WHERE g.status='expired'
        AND EXISTS (
          SELECT 1 FROM gig_events e
           WHERE e.gig_id=g.id AND e.kind='expired' AND e.body='deadline elapsed'
        )
   )
   AND EXISTS (
     SELECT 1 FROM gigs g
      WHERE g.id=gig_assignments.gig_id
        AND g.completed_at IS NOT NULL
        AND token_revoked_at IS NOT NULL
        AND datetime(g.completed_at)=datetime(token_revoked_at)
        AND g.deadline_at IS NOT NULL
        AND token_expires_at IS NOT NULL
        AND datetime(g.deadline_at)=datetime(token_expires_at)
   );

UPDATE gigs
   SET status=CASE
         WHEN EXISTS (
           SELECT 1 FROM gig_assignments a
           JOIN gig_submissions s ON s.assignment_id=a.id
           WHERE a.gig_id=gigs.id
         ) THEN 'submitted'
         WHEN EXISTS (SELECT 1 FROM gig_assignments a WHERE a.gig_id=gigs.id AND a.status='accepted') THEN 'accepted'
         WHEN EXISTS (SELECT 1 FROM gig_assignments a WHERE a.gig_id=gigs.id AND a.status='offered') THEN 'offered'
         ELSE 'open'
       END,
       overdue_at=COALESCE(overdue_at, due_at, CURRENT_TIMESTAMP),
       completed_at=NULL,
       updated_at=CURRENT_TIMESTAMP
 WHERE status='expired'
   AND EXISTS (
     SELECT 1 FROM gig_events e
      WHERE e.gig_id=gigs.id AND e.kind='expired' AND e.body='deadline elapsed'
   )
   AND EXISTS (
     SELECT 1 FROM gig_assignments a
      WHERE a.gig_id=gigs.id
        AND a.status IN ('offered','accepted','submitted')
        AND a.token_revoked_at IS NULL
   );

INSERT INTO gig_events (project_id,gig_id,kind,actor,body)
SELECT g.project_id,g.id,'legacy_deadline_recovered','system','worker access restored during schedule/access migration'
  FROM gigs g
 WHERE g.overdue_at IS NOT NULL
   AND EXISTS (
     SELECT 1 FROM gig_events e
      WHERE e.gig_id=g.id AND e.kind='expired' AND e.body='deadline elapsed'
   )
   AND NOT EXISTS (
     SELECT 1 FROM gig_events e
      WHERE e.gig_id=g.id AND e.kind='legacy_deadline_recovered'
   );

CREATE INDEX ix_gigs_due ON gigs(project_id,status,due_at,overdue_at);
CREATE INDEX ix_gigs_scheduled ON gigs(project_id,scheduled_for);
CREATE INDEX ix_assignment_access ON gig_assignments(status,token_expires_at,token_revoked_at);
