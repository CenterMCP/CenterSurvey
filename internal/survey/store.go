package survey

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var ErrInvalid = errors.New("invalid input")
var ErrNotFound = errors.New("survey not found or unavailable")
var ErrConflict = errors.New("survey changed, is closed, or retry key was reused with different answers")

type Store struct{ DB *sql.DB }
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

func (o *Option) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '"' {
		return json.Unmarshal(raw, &o.Label)
	}
	type plain Option
	var v plain
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return err
	}
	*o = Option(v)
	return nil
}

type Item struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Required     bool     `json:"required"`
	Options      []Option `json:"options,omitempty"`
	IncludeOther bool     `json:"includeOther,omitempty"`
}
type Survey struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Published   bool   `json:"published"`
	Revision    int64  `json:"revision"`
	Items       []Item `json:"items"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}
type Answer struct {
	Values []string `json:"values"`
	Other  string   `json:"other,omitempty"`
}
type Submission struct {
	Revision int64             `json:"revision"`
	Answers  map[string]Answer `json:"answers"`
}
type Response struct {
	ID         string     `json:"id"`
	SurveyID   string     `json:"survey_id"`
	Submission Submission `json:"submission"`
	Snapshot   Survey     `json:"survey"`
	CreatedAt  string     `json:"created_at"`
}
type Patch struct {
	Title        *string   `json:"title"`
	Description  *string   `json:"description"`
	Required     *bool     `json:"required"`
	Options      *[]Option `json:"options"`
	IncludeOther *bool     `json:"includeOther"`
}

func ID() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func now() string                  { return time.Now().UTC().Format(time.RFC3339Nano) }
func invalid(message string) error { return fmt.Errorf("%w: %s", ErrInvalid, message) }
func valid(s string, n int) bool   { return strings.TrimSpace(s) != "" && len(s) <= n }
func Open(path string) (*Store, error) {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("_foreign_keys", "on")
	q.Set("_journal_mode", "WAL")
	q.Set("_busy_timeout", "5000")
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS schema_version(version INTEGER PRIMARY KEY);
 INSERT OR IGNORE INTO schema_version VALUES(1);
 CREATE TABLE IF NOT EXISTS surveys(id TEXT PRIMARY KEY, document TEXT NOT NULL CHECK(json_valid(document)));
 CREATE TABLE IF NOT EXISTS responses(id TEXT PRIMARY KEY,survey_id TEXT NOT NULL REFERENCES surveys(id) ON DELETE CASCADE,retry_key TEXT NOT NULL,input_hash TEXT NOT NULL,document TEXT NOT NULL CHECK(json_valid(document)),UNIQUE(survey_id,retry_key));
 CREATE INDEX IF NOT EXISTS responses_survey ON responses(survey_id,id);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db}, nil
}
func (s *Store) Close() error { return s.DB.Close() }

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func get(ctx context.Context, db queryer, id string) (Survey, error) {
	var raw string
	var v Survey
	err := db.QueryRowContext(ctx, "SELECT document FROM surveys WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	err = json.Unmarshal([]byte(raw), &v)
	return v, err
}
func (s *Store) Get(ctx context.Context, id string, public bool) (Survey, error) {
	v, err := get(ctx, s.DB, id)
	if err == nil && public && !v.Published {
		return Survey{}, ErrNotFound
	}
	return v, err
}
func (s *Store) Create(ctx context.Context, title, description string) (Survey, error) {
	if !valid(title, 300) || len(description) > 8000 {
		return Survey{}, invalid("title is required (max 300 bytes); description max 8000 bytes")
	}
	v := Survey{ID: ID(), Title: strings.TrimSpace(title), Description: description, Revision: 1, Items: []Item{}, CreatedAt: now(), UpdatedAt: now()}
	raw, _ := json.Marshal(v)
	_, err := s.DB.ExecContext(ctx, "INSERT INTO surveys VALUES(?,?)", v.ID, string(raw))
	return v, err
}
func (s *Store) List(ctx context.Context, page int) ([]Survey, error) {
	if page < 1 || page > 1000000 {
		return nil, invalid("page must be 1–1000000")
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT document FROM surveys ORDER BY json_extract(document,'$.updated_at') DESC,id LIMIT 100 OFFSET ?", (page-1)*100)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Survey{}
	for rows.Next() {
		var raw string
		var v Survey
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) mutate(ctx context.Context, id string, fn func(*Survey) error) (Survey, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Survey{}, err
	}
	defer tx.Rollback()
	v, err := get(ctx, tx, id)
	if err != nil {
		return v, err
	}
	if err = fn(&v); err != nil {
		return Survey{}, err
	}
	v.Revision++
	v.UpdatedAt = now()
	raw, _ := json.Marshal(v)
	if _, err = tx.ExecContext(ctx, "UPDATE surveys SET document=? WHERE id=?", string(raw), id); err != nil {
		return Survey{}, err
	}
	return v, tx.Commit()
}
func (s *Store) Update(ctx context.Context, id string, title, description *string, published *bool) (Survey, error) {
	if title == nil && description == nil && published == nil {
		return Survey{}, invalid("provide at least one field to update")
	}
	return s.mutate(ctx, id, func(v *Survey) error {
		if title != nil {
			if !valid(*title, 300) {
				return invalid("title is required, max 300 bytes")
			}
			v.Title = strings.TrimSpace(*title)
		}
		if description != nil {
			if len(*description) > 8000 {
				return invalid("description max 8000 bytes")
			}
			v.Description = *description
		}
		if published != nil {
			v.Published = *published
		}
		return nil
	})
}
func validateItem(v Item) error {
	if !valid(v.Title, 500) || len(v.Description) > 8000 {
		return invalid("item title is required (max 500 bytes); description max 8000 bytes")
	}
	switch v.Type {
	case "text":
		if len(v.Options) > 0 || v.IncludeOther {
			return invalid("text questions cannot have choices")
		}
	case "section", "page":
		if v.Required || len(v.Options) > 0 || v.IncludeOther {
			return invalid("sections and pages cannot require answers or have choices")
		}
	case "radio", "checkbox":
		if len(v.Options) < 1 || len(v.Options) > 100 {
			return invalid("choice questions need 1–100 options")
		}
		seen := map[string]bool{}
		for _, o := range v.Options {
			if !valid(o.Label, 500) || strings.TrimSpace(o.Label) != o.Label || len(o.Description) > 1000 || seen[o.Label] {
				return invalid("option labels must be unique, trimmed, nonempty, max 500 bytes; descriptions max 1000 bytes")
			}
			seen[o.Label] = true
		}
	default:
		return invalid("unsupported item type")
	}
	return nil
}
func (s *Store) AddItem(ctx context.Context, id string, item Item) (Survey, error) {
	if err := validateItem(item); err != nil {
		return Survey{}, err
	}
	item.ID = ID()
	return s.mutate(ctx, id, func(v *Survey) error {
		if len(v.Items) >= 200 {
			return invalid("maximum 200 items per survey")
		}
		v.Items = append([]Item{item}, v.Items...)
		return nil
	})
}
func (s *Store) UpdateItem(ctx context.Context, id string, index int, p Patch) (Survey, error) {
	if p.Title == nil && p.Description == nil && p.Required == nil && p.Options == nil && p.IncludeOther == nil {
		return Survey{}, invalid("provide at least one field to update")
	}
	return s.mutate(ctx, id, func(v *Survey) error {
		if index < 0 || index >= len(v.Items) {
			return invalid("item index is out of range")
		}
		q := &v.Items[index]
		if p.Title != nil {
			q.Title = *p.Title
		}
		if p.Description != nil {
			q.Description = *p.Description
		}
		if p.Required != nil {
			q.Required = *p.Required
		}
		if p.Options != nil {
			q.Options = *p.Options
		}
		if p.IncludeOther != nil {
			q.IncludeOther = *p.IncludeOther
		}
		return validateItem(*q)
	})
}
func (s *Store) DeleteItem(ctx context.Context, id string, index int) (Survey, error) {
	return s.mutate(ctx, id, func(v *Survey) error {
		if index < 0 || index >= len(v.Items) {
			return invalid("item index is out of range")
		}
		v.Items = append(v.Items[:index], v.Items[index+1:]...)
		return nil
	})
}
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM surveys WHERE id=?", id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}
func validateAnswers(v Survey, in Submission) error {
	if in.Answers == nil || len(in.Answers) > 200 {
		return invalid("answers must be an object keyed by question ID")
	}
	remaining := map[string]Answer{}
	for k, a := range in.Answers {
		remaining[k] = a
	}
	for _, q := range v.Items {
		if q.Type == "page" || q.Type == "section" {
			continue
		}
		a := in.Answers[q.ID]
		delete(remaining, q.ID)
		if len(a.Values) > 100 || len(a.Other) > 8000 {
			return invalid("answer too long")
		}
		count := 0
		seen := map[string]bool{}
		for _, value := range a.Values {
			if len(value) > 8000 || seen[value] {
				return invalid("invalid or duplicate answer")
			}
			seen[value] = true
			if q.Type == "text" {
				if strings.TrimSpace(value) != "" {
					count++
				}
				continue
			}
			found := false
			for _, o := range q.Options {
				if o.Label == value {
					found = true
					break
				}
			}
			if !found {
				return invalid("answer is not an available choice")
			}
			count++
		}
		if a.Other != "" {
			if !q.IncludeOther || strings.TrimSpace(a.Other) == "" {
				return invalid("Other is not allowed or is blank")
			}
			count++
		}
		if q.Type == "text" && (len(a.Values) > 1 || a.Other != "") {
			return invalid("text questions accept one value")
		}
		if q.Type == "radio" && count > 1 {
			return invalid("radio questions accept one selection")
		}
		if q.Required && count == 0 {
			return invalid("an answer is required for: " + q.Title)
		}
	}
	if len(remaining) > 0 {
		return invalid("answer references an unknown question or a section/page")
	}
	return nil
}
func (s *Store) Submit(ctx context.Context, id, key string, in Submission) (Response, error) {
	if !valid(key, 200) || in.Revision < 1 {
		return Response{}, invalid("Idempotency-Key and survey revision are required")
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return Response{}, invalid("invalid answers")
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Response{}, err
	}
	defer tx.Rollback()
	var oldHash, oldDoc string
	err = tx.QueryRowContext(ctx, "SELECT input_hash,document FROM responses WHERE survey_id=? AND retry_key=?", id, key).Scan(&oldHash, &oldDoc)
	if err == nil {
		if hash != oldHash {
			return Response{}, ErrConflict
		}
		var r Response
		err = json.Unmarshal([]byte(oldDoc), &r)
		return r, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Response{}, err
	}
	v, err := get(ctx, tx, id)
	if err != nil {
		return Response{}, err
	}
	if !v.Published || v.Revision != in.Revision {
		return Response{}, ErrConflict
	}
	if err = validateAnswers(v, in); err != nil {
		return Response{}, err
	}
	r := Response{ID: ID(), SurveyID: id, Submission: in, Snapshot: v, CreatedAt: now()}
	document, _ := json.Marshal(r)
	if _, err = tx.ExecContext(ctx, "INSERT INTO responses VALUES(?,?,?,?,?)", r.ID, id, key, hash, string(document)); err != nil {
		return Response{}, err
	}
	return r, tx.Commit()
}
func (s *Store) Responses(ctx context.Context, id string, page int) ([]Response, error) {
	if page < 1 || page > 1000000 {
		return nil, invalid("page must be 1–1000000")
	}
	if _, err := s.Get(ctx, id, false); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT document FROM responses WHERE survey_id=? ORDER BY id LIMIT 100 OFFSET ?", id, (page-1)*100)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Response{}
	for rows.Next() {
		var raw string
		var v Response
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
