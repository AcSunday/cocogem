package httpapi

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AcSunday/cocogem/internal/workflow"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func TestSourceUploadAcceptsMultipartWithCSRF(t *testing.T) {
	server, db, cookie, csrf := pagingServer(t, workflow.DefaultLimits())
	thread, _ := seedThread(t, db)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "notes.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("name,amount\nalpha,2\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "http://local.test/api/v1/threads/"+thread.ID+"/sources", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("source upload = %d: %s", recorder.Code, recorder.Body.String())
	}
	var imported w.SourceImport
	if err := json.Unmarshal(recorder.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.ParseStatus != "parsed" || imported.Kind != "csv" || imported.MediaType != "text/csv" {
		t.Fatalf("source import = %+v", imported)
	}
}
