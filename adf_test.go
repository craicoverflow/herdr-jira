package main

import (
	"encoding/json"
	"testing"
)

func TestRichTextMarkdownAcceptsMarkdownAndEmpty(t *testing.T) {
	cases := map[string]string{
		`"## Hi\n\nThere"`: "## Hi\n\nThere",
		`null`:             "",
		``:                 "",
		`{"type":"doc","version":1,"content":[]}`: "",
		`{"not":"adf"}`: "",
	}
	for in, want := range cases {
		if got := richTextMarkdown(json.RawMessage(in)); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
}

func TestRichTextMarkdownFromADF(t *testing.T) {
	doc := `{"type":"doc","version":1,"content":[
	  {"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Steps"}]},
	  {"type":"paragraph","content":[
	    {"type":"text","text":"Open "},
	    {"type":"text","text":"cart","marks":[{"type":"strong"}]},
	    {"type":"text","text":" and see "},
	    {"type":"text","text":"docs","marks":[{"type":"link","attrs":{"href":"https://x.test"}}]},
	    {"type":"hardBreak"},
	    {"type":"mention","attrs":{"text":"@Sam"}},
	    {"type":"text","text":" knows"}
	  ]},
	  {"type":"orderedList","content":[
	    {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]}]},
	    {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"two"}]}]}
	  ]},
	  {"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"x := 1"}]},
	  {"type":"rule"},
	  {"type":"table","content":[
	    {"type":"tableRow","content":[{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"A"}]}]},{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"B"}]}]}]},
	    {"type":"tableRow","content":[{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"1"}]}]},{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"2|3"}]}]}]}
	  ]}
	]}`
	want := "## Steps\n\n" +
		"Open **cart** and see [docs](https://x.test)  \n**@Sam** knows\n\n" +
		"1. one\n2. two\n\n" +
		"```go\nx := 1\n```\n\n" +
		"---\n\n" +
		"| A | B |\n| --- | --- |\n| 1 | 2\\|3 |"
	if got := richTextMarkdown(json.RawMessage(doc)); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestDecodeIssueFull(t *testing.T) {
	inner := `{"key":"PROJ-7","fields":{
	  "summary":"Fix it","status":{"name":"In Review","statusCategory":{"key":"indeterminate"}},
	  "issuetype":{"name":"Story","hierarchyLevel":0},"priority":{"name":"High"},
	  "assignee":{"displayName":"Ann","accountId":"a1"},"reporter":null,
	  "created":"2026-11-01T10:00:00.000+0000","updated":"2026-11-02T10:00:00.000+0000",
	  "labels":["x"],"description":"Some *text*",
	  "project":{"key":"PROJ","name":"Project"},
	  "parent":{"key":"PROJ-1","fields":{"summary":"Epic one","issuetype":{"name":"Epic"}}},
	  "subtasks":[{"key":"PROJ-8","fields":{"summary":"Sub","status":{"name":"To Do","statusCategory":{"key":"new"}},"issuetype":{"name":"Subtask","hierarchyLevel":-1}}}],
	  "comment":{"comments":[{"author":{"displayName":"C1"},"created":"t1","body":"one"},{"author":{"displayName":"C2"},"created":"t2","body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"two"}]}]}}]}
	}}`
	raw, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": inner}}})
	got, err := decodeIssueFull(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != "PROJ-7" || got.StatusCategory != "indeterminate" || got.Priority != "High" || got.AssigneeAccountID != "a1" || got.ReporterName != "" {
		t.Errorf("fields: %+v", got)
	}
	if got.ParentKey != "PROJ-1" || got.ParentType != "Epic" || got.ProjectName != "Project" || got.Description != "Some *text*" {
		t.Errorf("parent/project/description: %+v", got)
	}
	if len(got.Subtasks) != 1 || got.Subtasks[0].HierarchyLevel != -1 || got.Subtasks[0].ParentKey != "PROJ-7" {
		t.Errorf("subtasks: %+v", got.Subtasks)
	}
	if len(got.Comments) != 2 || got.Comments[1].Body != "two" || got.Comments[0].Author != "C1" {
		t.Errorf("comments: %+v", got.Comments)
	}
}
