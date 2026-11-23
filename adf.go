package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

// adf.go turns an issue's rich text into markdown for the Herdr app's detail
// view. getJiraIssue is asked for markdown, but Jira still answers some
// fields (an empty description, for one) in Atlassian Document Format, so a
// description may arrive as either. richTextMarkdown accepts both.

// adfNode is one node of an Atlassian Document Format tree.
type adfNode struct {
	Type    string         `json:"type"`
	Text    string         `json:"text"`
	Attrs   map[string]any `json:"attrs"`
	Marks   []adfMark      `json:"marks"`
	Content []adfNode      `json:"content"`
}

type adfMark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs"`
}

// richTextMarkdown returns raw (a JSON string or an ADF document) as
// markdown, or "" when it is empty, null or unreadable.
func richTextMarkdown(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var doc adfNode
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	return strings.TrimSpace(adfBlocks(doc.Content, ""))
}

// adfBlocks renders block nodes, one blank line apart, each line prefixed
// with indent (for nesting inside lists and quotes).
func adfBlocks(nodes []adfNode, indent string) string {
	var parts []string
	for _, n := range nodes {
		if b := adfBlock(n, indent); strings.TrimSpace(b) != "" {
			parts = append(parts, b)
		}
	}
	return strings.Join(parts, "\n\n")
}

func adfBlock(n adfNode, indent string) string {
	switch n.Type {
	case "paragraph":
		return indent + adfInline(n.Content)
	case "heading":
		level := 1
		if l, ok := n.Attrs["level"].(float64); ok && l >= 1 && l <= 6 {
			level = int(l)
		}
		return indent + strings.Repeat("#", level) + " " + adfInline(n.Content)
	case "bulletList", "orderedList":
		var lines []string
		for i, item := range n.Content {
			marker := "- "
			if n.Type == "orderedList" {
				marker = strconv.Itoa(i+1) + ". "
			}
			body := adfBlocks(item.Content, indent+"   ")
			body = strings.TrimPrefix(body, indent+"   ")
			lines = append(lines, indent+marker+body)
		}
		return strings.Join(lines, "\n")
	case "taskList":
		var lines []string
		for _, item := range n.Content {
			box := "[ ] "
			if s, _ := item.Attrs["state"].(string); s == "DONE" {
				box = "[x] "
			}
			lines = append(lines, indent+"- "+box+adfInline(item.Content))
		}
		return strings.Join(lines, "\n")
	case "codeBlock":
		lang, _ := n.Attrs["language"].(string)
		var text strings.Builder
		for _, c := range n.Content {
			text.WriteString(c.Text)
		}
		return indent + "```" + lang + "\n" + text.String() + "\n" + indent + "```"
	case "blockquote", "panel":
		inner := adfBlocks(n.Content, "")
		var lines []string
		for _, l := range strings.Split(inner, "\n") {
			lines = append(lines, indent+"> "+l)
		}
		return strings.Join(lines, "\n")
	case "rule":
		return indent + "---"
	case "table":
		return adfTable(n, indent)
	case "mediaSingle", "mediaGroup", "media":
		return indent + "_(attachment)_"
	case "expand", "nestedExpand":
		title, _ := n.Attrs["title"].(string)
		body := adfBlocks(n.Content, indent)
		if title != "" {
			return indent + "**" + title + "**\n\n" + body
		}
		return body
	}
	if len(n.Content) > 0 {
		return adfBlocks(n.Content, indent)
	}
	return indent + adfInline([]adfNode{n})
}

// adfTable renders a table as a markdown table, its first row the header.
func adfTable(n adfNode, indent string) string {
	var rows [][]string
	for _, r := range n.Content {
		var cells []string
		for _, c := range r.Content {
			cell := strings.ReplaceAll(adfBlocks(c.Content, ""), "\n", " ")
			cells = append(cells, strings.ReplaceAll(cell, "|", `\|`))
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return ""
	}
	var lines []string
	for i, cells := range rows {
		lines = append(lines, indent+"| "+strings.Join(cells, " | ")+" |")
		if i == 0 {
			seps := make([]string, len(cells))
			for j := range seps {
				seps[j] = "---"
			}
			lines = append(lines, indent+"| "+strings.Join(seps, " | ")+" |")
		}
	}
	return strings.Join(lines, "\n")
}

// adfInline renders inline nodes: text with its marks, mentions, links.
func adfInline(nodes []adfNode) string {
	var b strings.Builder
	for _, n := range nodes {
		switch n.Type {
		case "text":
			b.WriteString(adfMarked(n.Text, n.Marks))
		case "hardBreak":
			b.WriteString("  \n")
		case "mention":
			t, _ := n.Attrs["text"].(string)
			if t == "" {
				t = "@someone"
			}
			if !strings.HasPrefix(t, "@") {
				t = "@" + t
			}
			b.WriteString("**" + t + "**")
		case "emoji":
			if t, _ := n.Attrs["text"].(string); t != "" {
				b.WriteString(t)
			} else if s, _ := n.Attrs["shortName"].(string); s != "" {
				b.WriteString(s)
			}
		case "inlineCard", "blockCard", "embedCard":
			if u, _ := n.Attrs["url"].(string); u != "" {
				b.WriteString("<" + u + ">")
			}
		case "status":
			if t, _ := n.Attrs["text"].(string); t != "" {
				b.WriteString("`" + t + "`")
			}
		case "date":
			if t, _ := n.Attrs["timestamp"].(string); t != "" {
				b.WriteString(t)
			}
		default:
			b.WriteString(adfInline(n.Content))
		}
	}
	return b.String()
}

func adfMarked(text string, marks []adfMark) string {
	if text == "" {
		return ""
	}
	for _, m := range marks {
		switch m.Type {
		case "code":
			text = "`" + text + "`"
		case "strong":
			text = "**" + text + "**"
		case "em":
			text = "*" + text + "*"
		case "strike":
			text = "~~" + text + "~~"
		case "link":
			if href, _ := m.Attrs["href"].(string); href != "" {
				text = "[" + text + "](" + href + ")"
			}
		}
	}
	return text
}
