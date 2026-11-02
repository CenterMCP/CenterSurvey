package survey

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "survey.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func pointer[T any](v T) *T { return &v }
func TestSurveyLifecycleAndSnapshots(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	v, err := s.Create(ctx, "Customer feedback", "Tell us more")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(ctx, v.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatal("draft exposed", err)
	}
	v, err = s.AddItem(ctx, v.ID, Item{Type: "text", Title: "Your thoughts", Required: true})
	if err != nil {
		t.Fatal(err)
	}
	textID := v.Items[0].ID
	v, err = s.AddItem(ctx, v.ID, Item{Type: "radio", Title: "Rating", Required: true, Options: []Option{{Label: "Good"}, {Label: "Great"}}, IncludeOther: true})
	if err != nil {
		t.Fatal(err)
	}
	if v.Items[1].ID != textID {
		t.Fatal("items must prepend and IDs must remain stable")
	}
	choiceID := v.Items[0].ID
	v, err = s.UpdateItem(ctx, v.ID, 1, Patch{Description: pointer("Be candid")})
	if err != nil {
		t.Fatal(err)
	}
	if v.Items[1].Title != "Your thoughts" || !v.Items[1].Required {
		t.Fatal("patch lost omitted fields")
	}
	v, err = s.Update(ctx, v.ID, nil, nil, pointer(true))
	if err != nil {
		t.Fatal(err)
	}
	input := Submission{Revision: v.Revision, Answers: map[string]Answer{textID: {Values: []string{"Loved it"}}, choiceID: {Values: []string{"Great"}}}}
	first, err := s.Submit(ctx, v.ID, "submit-1", input)
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.DeleteItem(ctx, v.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.Update(ctx, v.ID, nil, nil, pointer(false))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Submit(ctx, v.ID, "submit-1", input)
	if err != nil || replay.ID != first.ID {
		t.Fatal("retry after close must return receipt", err)
	}
	if _, err = s.Submit(ctx, v.ID, "new-key", input); !errors.Is(err, ErrConflict) {
		t.Fatal("closed/stale survey accepted", err)
	}
	input.Answers[textID] = Answer{Values: []string{"Changed"}}
	if _, err = s.Submit(ctx, v.ID, "submit-1", input); !errors.Is(err, ErrConflict) {
		t.Fatal("changed retry accepted", err)
	}
	records, err := s.Responses(ctx, v.ID, 1)
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	if len(records[0].Snapshot.Items) != 2 || records[0].Snapshot.Items[0].ID != choiceID {
		t.Fatal("historic survey changed")
	}
	if err = s.Delete(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.DB.QueryRow("SELECT count(*) FROM responses").Scan(&count); err != nil || count != 0 {
		t.Fatal("responses were not cascaded", err)
	}
}
func TestAnswerValidationAndRollback(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	v, err := s.Create(ctx, "Validation", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []Item{
		{Type: "text", Title: "Text", Required: true},
		{Type: "radio", Title: "Radio", Required: true, Options: []Option{{Label: "A"}, {Label: "B"}}, IncludeOther: true},
		{Type: "checkbox", Title: "Checkbox", Options: []Option{{Label: "X"}, {Label: "Y"}}},
		{Type: "section", Title: "Section"},
	} {
		v, err = s.AddItem(ctx, v.ID, item)
		if err != nil {
			t.Fatal(err)
		}
	}
	v, err = s.Update(ctx, v.ID, nil, nil, pointer(true))
	if err != nil {
		t.Fatal(err)
	}
	section, check, radio, text := v.Items[0].ID, v.Items[1].ID, v.Items[2].ID, v.Items[3].ID
	good := func() map[string]Answer {
		return map[string]Answer{text: {Values: []string{"Hello"}}, radio: {Values: []string{"A"}}, check: {Values: []string{"X", "Y"}}}
	}
	cases := []struct {
		name   string
		change func(map[string]Answer)
	}{
		{"missing required", func(a map[string]Answer) { delete(a, text) }},
		{"blank required", func(a map[string]Answer) { a[text] = Answer{Values: []string{" "}} }},
		{"multiple text", func(a map[string]Answer) { a[text] = Answer{Values: []string{"One", "Two"}} }},
		{"unknown question", func(a map[string]Answer) { a["unknown"] = Answer{Values: []string{"One"}} }},
		{"section answer", func(a map[string]Answer) { a[section] = Answer{Values: []string{"One"}} }},
		{"unknown choice", func(a map[string]Answer) { a[radio] = Answer{Values: []string{"C"}} }},
		{"multiple radio", func(a map[string]Answer) { a[radio] = Answer{Values: []string{"A", "B"}} }},
		{"radio plus other", func(a map[string]Answer) { a[radio] = Answer{Values: []string{"A"}, Other: "Other"} }},
		{"duplicate checkbox", func(a map[string]Answer) { a[check] = Answer{Values: []string{"X", "X"}} }},
		{"disallowed other", func(a map[string]Answer) { a[check] = Answer{Other: "Z"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := good()
			tc.change(a)
			if _, err := s.Submit(ctx, v.ID, "invalid-"+tc.name, Submission{Revision: v.Revision, Answers: a}); !errors.Is(err, ErrInvalid) {
				t.Fatal(err)
			}
		})
	}
	var count int
	if err = s.DB.QueryRow("SELECT count(*) FROM responses").Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid submission persisted", err)
	}
	a := good()
	a[radio] = Answer{Other: "Something else"}
	if _, err = s.Submit(ctx, v.ID, "other", Submission{Revision: v.Revision, Answers: a}); err != nil {
		t.Fatal(err)
	}
	before := v.Revision
	if _, err = s.UpdateItem(ctx, v.ID, 2, Patch{Options: &[]Option{{Label: "A"}, {Label: "A"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("duplicate choices allowed", err)
	}
	after, err := s.Get(ctx, v.ID, false)
	if err != nil || after.Revision != before {
		t.Fatal("invalid edit mutated survey", err)
	}
}
func TestPersistenceConcurrentEditsAndSubmissionRetries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "survey.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.Create(ctx, "Concurrency", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := s
			if i%2 == 0 {
				store = other
			}
			if _, err := store.AddItem(ctx, v.ID, Item{Type: "text", Title: "A question"}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	v, err = s.Get(ctx, v.ID, false)
	if err != nil || len(v.Items) != 12 || v.Revision != 13 {
		t.Fatal("lost concurrent edit", v, err)
	}
	v, err = s.Update(ctx, v.ID, nil, nil, pointer(true))
	if err != nil {
		t.Fatal(err)
	}
	input := Submission{Revision: v.Revision, Answers: map[string]Answer{}}
	ids := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := s
			if i%2 == 0 {
				store = other
			}
			r, err := store.Submit(ctx, v.ID, "same-key", input)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- r.ID
		}(i)
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first != "" && id != first {
			t.Fatal("retry duplicated")
		}
		first = id
	}
	s.Close()
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	rows, err := reopened.Responses(ctx, v.ID, 1)
	if err != nil || len(rows) != 1 {
		t.Fatal("persistence or retry failed", rows, err)
	}
}
