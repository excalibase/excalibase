-- PgDog is not part of the platform: tenant databases are reached through
-- their own load-balanced endpoint, so its route table and the plaintext
-- tenant passwords it held are dropped.
DROP TABLE IF EXISTS pgdog_users;
DROP TABLE IF EXISTS pgdog_databases;
