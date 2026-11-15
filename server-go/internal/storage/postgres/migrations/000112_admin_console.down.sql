DROP INDEX IF EXISTS idx_users_created_at;
ALTER TABLE users DROP COLUMN IF EXISTS last_login_ip;
ALTER TABLE users DROP COLUMN IF EXISTS last_login_at;
DROP TABLE IF EXISTS abuse_suspension_apps;
-- abuse_suspensions is kept: prod held rows in it before this migration existed.
DROP INDEX IF EXISTS idx_abuse_suspensions_at;
