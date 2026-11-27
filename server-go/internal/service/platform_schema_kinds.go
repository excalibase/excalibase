package service

import "strings"

// schemaObjectKind is one catalog whose rows live in a schema. The probe, the
// quarantine and the refusal are all built from schemaObjectKinds, so a kind
// cannot be seen by one and missed by another (EXC-821). A test lists every
// catalog with a namespace column and fails when one is in neither
// schemaObjectKinds nor schemaCatalogsNotObjects.
type schemaObjectKind struct {
	catalog, alias, namespaceColumn string
	// owner is the SQL for the owning role; empty when the kind has none, which
	// means only a superuser can create it.
	owner string
	// where narrows the rows that count as objects of this kind.
	where string
	move  *schemaObjectMove
}

// schemaObjectMove says how the quarantine moves a kind: the ALTER keyword, the
// object's name as ALTER takes it, and whether that name is taken in the
// quarantine schema {{QNS}}. A kind with no move is only refused.
type schemaObjectMove struct {
	where, kind, ident, collides string
}

// qualifiedName is the schema-qualified, quoted name of a row.
func qualifiedName(nameColumn, namespaceColumn string) string {
	return "(SELECT quote_ident(n.nspname) || '.' || quote_ident({a}." + nameColumn + ") FROM pg_namespace n WHERE n.oid = {a}." + namespaceColumn + ")"
}

// sameNameIn is a check that the quarantine schema has a row of the same name.
func sameNameIn(catalog, namespaceColumn string, columns ...string) string {
	var match strings.Builder
	for _, column := range columns {
		match.WriteString(" AND x." + column + " = {a}." + column)
	}
	return "EXISTS (SELECT 1 FROM " + catalog + " x WHERE x." + namespaceColumn + " = {{QNS}}" + match.String() + ")"
}

var schemaObjectKinds = []schemaObjectKind{
	{catalog: "pg_class", alias: "c", namespaceColumn: "relnamespace", owner: "c.relowner", where: "c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S', 'c')",
		move: &schemaObjectMove{where: "c.relkind = 'c'", kind: "'TYPE'", ident: "c.reltype::regtype::text",
			collides: "EXISTS (SELECT 1 FROM pg_type x WHERE x.typnamespace = {{QNS}} AND x.typname = c.relname)"}},
	{catalog: "pg_proc", alias: "p", namespaceColumn: "pronamespace", owner: "p.proowner",
		move: &schemaObjectMove{kind: "'ROUTINE'", ident: "p.oid::regprocedure::text", collides: sameNameIn("pg_proc", "pronamespace", "proname", "proargtypes")}},
	{catalog: "pg_type", alias: "t", namespaceColumn: "typnamespace", owner: typeOwnerOf("t"), where: "t.typrelid = 0 AND t.typcategory <> 'A'",
		move: &schemaObjectMove{kind: "CASE t.typtype WHEN 'd' THEN 'DOMAIN' ELSE 'TYPE' END", ident: "t.oid::regtype::text", collides: sameNameIn("pg_type", "typnamespace", "typname")}},
	{catalog: "pg_operator", alias: "op", namespaceColumn: "oprnamespace", owner: "op.oprowner",
		move: &schemaObjectMove{kind: "'OPERATOR'",
			ident:    "format('%I.%s (%s, %s)', (SELECT nspname FROM pg_namespace WHERE oid = {a}.oprnamespace), {a}.oprname, coalesce(NULLIF({a}.oprleft, 0)::regtype::text, 'NONE'), coalesce(NULLIF({a}.oprright, 0)::regtype::text, 'NONE'))",
			collides: sameNameIn("pg_operator", "oprnamespace", "oprname", "oprleft", "oprright")}},
	{catalog: "pg_opfamily", alias: "opf", namespaceColumn: "opfnamespace", owner: "opf.opfowner",
		move: &schemaObjectMove{kind: "'OPERATOR FAMILY'",
			ident:    "format('%s USING %s', " + qualifiedName("opfname", "opfnamespace") + ", (SELECT amname FROM pg_am WHERE oid = {a}.opfmethod))",
			collides: sameNameIn("pg_opfamily", "opfnamespace", "opfname", "opfmethod")}},
	{catalog: "pg_opclass", alias: "opc", namespaceColumn: "opcnamespace", owner: "opc.opcowner",
		move: &schemaObjectMove{kind: "'OPERATOR CLASS'",
			ident:    "format('%s USING %s', " + qualifiedName("opcname", "opcnamespace") + ", (SELECT amname FROM pg_am WHERE oid = {a}.opcmethod))",
			collides: sameNameIn("pg_opclass", "opcnamespace", "opcname", "opcmethod")}},
	{catalog: "pg_collation", alias: "col", namespaceColumn: "collnamespace", owner: "col.collowner",
		move: &schemaObjectMove{kind: "'COLLATION'", ident: qualifiedName("collname", "collnamespace"), collides: sameNameIn("pg_collation", "collnamespace", "collname", "collencoding")}},
	{catalog: "pg_conversion", alias: "cv", namespaceColumn: "connamespace", owner: "cv.conowner",
		move: &schemaObjectMove{kind: "'CONVERSION'", ident: qualifiedName("conname", "connamespace"), collides: sameNameIn("pg_conversion", "connamespace", "conname")}},
	{catalog: "pg_ts_config", alias: "tsc", namespaceColumn: "cfgnamespace", owner: "tsc.cfgowner",
		move: &schemaObjectMove{kind: "'TEXT SEARCH CONFIGURATION'", ident: qualifiedName("cfgname", "cfgnamespace"), collides: sameNameIn("pg_ts_config", "cfgnamespace", "cfgname")}},
	{catalog: "pg_ts_dict", alias: "tsd", namespaceColumn: "dictnamespace", owner: "tsd.dictowner",
		move: &schemaObjectMove{kind: "'TEXT SEARCH DICTIONARY'", ident: qualifiedName("dictname", "dictnamespace"), collides: sameNameIn("pg_ts_dict", "dictnamespace", "dictname")}},
	// A parser and a template have no owner: only a superuser can create either.
	{catalog: "pg_ts_parser", alias: "tsp", namespaceColumn: "prsnamespace"},
	{catalog: "pg_ts_template", alias: "tst", namespaceColumn: "tmplnamespace"},
	// An extension is not moved: relocating it can fail or break what depends on it.
	{catalog: "pg_extension", alias: "ext", namespaceColumn: "extnamespace", owner: "ext.extowner"},
	{catalog: "pg_statistic_ext", alias: "st", namespaceColumn: "stxnamespace", owner: "st.stxowner",
		move: &schemaObjectMove{kind: "'STATISTICS'", ident: qualifiedName("stxname", "stxnamespace"), collides: sameNameIn("pg_statistic_ext", "stxnamespace", "stxname")}},
}

// schemaCatalogsNotObjects are catalogs with a namespace column that hold no
// object a customer role can own in a platform schema, with why.
var schemaCatalogsNotObjects = map[string]string{
	"pg_namespace":             "the schema itself, owner reset by the lockdown",
	"pg_default_acl":           "default privileges, cleared by the lockdown",
	"pg_publication_namespace": "a schema's membership of a publication, not an object",
	"pg_constraint":            "a constraint belongs to its table, whose definition the taint check reads",
}

// For pg_event_trigger, which has no namespace: ALTER EVENT TRIGGER ... OWNER
// TO needs a superuser as the new owner, so a customer role cannot own one.

func forKind(kind schemaObjectKind, expr string) string {
	return strings.ReplaceAll(expr, "{a}", kind.alias)
}

// ownedObjectsSQL lists the owner of every object in {{NS}}, of every kind that
// has an owner.
func ownedObjectsSQL() string {
	var selects []string
	for _, kind := range schemaObjectKinds {
		if kind.owner == "" {
			continue
		}
		query := "SELECT " + kind.owner + " AS owner FROM " + kind.catalog + " " + kind.alias + " WHERE " + kind.alias + "." + kind.namespaceColumn + " = {{NS}}"
		if kind.where != "" {
			query += " AND " + kind.where
		}
		selects = append(selects, query)
	}
	return "(\n    " + strings.Join(selects, "\n    UNION ALL ") + "\n  )"
}

// movableObjectsSQL is the owners of the kinds the quarantine moves.
func movableObjectsSQL() string {
	var selects []string
	for _, kind := range schemaObjectKinds {
		if kind.move == nil {
			continue
		}
		selects = append(selects, "SELECT "+kind.owner+" AS owner FROM "+kind.catalog+" "+kind.alias+" WHERE "+kind.alias+"."+kind.namespaceColumn+" = {{NS}}"+moveFilter(kind))
	}
	return "(\n    " + strings.Join(selects, "\n    UNION ALL ") + "\n  )"
}

func moveFilter(kind schemaObjectKind) string {
	switch {
	case kind.move.where != "":
		return " AND " + kind.move.where
	case kind.where != "":
		return " AND " + kind.where
	}
	return ""
}

// movableWithTargetsSQL is each movable object as the statement that moves it,
// with whether its name is taken in the quarantine schema.
func movableWithTargetsSQL() string {
	var selects []string
	for _, kind := range schemaObjectKinds {
		if kind.move == nil {
			continue
		}
		selects = append(selects, forKind(kind, "SELECT "+kind.move.kind+" AS kind, "+kind.move.ident+" AS ident, {a}.oid AS object_oid, "+kind.owner+" AS owner, "+
			kind.move.collides+" AS collides FROM "+kind.catalog+" {a} WHERE {a}."+kind.namespaceColumn+" = {{NS}}"+moveFilter(kind)))
	}
	return "(\n    " + strings.Join(selects, "\n    UNION ALL ") + "\n  )"
}

// schemaScopedCatalogs are the catalogs the repair covers, for the test that
// every catalog with a namespace column is one of them.
func schemaScopedCatalogs() []string {
	catalogs := make([]string, 0, len(schemaObjectKinds)+len(schemaCatalogsNotObjects))
	for _, kind := range schemaObjectKinds {
		catalogs = append(catalogs, kind.catalog)
	}
	for catalog := range schemaCatalogsNotObjects {
		catalogs = append(catalogs, catalog)
	}
	return catalogs
}
