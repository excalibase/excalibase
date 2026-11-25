-- A single host's database serves no TLS (EXC-576). Rows created before the
-- mode was recorded carry an empty one. Only those are set, so it can run twice.
UPDATE database_instances SET ssl_mode = 'disable' WHERE deployment_mode = 'docker' AND COALESCE(ssl_mode, '') = '';
