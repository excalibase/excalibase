-- A project may exist without a database (EXC-426): its namespace, quota and
-- apps are the project, and a database is added later. Every existing row was
-- created with its database, so they start as false. The default is dropped
-- afterwards so every insert states which kind of project it is.
ALTER TABLE database_instances ADD COLUMN no_database BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE database_instances ALTER COLUMN no_database DROP DEFAULT;
