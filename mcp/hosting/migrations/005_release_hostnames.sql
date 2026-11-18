-- Released tenants give their hostname back, so its slug can be used again.
-- 2.0.0 cleared it on release but kept it on tenants deleted under 1.x.
UPDATE hosting_tenants SET default_hostname = NULL
WHERE status = 'released' AND default_hostname IS NOT NULL;
