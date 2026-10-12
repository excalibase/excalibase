package service

import "strings"

// realtimeIdentitySQL keeps every table in the realtime publication at
// REPLICA IDENTITY FULL. Without it a delete arrives with its key only, and the
// engine withholds it, since a subscriber's permission reads other columns.
// An ALTER TABLE that would leave a published table short of FULL is refused,
// and registration puts back FULL on any published table that lost it.
func realtimeIdentitySQL(publicationName string) string {
	if publicationName == "" {
		publicationName = DefaultPublicationName
	}
	return strings.ReplaceAll(realtimeIdentityTemplate, "{{PUBLICATION}}", sqlTextLiteral(publicationName))
}

const realtimeIdentityTemplate = `
DROP EVENT TRIGGER IF EXISTS excalibase_keep_realtime_identity;
DROP ROUTINE IF EXISTS excalibase.keep_realtime_identity();
CREATE FUNCTION excalibase.keep_realtime_identity()
RETURNS event_trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $fn$
DECLARE
  target_publication CONSTANT text := {{PUBLICATION}};
  published text;
BEGIN
  SELECT format('%I.%I', n.nspname, c.relname) INTO published
  FROM pg_event_trigger_ddl_commands() d
  JOIN pg_class c ON c.oid = d.objid
  JOIN pg_namespace n ON n.oid = c.relnamespace
  JOIN pg_publication_rel r ON r.prrelid = c.oid
  JOIN pg_publication p ON p.oid = r.prpubid
  WHERE d.classid = 'pg_catalog.pg_class'::regclass
    AND p.pubname = target_publication
    AND c.relreplident <> 'f'
  LIMIT 1;
  IF published IS NOT NULL THEN
    RAISE EXCEPTION 'table % has realtime on, which needs REPLICA IDENTITY FULL so deletes reach subscribers; turn realtime off for it first', published
      USING ERRCODE = 'object_in_use';
  END IF;
END
$fn$;
ALTER FUNCTION excalibase.keep_realtime_identity() OWNER TO CURRENT_USER;
REVOKE ALL ON FUNCTION excalibase.keep_realtime_identity() FROM PUBLIC;
CREATE EVENT TRIGGER excalibase_keep_realtime_identity ON ddl_command_end WHEN TAG IN ('ALTER TABLE')
  EXECUTE FUNCTION excalibase.keep_realtime_identity();

DO $excalibase_heal_realtime_identity$
DECLARE
  published record;
BEGIN
  FOR published IN
    SELECT n.nspname, c.relname
    FROM pg_publication p
    JOIN pg_publication_rel r ON r.prpubid = p.oid
    JOIN pg_class c ON c.oid = r.prrelid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE p.pubname = {{PUBLICATION}} AND c.relkind = 'r' AND c.relreplident <> 'f'
  LOOP
    EXECUTE format('ALTER TABLE %I.%I REPLICA IDENTITY FULL', published.nspname, published.relname);
  END LOOP;
END
$excalibase_heal_realtime_identity$;
`
