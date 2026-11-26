DROP TABLE IF EXISTS trust_verifications;
DROP TRIGGER IF EXISTS trg_release_trust_claims ON users;
DROP FUNCTION IF EXISTS release_trust_claims();
DROP TABLE IF EXISTS trust_claims;
ALTER TABLE studio_oauth_states DROP COLUMN IF EXISTS user_id;
ALTER TABLE studio_oauth_states DROP COLUMN IF EXISTS purpose;
