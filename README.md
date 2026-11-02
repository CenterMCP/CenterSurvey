# CenterSurvey

A standalone SQLite survey service built from CenterLINECRM's Go, embedded public-page, Docker, and Streamable HTTP MCP patterns. Survey editing follows CenterGoogleForm's item mechanics without Google APIs, OAuth, or external storage. The public interface uses the blue mobile application design of the CRM.

## Run

Requires Go 1.26+, CGO, and a C compiler (`mattn/go-sqlite3`).

```sh
go build -o build/centersurvey ./cmd/server
SURVEY_BASE_URL=https://centermcp.com/your-mcp-id \
  ./build/centersurvey --database ./data/centersurvey.db --address 127.0.0.1:8000
```

The database directory and version-1 schema are created automatically. Defaults: `./data/centersurvey.db`, `127.0.0.1:8000`. SQLite uses foreign keys, WAL, a five-second busy timeout, and immediate write transactions. Keep the DB, WAL, and SHM files together. Use SQLite's backup API or stop the service before copying the database.

## Connect an MCP client

```json
{
  "mcpServers": {
    "centersurvey": {
      "url": "http://localhost:8000/mcp"
    }
  }
}
```

The server does not require an LLM API key. Ask your MCP client to create a survey, add questions, publish it, and return its public URL.

| Tool | Purpose |
| --- | --- |
| `get_code_version` | Get the service version |
| `create_survey` | Create a draft with title and optional description |
| `get_survey` | Inspect a survey and its stable item IDs, order, and revision |
| `list_surveys` | List surveys by most recent modification |
| `update_survey` | Update title, description, and/or `published` |
| `delete_survey` | Permanently delete the survey and all responses |
| `add_section` | Prepend a visual section header |
| `add_page` | Prepend a page break |
| `add_text_question` | Prepend a text question |
| `add_multiple_choice_question` | Prepend radio buttons or checkboxes |
| `update_question` | Update an item by zero-based index |
| `delete_question` | Delete an item by zero-based index |
| `get_survey_responses` | Read private response records and survey snapshots |
| `get_public_urls` | Get a respondent URL using `SURVEY_BASE_URL` |

Survey tools use `surveyId`. Questions and sections share the same ordered item list. As in CenterGoogleForm, all new items are inserted at index **0**: add them in reverse order. A survey has at most 200 items. `get_survey` returns the current order before index-based edits. Text, radio, and checkbox questions have stable IDs for answers. Page and section items do not accept answers.

`list_surveys` and `get_survey_responses` accept `page` (default 1) and return up to 100 records. An empty page ends the collection. Unknown tool properties and invalid types are rejected before mutation. Destructive tools are annotated accordingly.

### Example workflow

Call `create_survey`:

```json
{"title":"How was your visit?","description":"We'd love your feedback."}
```

Using its `id`, add the last question first:

```json
{"surveyId":"<id>","question":"Anything else to share?","required":false}
```

Call `add_multiple_choice_question`:

```json
{
  "surveyId":"<id>",
  "question":"How was your experience?",
  "options":["Good",{"label":"Great","description":"Everything went well"}],
  "required":true,
  "includeOther":true,
  "multiSelect":false
}
```

Choice options accept strings or `{label,description}` objects. Labels must be unique and trimmed. `includeOther` defaults to true; `multiSelect:true` makes checkboxes. Text questions and section/page items do not accept choices. `update_question` preserves omitted properties and cannot change an item's type; delete and recreate an item to change its type. Its `question` argument also updates section/page titles. Empty descriptions clear help text. Publishing and each item edit increment `revision`.

Call `update_survey` to publish:

```json
{"surveyId":"<id>","published":true}
```

Call `get_public_urls` with `{"surveyId":"<id>"}` to retrieve the public link. `published:false` closes the survey. Drafts and closed surveys are not visible publicly. Existing completed submissions remain accessible through MCP.

## Public URLs and API

Set `SURVEY_BASE_URL` to the external **service base**, including the instance prefix but omitting `/mcp` and `/public/...`:

- Dedicated domain: `https://survey.example.com`
- Shared MCP domain: `https://centermcp.com/<actual-instance-id>`

`get_public_urls` appends `/public/surveys/<survey-id>`. With the variable unset or blank, it returns `https://centermcp.com/<mcp-id>/public/surveys/<survey-id>` with `is_template:true`. Replace `<mcp-id>` before sharing. Configured URLs return `is_template:false`. Only absolute HTTP/HTTPS URLs without credentials, query strings, or fragments are accepted.

Relative public assets and API requests preserve instance prefixes when a proxy strips them. Route `/mcp`, `/public/*`, and `/api/v1/*` to the same service. The public page renders text questions, radio buttons, checkboxes, Other inputs, section headers, and pages with Back/Continue navigation. It validates required questions before advancing, uses safe text rendering, and displays a response receipt on completion.

| Method | Endpoint | Behavior |
| --- | --- | --- |
| GET | `/health` | DB readiness and service version |
| GET | `/public/surveys/{id}` | Public survey page; published only |
| GET | `/api/v1/surveys/{id}` | Published definition and revision |
| POST | `/api/v1/surveys/{id}/responses` | Submit answers with `Idempotency-Key` |

Example submission (use stable item IDs from the definition):

```sh
curl -X POST http://localhost:8000/api/v1/surveys/SURVEY_ID/responses \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: unique-submission-attempt' \
  -d '{"revision":4,"answers":{"TEXT_ITEM_ID":{"values":["Lovely visit"]},"RADIO_ITEM_ID":{"values":["Great"]}}}'
```

All answers are `{values:[string,...],other?:string}`. Text and radio questions accept at most one value; radio questions accept an Other answer instead of a listed value when allowed. Checkboxes accept multiple unique listed values and an optional Other answer. Required questions reject blank or missing answers. Unknown questions, answers to sections/pages, duplicate choices, and unavailable options return 400. Request bodies are limited to 1 MiB.

The server stores a snapshot of the survey and its revision with each response, so subsequent edits/deletions of questions do not reinterpret historic answers. A submission against an old revision or a closed survey returns 409. Identical retries with the same key return the original receipt, even after closing/editing the survey. Reusing that key for different input returns 409. Browser retries hold the same key and frozen answers in page memory after an uncertain failure; they are not retained across refresh or tab closure. A reload starts a new attempt.

Public submission responses contain only `id`, `survey_id`, and `created_at`. There is no public response-list endpoint. MCP response retrieval is private to whoever can access MCP. Deleting an entire survey permanently cascades its response records.

Errors use `{"error":"..."}`: 400 invalid input, 404 unavailable survey, 409 stale/closed survey or changed retry, 500 internal failure. Responses and public assets are sent with `Cache-Control: no-store`; public pages enforce a same-origin content security policy.

## Docker

```sh
docker build -t centersurvey .
docker run --rm -p 127.0.0.1:8000:8000 \
  -e SURVEY_BASE_URL=https://survey.example.com \
  -v centersurvey-data:/data centersurvey
```

The image uses CGO SQLite, runs as an unprivileged `survey` user, and persists `/data/centersurvey.db` in the named volume.

`docker-build.sh` follows CenterLINECRM's publishing contract, including Docker Hub credentials on stdin, a temporary credential helper, and Buildx push. It does not change the existing Docker login. Run in Bash:

```bash
read -r -s -p 'Docker Hub token: ' SURVEY_DOCKER_TOKEN; printf '\n'
printf '%s' "$SURVEY_DOCKER_TOKEN" | ./docker-build.sh your-user/centersurvey 0.1.0 your-user
unset SURVEY_DOCKER_TOKEN
```

Default platform: `linux/amd64`. Set `PLATFORMS=linux/amd64,linux/arm64` for multi-platform publishing with a suitable builder. Publishing requires Docker, Buildx, and push permission for the named repository. `task docker-build` only builds locally.

## Development

```sh
go test -race ./...
go vet ./...
go build ./...
node --check internal/transport/survey.js
```

Task commands: `build`, `test`, `check`, `run-dev`, `docker-build`. Tests exercise survey/question CRUD, prepending, partial updates, draft/published access, required/choice validation, revision conflicts, immutable response snapshots, rollback, SQLite persistence, independent-connection concurrency, idempotent submissions, MCP schema validation, public HTTP flows, and URL prefix configuration.

## Deployment scope

This is a single-business service. It does not implement Google synchronization, survey branching, scoring, file uploads, respondent identity verification, authentication, CAPTCHA, or built-in rate limits. There is no authentication on `/mcp`; anyone with access can edit/delete surveys and read all submitted answers. Protect MCP separately using an authenticated reverse proxy, terminate HTTPS, and apply rate limits and request/origin restrictions to public submission routes before internet deployment. No cross-origin CORS access is enabled. An idempotency key prevents duplicate retries, not multiple intentional submissions. Treat SQLite files and response backups as sensitive data.
