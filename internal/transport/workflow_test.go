package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/center/CenterSurvey/internal/survey"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func mcpCaller(t *testing.T, h http.Handler) func(string, map[string]any) map[string]any {
	t.Helper()
	session := ""
	id := 0
	rpc := func(method string, params any) map[string]any {
		t.Helper()
		id++
		body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		if session != "" {
			r.Header.Set("Mcp-Session-Id", session)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if v := w.Header().Get("Mcp-Session-Id"); v != "" {
			session = v
		}
		var envelope map[string]any
		if err = json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("MCP HTTP %d: %s", w.Code, w.Body)
		}
		if envelope["error"] != nil {
			t.Fatalf("RPC error: %v", envelope["error"])
		}
		return envelope["result"].(map[string]any)
	}
	rpc("initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "workflow-test", "version": "1"}})
	return func(name string, args map[string]any) map[string]any {
		t.Helper()
		return rpc("tools/call", map[string]any{"name": name, "arguments": args})
	}
}
func TestMCPToPublicSurveyWorkflow(t *testing.T) {
	s, err := survey.Open(filepath.Join(t.TempDir(), "survey.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := Handler(s)
	call := mcpCaller(t, h)
	success := func(name string, args map[string]any) any {
		t.Helper()
		r := call(name, args)
		if r["isError"] == true {
			t.Fatalf("%s: %v", name, r)
		}
		return r["structuredContent"].(map[string]any)["data"]
	}
	v := success("create_survey", map[string]any{"title": "Your visit", "description": "Tell us about it"}).(map[string]any)
	id := v["id"].(string)
	request := func(method, path, body, key string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/public/surveys/"+id, "", ""); w.Code != 404 {
		t.Fatal("draft exposed")
	}
	success("add_text_question", map[string]any{"surveyId": id, "question": "Tell us more", "required": true})
	success("add_page", map[string]any{"surveyId": id, "title": "One more thing"})
	v = success("add_multiple_choice_question", map[string]any{"surveyId": id, "question": "How was it?", "options": []any{"Good", map[string]any{"label": "Great", "description": "Everything went well"}}, "required": true}).(map[string]any)
	items := v["items"].([]any)
	radio := items[0].(map[string]any)["id"].(string)
	text := items[2].(map[string]any)["id"].(string)
	success("update_question", map[string]any{"surveyId": id, "index": 2, "description": "Details help"})
	v = success("update_survey", map[string]any{"surveyId": id, "published": true}).(map[string]any)
	t.Setenv("SURVEY_BASE_URL", "https://example.com/mcp-123/")
	urls := success("get_public_urls", map[string]any{"surveyId": id}).(map[string]any)
	if urls["survey_url"] != "https://example.com/mcp-123/public/surveys/"+id || urls["is_template"] != false {
		t.Fatal(urls)
	}
	w := request("GET", "/public/surveys/"+id, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "survey-form") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "connect-src 'self'") {
		t.Fatal(w.Code, w.Body)
	}
	for _, path := range []string{"/public/survey.css", "/public/survey.js", "/api/v1/surveys/" + id} {
		if w := request("GET", path, "", ""); w.Code != 200 {
			t.Fatal(path, w.Code)
		}
	}
	body, _ := json.Marshal(map[string]any{"revision": v["revision"], "answers": map[string]any{radio: map[string]any{"values": []string{"Great"}}, text: map[string]any{"values": []string{"Lovely visit"}}}})
	w = request("POST", "/api/v1/surveys/"+id+"/responses", string(body), "once")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	first := w.Body.String()
	if strings.Contains(first, "Lovely visit") || strings.Contains(first, "answers") {
		t.Fatal("public receipt exposes answers")
	}
	if retry := request("POST", "/api/v1/surveys/"+id+"/responses", string(body), "once"); retry.Body.String() != first {
		t.Fatal("replay changed")
	}
	if w := request("GET", "/api/v1/surveys/"+id+"/responses", "", ""); w.Code != 405 && w.Code != 404 {
		t.Fatal("public responses readable")
	}
	responses := success("get_survey_responses", map[string]any{"surveyId": id}).([]any)
	if len(responses) != 1 {
		t.Fatal(responses)
	}
	success("delete_question", map[string]any{"surveyId": id, "index": 0})
	for _, args := range []map[string]any{{"surveyId": id, "index": -1}, {"surveyId": id, "index": 1.5}, {"surveyId": id}, {"surveyId": id, "index": 0, "extra": true}} {
		if r := call("delete_question", args); r["isError"] != true {
			t.Fatal("invalid schema allowed", r)
		}
	}
	success("delete_survey", map[string]any{"surveyId": id})
	if w := request("GET", "/api/v1/surveys/"+id, "", ""); w.Code != 404 {
		t.Fatal("deleted survey visible")
	}
}
func TestPublicSubmissionRejectsInvalidJSONAndStaleRevision(t *testing.T) {
	s, err := survey.Open(filepath.Join(t.TempDir(), "survey.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	v, err := s.Create(ctx, "Survey", "")
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	v, err = s.Update(ctx, v.ID, nil, nil, &yes)
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(s)
	for _, body := range []string{`{}`, `{"revision":2,"answers":{},"extra":true}`, `{"revision":2,"answers":{}} {}`, `{"revision":2,"answers":{"bad":{"values":[],"extra":true}}}`} {
		r := httptest.NewRequest("POST", "/api/v1/surveys/"+v.ID+"/responses", strings.NewReader(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(body, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/api/v1/surveys/"+v.ID+"/responses", strings.NewReader(`{"revision":1,"answers":{}}`))
	r.Header.Set("Idempotency-Key", "stale")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
}
func TestPublicURLConfigurationAndPrefixes(t *testing.T) {
	for _, tc := range []struct {
		base, want string
		template   bool
	}{
		{"", "https://centermcp.com/<mcp-id>/public/surveys/abc", true},
		{" https://example.com/instance/// ", "https://example.com/instance/public/surveys/abc", false},
		{"http://localhost:8000", "http://localhost:8000/public/surveys/abc", false},
	} {
		t.Setenv("SURVEY_BASE_URL", tc.base)
		v, err := PublicURL("abc")
		if err != nil || v["survey_url"] != tc.want || v["is_template"] != tc.template {
			t.Fatal(v, err)
		}
	}
	for _, base := range []string{"example.com", "javascript:alert(1)", "https://user:secret@example.com", "https://example.com?a=1", "https://example.com#fragment"} {
		t.Setenv("SURVEY_BASE_URL", base)
		if _, err := PublicURL("abc"); err == nil {
			t.Fatal("invalid URL allowed")
		}
	}
	page, _ := url.Parse("https://example.com/instance/public/surveys/abc")
	for rel, want := range map[string]string{"../survey.js": "/instance/public/survey.js", "../../api/v1/surveys/abc": "/instance/api/v1/surveys/abc"} {
		u, err := page.Parse(rel)
		if err != nil || u.Path != want {
			t.Fatal(u, err)
		}
	}
}
