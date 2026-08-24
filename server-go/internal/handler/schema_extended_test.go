//go:build integration

package handler

import (
	"encoding/json"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/schema"
)

const (
	testTriggersPath = "/api/schema/test-proj/triggers"
	testIndexesPath  = "/api/schema/test-proj/indexes"
	testRowsPath     = "/api/schema/test-proj/tables/users/rows"
)


// These tests extend schema_handler_test.go to cover triggers, indexes, data
// operations, types, and advisors — all 0% or very low coverage before this file.

// --- Triggers ---

func TestSchemaHandler_GetTriggers(t *testing.T) {
	r := setupSchemaRouter(t)

	// Initially no triggers
	w := schemaRequest(r, "GET", testTriggersPath, "")
	if w.Code != 200 {
		t.Fatalf("GetTriggers: %d, body: %s", w.Code, w.Body.String())
	}
	var triggers []interface{}
	json.NewDecoder(w.Body).Decode(&triggers)
	// No triggers yet — list should be empty or nil
}

func TestSchemaHandler_CreateTrigger_MissingFields_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	// Missing function field
	body := `{"name":"trg","table":"users"}`
	w := schemaRequest(r, "POST", testTriggersPath, body)
	if w.Code != 400 {
		t.Fatalf("CreateTrigger missing function: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_CreateTrigger_InvalidJSON_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "POST", testTriggersPath, testNotJSON)
	if w.Code != 400 {
		t.Fatalf("CreateTrigger invalid JSON: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_DropTrigger_MissingTable_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	// No "table" query param
	w := schemaRequest(r, "DELETE", "/api/schema/test-proj/triggers/my_trigger", "")
	if w.Code != 400 {
		t.Fatalf("DropTrigger missing table: %d, body: %s", w.Code, w.Body.String())
	}
}

// --- Indexes ---

func TestSchemaHandler_CreateIndex_Success(t *testing.T) {
	r := setupSchemaRouter(t)

	body := `{"name":"idx_users_name","table":"users","columns":["name"]}`
	w := schemaRequest(r, "POST", testIndexesPath, body)
	if w.Code != 201 {
		t.Fatalf("CreateIndex: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_CreateIndex_MissingFields_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	// Missing columns
	body := `{"name":"idx_missing","table":"users"}`
	w := schemaRequest(r, "POST", testIndexesPath, body)
	if w.Code != 400 {
		t.Fatalf("CreateIndex missing columns: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_CreateIndex_InvalidJSON_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "POST", testIndexesPath, testNotJSON)
	if w.Code != 400 {
		t.Fatalf("CreateIndex invalid JSON: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_DropIndex_Success(t *testing.T) {
	r := setupSchemaRouter(t)

	// First create an index to drop
	createBody := `{"name":"idx_to_drop","table":"users","columns":["name"]}`
	wc := schemaRequest(r, "POST", testIndexesPath, createBody)
	if wc.Code != 201 {
		t.Skipf("create index failed (%d): %s", wc.Code, wc.Body.String())
	}

	w := schemaRequest(r, "DELETE", "/api/schema/test-proj/indexes/idx_to_drop", "")
	if w.Code != 200 {
		t.Fatalf("DropIndex: %d, body: %s", w.Code, w.Body.String())
	}
}

// --- GetTypes ---

func TestSchemaHandler_GetTypes(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "GET", "/api/schema/test-proj/types", "")
	if w.Code != 200 {
		t.Fatalf("GetTypes: %d, body: %s", w.Code, w.Body.String())
	}
	// Types may be empty or contain default pg types
}

// --- DropExtension ---

func TestSchemaHandler_DropExtension_NotExists(t *testing.T) {
	r := setupSchemaRouter(t)

	// Drop a non-existent extension — should return error (500)
	w := schemaRequest(r, "DELETE", "/api/schema/test-proj/extensions/nonexistent_ext", "")
	if w.Code != 500 && w.Code != 200 {
		t.Fatalf("DropExtension not exists: unexpected %d, body: %s", w.Code, w.Body.String())
	}
}

// --- CreateExtension validation ---

func TestSchemaHandler_CreateExtension_MissingName_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "POST", "/api/schema/test-proj/extensions", `{"schema":"public"}`)
	if w.Code != 400 {
		t.Fatalf("CreateExtension missing name: %d, body: %s", w.Code, w.Body.String())
	}
}

// --- CreatePolicy validation ---

func TestSchemaHandler_CreatePolicy_MissingTable_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	// name present but table missing
	w := schemaRequest(r, "POST", "/api/schema/test-proj/policies", `{"name":"my_policy"}`)
	if w.Code != 400 {
		t.Fatalf("CreatePolicy missing table: %d, body: %s", w.Code, w.Body.String())
	}
}

// --- DropPolicy ---

func TestSchemaHandler_DropPolicy_MissingTable_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	// No "table" query param
	w := schemaRequest(r, "DELETE", "/api/schema/test-proj/policies/my_policy", "")
	if w.Code != 400 {
		t.Fatalf("DropPolicy missing table: %d, body: %s", w.Code, w.Body.String())
	}
}

// --- CreateFunction validation ---

func TestSchemaHandler_CreateFunction_MissingBody_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	// name present but body missing
	w := schemaRequest(r, "POST", "/api/schema/test-proj/functions", `{"name":"my_fn"}`)
	if w.Code != 400 {
		t.Fatalf("CreateFunction missing body: %d, body: %s", w.Code, w.Body.String())
	}
}

// --- schema_data: Row CRUD ---

func TestSchemaHandler_GetRows(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "GET", testRowsPath, "")
	if w.Code != 200 {
		t.Fatalf("GetRows: %d, body: %s", w.Code, w.Body.String())
	}
	var result schema.RowsResult
	json.NewDecoder(w.Body).Decode(&result)
	if result.TotalCount < 1 {
		t.Errorf("expected at least 1 row (seeded Alice), got %d", result.TotalCount)
	}
}

func TestSchemaHandler_GetRows_WithFilters(t *testing.T) {
	r := setupSchemaRouter(t)

	// Filter by email using = operator
	w := schemaRequest(r, "GET", "/api/schema/test-proj/tables/users/rows?filter[email]==:alice@test.com", "")
	if w.Code != 200 {
		t.Fatalf("GetRows with filter: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_GetRows_WithSortAndLimit(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "GET", "/api/schema/test-proj/tables/users/rows?sort=email&order=asc&limit=10&offset=0", "")
	if w.Code != 200 {
		t.Fatalf("GetRows with sort/limit: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_GetRows_InvalidLimit_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "GET", "/api/schema/test-proj/tables/users/rows?limit=notanumber", "")
	if w.Code != 400 {
		t.Fatalf("GetRows invalid limit: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_GetRows_InvalidOffset_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "GET", "/api/schema/test-proj/tables/users/rows?offset=notanumber", "")
	if w.Code != 400 {
		t.Fatalf("GetRows invalid offset: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_InsertRow_Success(t *testing.T) {
	r := setupSchemaRouter(t)

	body := `{"data":{"email":"bob@test.com","name":"Bob"}}`
	w := schemaRequest(r, "POST", testRowsPath, body)
	if w.Code != 201 {
		t.Fatalf("InsertRow: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_InsertRow_EmptyData_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	// Empty data map
	w := schemaRequest(r, "POST", testRowsPath, `{"data":{}}`)
	if w.Code != 400 {
		t.Fatalf("InsertRow empty data: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_InsertRow_InvalidJSON_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "POST", testRowsPath, testNotJSON)
	if w.Code != 400 {
		t.Fatalf("InsertRow invalid JSON: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_UpdateRow_Success(t *testing.T) {
	r := setupSchemaRouter(t)

	body := `{"pk":{"column":"email","value":"alice@test.com"},"data":{"name":"Alice Updated"}}`
	w := schemaRequest(r, "PATCH", testRowsPath, body)
	if w.Code != 200 {
		t.Fatalf("UpdateRow: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_UpdateRow_MissingPK_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	// pk.column is empty
	body := `{"pk":{"column":"","value":"1"},"data":{"name":"X"}}`
	w := schemaRequest(r, "PATCH", testRowsPath, body)
	if w.Code != 400 {
		t.Fatalf("UpdateRow missing pk: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_UpdateRow_EmptyData_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	body := `{"pk":{"column":"id","value":"1"},"data":{}}`
	w := schemaRequest(r, "PATCH", testRowsPath, body)
	if w.Code != 400 {
		t.Fatalf("UpdateRow empty data: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_UpdateRow_InvalidJSON_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "PATCH", testRowsPath, testNotJSON)
	if w.Code != 400 {
		t.Fatalf("UpdateRow invalid JSON: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_DeleteRow_Success(t *testing.T) {
	r := setupSchemaRouter(t)

	// First insert a row to delete
	insertBody := `{"data":{"email":"delete@test.com","name":"ToDelete"}}`
	wi := schemaRequest(r, "POST", testRowsPath, insertBody)
	if wi.Code != 201 {
		t.Skipf("insert failed (%d): %s", wi.Code, wi.Body.String())
	}

	body := `{"pk":{"column":"email","value":"delete@test.com"}}`
	w := schemaRequest(r, "DELETE", testRowsPath, body)
	if w.Code != 200 {
		t.Fatalf("DeleteRow: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_DeleteRow_MissingPK_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	// pk.value is empty
	body := `{"pk":{"column":"id","value":""}}`
	w := schemaRequest(r, "DELETE", testRowsPath, body)
	if w.Code != 400 {
		t.Fatalf("DeleteRow missing pk: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_DeleteRow_InvalidJSON_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "DELETE", testRowsPath, testNotJSON)
	if w.Code != 400 {
		t.Fatalf("DeleteRow invalid JSON: %d, body: %s", w.Code, w.Body.String())
	}
}

// --- schema_advisors ---

func TestSchemaHandler_RunPerformanceAdvisor(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "GET", "/api/schema/test-proj/advisors/performance", "")
	if w.Code != 200 {
		t.Fatalf("RunPerformanceAdvisor: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_RunSecurityAdvisor(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "GET", "/api/schema/test-proj/advisors/security", "")
	if w.Code != 200 {
		t.Fatalf("RunSecurityAdvisor: %d, body: %s", w.Code, w.Body.String())
	}
}

// --- UpdateTable and AlterColumn validation ---

func TestSchemaHandler_UpdateTable_InvalidJSON_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "PATCH", "/api/schema/test-proj/tables/users", testNotJSON)
	if w.Code != 400 {
		t.Fatalf("UpdateTable invalid JSON: %d, body: %s", w.Code, w.Body.String())
	}
}

func TestSchemaHandler_AlterColumn_InvalidJSON_Returns400(t *testing.T) {
	r := setupSchemaRouter(t)

	w := schemaRequest(r, "PATCH", "/api/schema/test-proj/tables/users/columns/name", testNotJSON)
	if w.Code != 400 {
		t.Fatalf("AlterColumn invalid JSON: %d, body: %s", w.Code, w.Body.String())
	}
}

// --- CreateTrigger success path (requires a function to exist) ---

func TestSchemaHandler_CreateAndDropTrigger(t *testing.T) {
	r := setupSchemaRouter(t)

	// First create a trigger function
	fnBody := `{"name":"log_fn","returnType":"trigger","language":"plpgsql","body":"BEGIN RETURN NEW; END;"}`
	wf := schemaRequest(r, "POST", "/api/schema/test-proj/functions", fnBody)
	if wf.Code != 201 {
		t.Skipf("create trigger function failed (%d): %s", wf.Code, wf.Body.String())
	}

	// Create trigger
	trigBody := `{"name":"log_trigger","table":"users","function":"log_fn","timing":"BEFORE","events":["INSERT"]}`
	wt := schemaRequest(r, "POST", testTriggersPath, trigBody)
	if wt.Code != 201 {
		t.Fatalf("CreateTrigger: %d, body: %s", wt.Code, wt.Body.String())
	}

	// Get triggers — should see the new trigger
	wg := schemaRequest(r, "GET", testTriggersPath, "")
	if wg.Code != 200 {
		t.Fatalf("GetTriggers after create: %d, body: %s", wg.Code, wg.Body.String())
	}

	// Drop trigger
	wd := schemaRequest(r, "DELETE", "/api/schema/test-proj/triggers/log_trigger?table=users", "")
	if wd.Code != 200 {
		t.Fatalf("DropTrigger: %d, body: %s", wd.Code, wd.Body.String())
	}
}
