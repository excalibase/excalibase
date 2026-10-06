package mcpserver

// countNeedsAggregations is the rule a page that shows totals runs into.
const countNeedsAggregations = "Counting rows (Prefer: count=exact) needs allowAggregations: true on the role's select permission; " +
	"without it every request that asks for a count answers 403 permission_denied \"Counting rows of <schema.table> is not permitted\"."

// restGuide is the REST surface a generated page uses, so a client need not
// read the engine's source to learn it.
func restGuide(urls map[string]string) map[string]any {
	return map[string]any{
		"tableUrl": urls["rest"] + "/<table>",
		"headers":  "Authorization: Bearer <accessToken from the token exchange>, X-Excalibase-Publishable-Key: <key>",
		"read": map[string]string{
			"select": "select=id,title or select=* ; embed a related table: select=id,author(name)",
			"order":  "order=created_at.desc or order=title.asc.nullslast ; comma-separate several",
			"page":   "limit=20&offset=40 (limit defaults to 30 and is capped by the server); or keyset first=20&after=<last value of the first order column>",
			"filter": "<column>=<op>.<value>: eq. neq. gt. gte. lt. lte. like. ilike. (* is the wildcard) in.(a,b) is.null is.true " +
				"and not. before any, e.g. completed=eq.false&id=in.(1,2,3)&title=not.is.null ; either of several: or=(status.eq.open,priority.gt.3)",
		},
		"write": map[string]string{
			"insert": "POST <table> with a JSON object, or an array for several rows",
			"upsert": "POST one object with Prefer: resolution=merge-duplicates (needs insert and update permission)",
			"update": "PATCH <table>?<filters> with the changed columns; at least one filter is required",
			"delete": "DELETE <table>?<filters>; at least one filter is required",
		},
		"prefer": map[string]string{
			"count=exact":           "GET adds pagination.total and a Content-Range header. " + countNeedsAggregations,
			"return=representation": "a write answers with the written rows in data; without it the body is empty",
			"tx=rollback":           "runs a write and rolls it back: a safe way to try a write with test_api_request",
		},
		"responses": map[string]string{
			"list":            `{"data":[...]} ; with Prefer: count=exact {"data":[...],"pagination":{"total":N,"limit":N,"offset":N}} ; with first/after {"data":[...],"pageInfo":{"hasNextPage":bool}}`,
			"write":           `POST 201, PATCH and DELETE 200; {"data":[...]} with return=representation`,
			"permission":      `403 {"code":"permission_denied","message":...}: the role lacks that operation or column (set_permission)`,
			"unknown":         `404 {"error":"Not found"}: no such table, or the role cannot select it`,
			"missingCountFix": "set_permission select with allowAggregations: true, or drop Prefer: count=exact",
		},
		"verify": "call test_api_request with the page's exact path, query and Prefer header before handing a page over",
	}
}
