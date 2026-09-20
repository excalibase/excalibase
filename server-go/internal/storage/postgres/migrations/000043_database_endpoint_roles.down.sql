ALTER TABLE database_endpoints DROP CONSTRAINT IF EXISTS database_endpoints_role_known;
DELETE FROM database_endpoints WHERE role <> 'postgres';
ALTER TABLE database_endpoints DROP CONSTRAINT IF EXISTS database_endpoints_pkey;
ALTER TABLE database_endpoints DROP COLUMN IF EXISTS role;
ALTER TABLE database_endpoints ADD PRIMARY KEY (project_id);
