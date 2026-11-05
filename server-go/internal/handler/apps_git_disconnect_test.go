package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// A disconnect that deletes no connection row is a 404 and cancels nothing; one that deletes it is a 204.
func TestAppGit_DisconnectWithoutAConnectionIsNotFound(t *testing.T) {
	for name, tc := range map[string]struct {
		deleted int64
		want    int
	}{
		"nothing connected": {0, http.StatusNotFound},
		"connected":         {1, http.StatusNoContent},
	} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		mock.ExpectExec(`DELETE FROM app_git_connections`).WithArgs("p1", "a1").WillReturnResult(sqlmock.NewResult(0, tc.deleted))
		if tc.deleted > 0 {
			mock.ExpectExec(`UPDATE`).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		service := sitebuilds.NewService(sitebuilds.NewPostgresStore(db), nil, nil, nil, nil, sitebuilds.Options{Mode: sitebuilds.ModeOff})
		recorder := httptest.NewRecorder()
		appGitRouter(service, false).ServeHTTP(recorder, gitRequest("DELETE", "/api/projects/p1/apps/a1/git", ""))
		if recorder.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", name, recorder.Code, recorder.Body.String(), tc.want)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		_ = db.Close()
	}
}
