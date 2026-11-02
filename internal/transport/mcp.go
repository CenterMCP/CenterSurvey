package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/center/CenterSurvey/internal/survey"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const Version = "0.1.0"

type input struct {
	SurveyID     string           `json:"surveyId"`
	Title        *string          `json:"title"`
	Description  *string          `json:"description"`
	Published    *bool            `json:"published"`
	Question     *string          `json:"question"`
	Required     *bool            `json:"required"`
	IncludeOther *bool            `json:"includeOther"`
	MultiSelect  bool             `json:"multiSelect"`
	Options      *[]survey.Option `json:"options"`
	Index        int              `json:"index"`
	Page         int              `json:"page"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func flag(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}
func PublicURL(id string) (map[string]any, error) {
	base := strings.TrimSpace(os.Getenv("SURVEY_BASE_URL"))
	template := base == ""
	if template {
		base = "https://centermcp.com/<mcp-id>"
	} else {
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(base, "#") {
			return nil, fmt.Errorf("%w: SURVEY_BASE_URL must be an absolute HTTP or HTTPS service base without credentials, query strings, or fragments", survey.ErrInvalid)
		}
	}
	message := "Share this URL with respondents."
	if template {
		message = "Replace <mcp-id> with the actual MCP instance ID before sharing, or configure SURVEY_BASE_URL."
	}
	return map[string]any{"survey_url": strings.TrimRight(base, "/") + "/public/surveys/" + url.PathEscape(id), "is_template": template, "message": message}, nil
}
func MCP(s *survey.Store) *server.MCPServer {
	ms := server.NewMCPServer("CenterSurvey", Version, server.WithInputSchemaValidation())
	optionSchema := map[string]any{"oneOf": []any{map[string]any{"type": "string", "minLength": 1, "maxLength": 500}, map[string]any{"type": "object", "properties": map[string]any{"label": map[string]any{"type": "string", "minLength": 1, "maxLength": 500}, "description": map[string]any{"type": "string", "maxLength": 1000}}, "required": []string{"label"}, "additionalProperties": false}}}
	fields := map[string]any{}
	for _, name := range []string{"surveyId", "title", "question", "description"} {
		fields[name] = map[string]any{"type": "string", "maxLength": 8000}
	}
	for _, name := range []string{"published", "required", "includeOther", "multiSelect"} {
		fields[name] = map[string]any{"type": "boolean"}
	}
	fields["index"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 199}
	fields["page"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 1000000}
	fields["options"] = map[string]any{"type": "array", "items": optionSchema, "minItems": 1, "maxItems": 100}
	type spec struct {
		name, description string
		read, destructive bool
		fields, required  []string
	}
	specs := []spec{
		{"get_code_version", "Get the CenterSurvey version.", true, false, nil, nil},
		{"create_survey", "Create a draft survey. Add questions, then update_survey with published=true to accept public responses.", false, false, []string{"title", "description"}, []string{"title"}},
		{"get_survey", "Get a survey, including stable item IDs, zero-based item order, revision, and publication state.", true, false, []string{"surveyId"}, []string{"surveyId"}},
		{"list_surveys", "List surveys by most recent modification; 100 per page.", true, false, []string{"page"}, nil},
		{"update_survey", "Update title, description (empty clears it), and/or published. published=false closes public access. Each edit increments the revision.", false, false, []string{"surveyId", "title", "description", "published"}, []string{"surveyId"}},
		{"delete_survey", "Permanently delete a survey and all its responses.", false, true, []string{"surveyId"}, []string{"surveyId"}},
		{"add_section", "Prepend a visual section header at index 0, without a page break.", false, false, []string{"surveyId", "title", "description"}, []string{"surveyId", "title"}},
		{"add_page", "Prepend a page break at index 0. Respondents continue to the next page.", false, false, []string{"surveyId", "title", "description"}, []string{"surveyId", "title"}},
		{"add_text_question", "Prepend a text question at index 0. Add items in reverse order to build a survey in the desired order.", false, false, []string{"surveyId", "question", "description", "required"}, []string{"surveyId", "question"}},
		{"add_multiple_choice_question", "Prepend a radio question, or checkboxes with multiSelect=true. Options accept strings or {label,description}. includeOther defaults to true.", false, false, []string{"surveyId", "question", "description", "options", "required", "includeOther", "multiSelect"}, []string{"surveyId", "question", "options"}},
		{"update_question", "Update a survey item by zero-based index. question updates its title; options/required/includeOther apply only to compatible question types. Omitted fields are preserved.", false, false, []string{"surveyId", "index", "question", "description", "options", "required", "includeOther"}, []string{"surveyId", "index"}},
		{"delete_question", "Permanently remove a question, section, or page by zero-based index. Existing responses retain survey snapshots.", false, true, []string{"surveyId", "index"}, []string{"surveyId", "index"}},
		{"get_survey_responses", "Read private submitted answers and their survey snapshots; 100 per page. Responses are not exposed through public HTTP routes.", true, false, []string{"surveyId", "page"}, []string{"surveyId"}},
		{"get_public_urls", "Get the respondent URL prefixed by SURVEY_BASE_URL. If unset, returns a https://centermcp.com/<mcp-id>/public/surveys/<survey-id> template. Replace <mcp-id> before sharing. The survey must be published for public access.", true, false, []string{"surveyId"}, []string{"surveyId"}},
	}
	for _, sp := range specs {
		props := map[string]any{}
		for _, field := range sp.fields {
			props[field] = fields[field]
		}
		required := sp.required
		if required == nil {
			required = []string{}
		}
		raw, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false})
		tool := mcp.NewToolWithRawSchema(sp.name, sp.description, raw)
		mcp.WithReadOnlyHintAnnotation(sp.read)(&tool)
		mcp.WithDestructiveHintAnnotation(sp.destructive)(&tool)
		name := sp.name
		ms.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			raw, err := json.Marshal(req.Params.Arguments)
			var in input
			if err == nil {
				err = json.Unmarshal(raw, &in)
			}
			if err != nil {
				return mcp.NewToolResultError("Invalid arguments"), nil
			}
			if in.Page == 0 {
				in.Page = 1
			}
			var result any
			switch name {
			case "get_code_version":
				result = map[string]string{"version": Version}
			case "create_survey":
				result, err = s.Create(ctx, str(in.Title), str(in.Description))
			case "get_survey":
				result, err = s.Get(ctx, in.SurveyID, false)
			case "list_surveys":
				result, err = s.List(ctx, in.Page)
			case "update_survey":
				result, err = s.Update(ctx, in.SurveyID, in.Title, in.Description, in.Published)
			case "delete_survey":
				err = s.Delete(ctx, in.SurveyID)
				result = map[string]any{"deleted": in.SurveyID}
			case "add_section", "add_page", "add_text_question", "add_multiple_choice_question":
				item := survey.Item{Title: str(in.Title), Description: str(in.Description), Required: flag(in.Required, false)}
				switch name {
				case "add_section":
					item.Type = "section"
				case "add_page":
					item.Type = "page"
				case "add_text_question":
					item.Type = "text"
					item.Title = str(in.Question)
				case "add_multiple_choice_question":
					item.Type = "radio"
					if in.MultiSelect {
						item.Type = "checkbox"
					}
					item.Title = str(in.Question)
					item.IncludeOther = flag(in.IncludeOther, true)
					if in.Options != nil {
						item.Options = *in.Options
					}
				}
				result, err = s.AddItem(ctx, in.SurveyID, item)
			case "update_question":
				result, err = s.UpdateItem(ctx, in.SurveyID, in.Index, survey.Patch{Title: in.Question, Description: in.Description, Required: in.Required, Options: in.Options, IncludeOther: in.IncludeOther})
			case "delete_question":
				result, err = s.DeleteItem(ctx, in.SurveyID, in.Index)
			case "get_survey_responses":
				result, err = s.Responses(ctx, in.SurveyID, in.Page)
			case "get_public_urls":
				var v survey.Survey
				v, err = s.Get(ctx, in.SurveyID, false)
				if err == nil {
					var urls map[string]any
					urls, err = PublicURL(in.SurveyID)
					if err == nil {
						urls["published"] = v.Published
						result = urls
					}
				}
			}
			if err != nil {
				return mcp.NewToolResultError(publicError(err)), nil
			}
			output := map[string]any{"tool": name, "data": result}
			body, _ := json.Marshal(output)
			return mcp.NewToolResultStructured(output, string(body)), nil
		})
	}
	return ms
}
