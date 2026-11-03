package server

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// skillDocument separates operator-authored YAML from the executable instructions.
// Only the YAML is serialized when metadata changes; the body bytes are retained.
type skillDocument struct {
	header *yaml.Node
	body   []byte
}

func readSkillDocument(content []byte) (skillDocument, error) {
	lines := bytes.SplitAfter(content, []byte("\n"))
	if len(lines) < 2 || string(bytes.TrimSpace(lines[0])) != "---" {
		return skillDocument{}, fmt.Errorf("SKILL.md has no YAML frontmatter")
	}
	end := 1
	for end < len(lines) && string(bytes.TrimSpace(lines[end])) != "---" {
		end++
	}
	if end == len(lines) {
		return skillDocument{}, fmt.Errorf("SKILL.md frontmatter is not closed")
	}
	var document yaml.Node
	if err := yaml.Unmarshal(bytes.Join(lines[1:end], nil), &document); err != nil {
		return skillDocument{}, fmt.Errorf("invalid SKILL.md frontmatter: %w", err)
	}
	header := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if len(document.Content) != 0 {
		header = document.Content[0]
		if header.Kind != yaml.MappingNode {
			return skillDocument{}, fmt.Errorf("SKILL.md frontmatter must be a mapping")
		}
	}
	return skillDocument{header: header, body: bytes.Join(lines[end+1:], nil)}, nil
}

func (document skillDocument) text(key string, value *string) {
	if value == nil {
		return
	}
	node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: strings.TrimSpace(*value)}
	for index := 0; index < len(document.header.Content); index += 2 {
		if document.header.Content[index].Value == key {
			previous := document.header.Content[index+1]
			node.Anchor, node.HeadComment, node.LineComment, node.FootComment = previous.Anchor, previous.HeadComment, previous.LineComment, previous.FootComment
			*previous = *node
			return
		}
	}
	document.header.Content = append(document.header.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, node)
}

func (document skillDocument) remove(keys ...string) {
	retained := document.header.Content[:0]
	for index := 0; index < len(document.header.Content); index += 2 {
		key := document.header.Content[index].Value
		drop := false
		for _, candidate := range keys {
			drop = drop || candidate == key
		}
		if !drop {
			retained = append(retained, document.header.Content[index:index+2]...)
		}
	}
	document.header.Content = retained
}

func (document skillDocument) bytes() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteString("---\n")
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(document.header); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	buffer.WriteString("---\n")
	buffer.Write(document.body)
	return buffer.Bytes(), nil
}

func (document skillDocument) bindings(mcps []string) {
	document.remove("mcps", "mcp")
	var names []string
	for _, name := range mcps {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	if len(names) != 0 {
		joined := strings.Join(names, ", ")
		document.text("mcps", &joined)
	}
}

func newSkillDocument(name, description, license, compatibility string, mcps []string, instructions string) ([]byte, error) {
	if strings.TrimSpace(instructions) == "" {
		instructions = fmt.Sprintf("## %s\n\n<!-- Describe step-by-step instructions in Markdown. -->\n\n1. \n2. \n3. \n", name)
	}
	document := skillDocument{header: &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, body: []byte(instructions)}
	document.text("name", &name)
	document.text("description", &description)
	if license != "" {
		document.text("license", &license)
	}
	if compatibility != "" {
		document.text("compatibility", &compatibility)
	}
	document.bindings(mcps)
	return document.bytes()
}

func rewriteSkillFrontmatter(content []byte, mcps *[]string, description, license, compatibility *string) ([]byte, error) {
	document, err := readSkillDocument(content)
	if err != nil {
		return nil, err
	}
	if mcps == nil && description == nil && license == nil && compatibility == nil {
		return bytes.Clone(content), nil
	}
	document.text("description", description)
	document.text("license", license)
	document.text("compatibility", compatibility)
	if mcps != nil {
		// The SDK falls back from mcps to the historical mcp alias when mcps is
		// empty. An explicit update must replace both sources of that binding.
		document.bindings(*mcps)
	}
	return document.bytes()
}
