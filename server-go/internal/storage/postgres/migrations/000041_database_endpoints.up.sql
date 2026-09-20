-- Public database endpoints (EXC-410).
--
-- A project reaches its own database inside the cluster with no public port
-- at all. A public endpoint exists only when the customer asks for one: every
-- exposed project gets a Kubernetes Service of type LoadBalancer carrying
-- MetalLB's shared-IP annotation and a TCP port of its own, so several
-- projects share one public address and Kubernetes does the routing.
--
-- One row per project that has ever touched the setting. No row means the
-- default: not public, no port held, TLS required.
--
--   port         the project's TCP port, NULL when it holds none. Kept across
--                a pause so a resume comes back on the same number.
--   public       whether the Service exists. Written only after the
--                Kubernetes object has been observed created or deleted.
--   require_tls  enforced in Postgres through pg_hba (hostssl only when on),
--                never at the edge. A setting of the project, not of the
--                port, so it survives the endpoint being turned off.
CREATE TABLE IF NOT EXISTS database_endpoints (
    project_id  TEXT PRIMARY KEY,
    port        INTEGER,
    public      BOOLEAN     NOT NULL DEFAULT FALSE,
    require_tls BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT database_endpoints_port_range CHECK (port IS NULL OR (port BETWEEN 1024 AND 65535))
);

-- Two projects must never hold the same port. This is the backstop behind the
-- allocator's advisory lock: even a caller that somehow skipped the lock
-- cannot commit a duplicate.
CREATE UNIQUE INDEX IF NOT EXISTS database_endpoints_port_key
    ON database_endpoints (port) WHERE port IS NOT NULL;

-- A freed port is not reusable immediately. A customer's application, a saved
-- connection in a GUI client or a forgotten cron job keeps dialling a port for
-- weeks after the project behind it is gone; if that port has been handed to
-- another tenant in the meantime, those clients arrive at a stranger's
-- database and present credentials to it. So a freed port sits here until the
-- configured window has passed, and the allocator skips anything it still
-- covers. Rows older than the window are inert and may be pruned.
CREATE TABLE IF NOT EXISTS database_endpoint_port_quarantine (
    port                INTEGER PRIMARY KEY,
    released_at         TIMESTAMPTZ NOT NULL,
    released_project_id TEXT        NOT NULL
);

-- The allocator's scan is "which ports are still quarantined", newest first.
CREATE INDEX IF NOT EXISTS idx_db_endpoint_quarantine_released
    ON database_endpoint_port_quarantine (released_at);
