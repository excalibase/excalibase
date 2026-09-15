-- Service identity (EXC-365).
--
-- users.kind separates human accounts from service principals. A service
-- principal owns capability tokens and nothing else: it cannot log in, cannot
-- register and is never added to an organization. Existing rows are humans.
--
-- access_tokens.permissions holds the capability list of one token, each entry
-- shaped <resource>:<action>[:<selector>] (for example "policies:read" or
-- "vault:read:pki/signing/*"). A token with a non-empty list reaches only the
-- handlers that declare a matching capability, regardless of the owning user's
-- platform role. NULL / empty keeps the pre-existing behaviour of a human PAT.
--
-- Nothing is seeded here: service principals are created through
-- POST /api/admin/service-accounts by a platform admin.
ALTER TABLE users ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'human';

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_kind_check;
ALTER TABLE users ADD CONSTRAINT users_kind_check CHECK (kind IN ('human', 'service'));

CREATE INDEX IF NOT EXISTS idx_users_kind ON users (kind);

ALTER TABLE access_tokens ADD COLUMN IF NOT EXISTS permissions TEXT[];
