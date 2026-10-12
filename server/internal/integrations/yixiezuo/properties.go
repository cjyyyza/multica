package yixiezuo

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// SourceLinkPropertyName is the stable workspace property for the source
	// issue URL. The name matches the product's label for that link.
	SourceLinkPropertyName = "易协作链接"
	propertyMultiURL       = "multi_url"
	propertyMultiText      = "multi_text"
	propertyActor          = "actor"
	propertyMultiActor     = "multi_actor"
	maxPropertyNameLen     = 32
	maxURLValueLen         = 2048
)

// reservedPropertyNames mirrors the handler catalog. A source field that
// would collide with a built-in issue field stays in the description.
var reservedPropertyNames = map[string]struct{}{
	"status": {}, "priority": {}, "assignee": {}, "project": {}, "parent": {},
	"stage": {}, "label": {}, "labels": {}, "start_date": {}, "due_date": {},
	"title": {}, "description": {}, "creator": {}, "created_at": {}, "updated_at": {},
	"metadata": {}, "properties": {},
}

// MemberIDs maps an exact user display name to the workspace member user ids
// that use it. More than one id means the name is ambiguous.
type MemberIDs map[string][]string

// MappedProperty is one source value ready for an issue property definition.
// Value is the JSON stored on the issue: a string array for multi_text and
// multi_url, a "member:<uuid>" string for actor, or an array of those for
// multi_actor.
type MappedProperty struct {
	Name  string
	Type  string
	Value json.RawMessage
}

// Classification is the pure mapping of one snapshot onto issue fields.
type Classification struct {
	Properties []MappedProperty
	StartDate  string
	DueDate    string
	// Retained are the source fields that stay in the description.
	Retained []SourceField
}

type propertyGroup struct {
	name   string
	typ    string
	values []string
}

// Classify maps snapshot fields onto issue properties and start/due dates.
// members is keyed by exact display name. A person field is stored only when
// every name matches exactly one member; any miss stays in Retained and is
// not written as text.
func Classify(snapshot Snapshot, members MemberIDs) Classification {
	var out Classification
	groups := map[string]*propertyGroup{}
	var order []string
	add := func(name, typ string, values []string) bool {
		key := strings.ToLower(name)
		group, ok := groups[key]
		if !ok {
			groups[key] = &propertyGroup{name: name, typ: typ, values: dedupe(values)}
			order = append(order, key)
			return true
		}
		return group.absorb(typ, values)
	}

	if raw := strings.TrimSpace(snapshot.Source.URL); httpURL(raw) {
		add(SourceLinkPropertyName, propertyMultiURL, []string{raw})
	}

	for _, field := range snapshot.Fields {
		name := strings.TrimSpace(field.Name)
		value := strings.TrimSpace(field.Value)
		if name == "" || value == "" {
			out.Retained = append(out.Retained, field)
			continue
		}
		if column := dateColumn(name); column != "" {
			day, ok := calendarDay(value)
			if !ok {
				out.Retained = append(out.Retained, field)
				continue
			}
			switch column {
			case "start":
				if out.StartDate == "" {
					out.StartDate = day
					continue
				}
			case "due":
				if out.DueDate == "" {
					out.DueDate = day
					continue
				}
			}
			out.Retained = append(out.Retained, field)
			continue
		}
		if typ := personFieldType(name); typ != "" {
			ids, ok := mapPerson(value, members)
			if !ok || (typ == propertyActor && len(ids) != 1) || !propertyNameOK(name) || !add(name, typ, ids) {
				out.Retained = append(out.Retained, field)
				continue
			}
			continue
		}
		if urls, ok := urlValues(value); ok && propertyNameOK(name) && add(name, propertyMultiURL, urls) {
			continue
		}
		if !propertyNameOK(name) {
			out.Retained = append(out.Retained, field)
			continue
		}
		if !add(name, propertyMultiText, textValues(value)) {
			out.Retained = append(out.Retained, field)
			continue
		}
	}

	out.Properties = make([]MappedProperty, 0, len(order))
	for _, key := range order {
		out.Properties = append(out.Properties, groups[key].mapped())
	}
	return out
}

func (g *propertyGroup) absorb(typ string, values []string) bool {
	if g.typ != typ {
		return false
	}
	merged := dedupe(append(append([]string{}, g.values...), values...))
	if typ == propertyActor && len(merged) != 1 {
		return false
	}
	g.values = merged
	return true
}

func (g *propertyGroup) mapped() MappedProperty {
	switch g.typ {
	case propertyActor:
		return MappedProperty{Name: g.name, Type: g.typ, Value: jsonValue("member:" + g.values[0])}
	case propertyMultiActor:
		refs := make([]string, len(g.values))
		for i, id := range g.values {
			refs[i] = "member:" + id
		}
		return MappedProperty{Name: g.name, Type: g.typ, Value: jsonValue(refs)}
	default:
		return MappedProperty{Name: g.name, Type: g.typ, Value: jsonValue(g.values)}
	}
}

func mapPerson(value string, members MemberIDs) ([]string, bool) {
	if ids, ok := oneMember(value, members); ok {
		return ids, true
	}
	// An ambiguous whole-value match is a miss. Do not split it into pieces
	// that might accidentally match other people.
	if len(members[strings.TrimSpace(value)]) > 1 {
		return nil, false
	}
	names := splitDelimiters(value, true)
	if len(names) < 2 {
		return nil, false
	}
	ids := make([]string, 0, len(names))
	for _, name := range names {
		matched, ok := oneMember(name, members)
		if !ok {
			return nil, false
		}
		ids = append(ids, matched[0])
	}
	return dedupe(ids), true
}

func oneMember(name string, members MemberIDs) ([]string, bool) {
	ids := members[strings.TrimSpace(name)]
	if len(ids) != 1 || strings.TrimSpace(ids[0]) == "" {
		return nil, false
	}
	return []string{ids[0]}, true
}

func urlValues(value string) ([]string, bool) {
	// Split before accepting the whole string. url.Parse treats a delimiter
	// inside the path as one URL, which would hide a real list.
	parts := splitDelimiters(value, true)
	if len(parts) >= 2 {
		for _, part := range parts {
			if !httpURL(part) {
				return nil, false
			}
		}
		return dedupe(parts), true
	}
	if httpURL(value) {
		return []string{strings.TrimSpace(value)}, true
	}
	return nil, false
}

func textValues(value string) []string {
	if parts := splitDelimiters(value, false); len(parts) >= 2 {
		return dedupe(parts)
	}
	// A comma splits a text list only when the value has no spaces, so
	// "a,b,c" is a list and "Hello, world" stays one entry.
	if strings.ContainsAny(value, " \t") {
		return []string{strings.TrimSpace(value)}
	}
	parts := splitDelimiters(value, true)
	if len(parts) >= 2 && compactTokens(parts) {
		return dedupe(parts)
	}
	return []string{strings.TrimSpace(value)}
}

func compactTokens(parts []string) bool {
	for _, part := range parts {
		if part == "" || utf8.RuneCountInString(part) > 64 {
			return false
		}
		if strings.ContainsAny(part, ".。!！?？:：") {
			return false
		}
	}
	return true
}

func splitDelimiters(value string, commas bool) []string {
	seps := "、;；|\n\r"
	if commas {
		seps += ",，"
	}
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return strings.ContainsRune(seps, r)
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func dedupe(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func dateColumn(name string) string {
	folded := strings.ToLower(strings.TrimSpace(name))
	switch strings.ReplaceAll(folded, " ", "_") {
	case "start_date", "startdate":
		return "start"
	case "due_date", "duedate":
		return "due"
	}
	compact := strings.ReplaceAll(strings.TrimSpace(name), " ", "")
	switch compact {
	case "开始日期":
		return "start"
	case "截止日期":
		return "due"
	}
	if !nameHasDateWord(name) {
		return ""
	}
	if strings.Contains(folded, "start") || strings.Contains(name, "开始") {
		return "start"
	}
	if strings.Contains(folded, "due") || strings.Contains(folded, "end") || strings.Contains(name, "截止") || strings.Contains(name, "结束") || strings.Contains(name, "到期") {
		return "due"
	}
	return ""
}

func nameHasDateWord(name string) bool {
	if strings.Contains(name, "日期") {
		return true
	}
	folded := strings.ToLower(name)
	for i := 0; i+4 <= len(folded); i++ {
		if folded[i:i+4] != "date" {
			continue
		}
		before := i == 0 || !isASCIILetter(rune(folded[i-1]))
		after := i+4 == len(folded) || !isASCIILetter(rune(folded[i+4]))
		if before && after {
			return true
		}
	}
	return false
}

func isASCIILetter(r rune) bool {
	return r >= 'a' && r <= 'z'
}

func calendarDay(raw string) (string, bool) {
	parsed, err := ParseLocalTime(strings.TrimSpace(raw))
	if err != nil || parsed.IsZero() {
		return "", false
	}
	return parsed.Format("2006-01-02"), true
}

// Multiplicity belongs to the field, not today's value. A participant field
// remains multi_actor when it contains one person or temporarily becomes empty.
func personFieldType(name string) string {
	for _, word := range []string{"参与者", "参与人"} {
		if strings.Contains(name, word) {
			return propertyMultiActor
		}
	}
	tokens := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, token := range tokens {
		for _, word := range []string{"participant", "participants", "assignees", "authors", "owners"} {
			if token == word {
				return propertyMultiActor
			}
		}
	}
	for _, word := range []string{"负责人", "指派给", "指派", "作者"} {
		if strings.Contains(name, word) {
			return propertyActor
		}
	}
	for _, token := range tokens {
		if token == "assignee" || token == "author" || token == "owner" {
			return propertyActor
		}
	}
	return ""
}

func propertyNameOK(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxPropertyNameLen {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	normalized := strings.ReplaceAll(strings.ToLower(name), " ", "_")
	if _, reserved := reservedPropertyNames[normalized]; reserved {
		return false
	}
	return true
}

func httpURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxURLValueLen {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func jsonValue(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
