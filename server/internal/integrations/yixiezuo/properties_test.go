package yixiezuo

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClassifyMapsSourceFields(t *testing.T) {
	ada := "11111111-1111-4111-8111-111111111111"
	grace := "22222222-2222-4222-8222-222222222222"
	members := MemberIDs{
		"Ada":   {ada},
		"Grace": {grace},
	}
	snapshot := Snapshot{
		Source:      Source{Host: "dj01.pm.netease.com", ID: "12", URL: "https://dj01.pm.netease.com/issues/12"},
		Title:       "Bug",
		Description: "Steps",
		Status:      "Ready",
		Fields: []SourceField{
			{Name: "版本", Value: "1.2"},
			{Name: "标签", Value: "alpha,beta"},
			{Name: "说明", Value: "Hello, world"},
			{Name: "相关链接", Value: "https://a.example/x、https://b.example/y"},
			{Name: "设计稿", Value: "https://design.example/spec"},
			{Name: "负责人", Value: "Ada"},
			{Name: "参与者", Value: "Ada、Grace"},
			{Name: "指派", Value: "Nobody"},
			{Name: "Assignee", Value: "Ada"},
			{Name: "开始日期", Value: "2026-04-01"},
			{Name: "due_date", Value: "2026-04-30 18:00"},
			{Name: "计划开始日期", Value: "2026-03-02"},
			{Name: "预计截止日期", Value: "2026-05-01T00:00:00+08:00"},
			{Name: "计划日期", Value: "2026-06-01"},
			{Name: "candidate", Value: "not-a-date"},
			{Name: "备注", Value: "  "},
			{Name: strings.Repeat("很长", 20), Value: "too long to be a property name"},
		},
		Comments: []Comment{{Author: "Ada", Content: "ship it", CreatedAt: "2026-04-01"}},
	}

	got := Classify(snapshot, members)

	assertProperty(t, got, SourceLinkPropertyName, "multi_url", []string{"https://dj01.pm.netease.com/issues/12"})
	assertProperty(t, got, "版本", "multi_text", []string{"1.2"})
	assertProperty(t, got, "标签", "multi_text", []string{"alpha", "beta"})
	assertProperty(t, got, "说明", "multi_text", []string{"Hello, world"})
	assertProperty(t, got, "相关链接", "multi_url", []string{"https://a.example/x", "https://b.example/y"})
	assertProperty(t, got, "设计稿", "multi_url", []string{"https://design.example/spec"})
	assertProperty(t, got, "负责人", "actor", "member:"+ada)
	assertProperty(t, got, "参与者", "multi_actor", []string{"member:" + ada, "member:" + grace})
	assertProperty(t, got, "计划日期", "multi_text", []string{"2026-06-01"})
	assertProperty(t, got, "candidate", "multi_text", []string{"not-a-date"})
	if _, ok := findProperty(got, "指派"); ok {
		t.Fatal("unresolved person was stored as a property")
	}
	if _, ok := findProperty(got, "Assignee"); ok {
		t.Fatal("reserved assignee name was stored as a property")
	}
	if got.StartDate != "2026-04-01" {
		t.Fatalf("start date = %q", got.StartDate)
	}
	if got.DueDate != "2026-04-30" {
		t.Fatalf("due date = %q, want the first due column", got.DueDate)
	}
	for _, name := range []string{"开始日期", "due_date", "版本", "负责人"} {
		if fieldRetained(got, name) {
			t.Fatalf("stored field %q stayed in the description", name)
		}
	}
	for _, name := range []string{"指派", "Assignee", "预计截止日期", "计划开始日期", "备注", strings.Repeat("很长", 20)} {
		if !fieldRetained(got, name) {
			t.Fatalf("field %q was dropped from the description", name)
		}
	}
	body := snapshot.MarkdownRetaining(got.Retained)
	if !strings.Contains(body, "指派: Nobody") || !strings.Contains(body, "ship it") {
		t.Fatalf("description lost retained material:\n%s", body)
	}
	if strings.Contains(body, "版本:") || strings.Contains(body, "负责人:") {
		t.Fatalf("description still contains stored fields:\n%s", body)
	}
	full := snapshot.Markdown()
	if !strings.Contains(full, "版本: 1.2") {
		t.Fatal("preview markdown dropped a source field")
	}
}

func TestClassifyPersonMissesStayOutOfText(t *testing.T) {
	ada := "11111111-1111-4111-8111-111111111111"
	got := Classify(Snapshot{
		Source: Source{URL: "https://dj01.pm.netease.com/issues/9"},
		Fields: []SourceField{
			{Name: "作者", Value: "Ada"},
			{Name: "参与人", Value: "Ada, Grace"},
			{Name: "Participants", Value: "Grace"},
			{Name: "Module owner", Value: "Nobody"},
		},
	}, MemberIDs{
		"Ada":   {ada, "33333333-3333-4333-8333-333333333333"},
		"Grace": {"44444444-4444-4444-8444-444444444444"},
	})
	if _, ok := findProperty(got, "作者"); ok {
		t.Fatal("ambiguous display name became an actor")
	}
	if _, ok := findProperty(got, "参与人"); ok {
		t.Fatal("partial person list became a property")
	}
	assertProperty(t, got, "Participants", "multi_actor", []string{"member:44444444-4444-4444-8444-444444444444"})
	if _, ok := findProperty(got, "Module owner"); ok {
		t.Fatal("unknown owner was stored as text")
	}
	for _, name := range []string{"作者", "参与人", "Module owner"} {
		if !fieldRetained(got, name) {
			t.Fatalf("%s was not left in the description", name)
		}
	}

	same := Classify(Snapshot{Fields: []SourceField{{Name: "指派给", Value: "Ada, Ada"}}}, MemberIDs{"Ada": {ada}})
	assertProperty(t, same, "指派给", "actor", "member:"+ada)
}

func TestClassifyCommaSeparatedURLsAndDuplicateNames(t *testing.T) {
	got := Classify(Snapshot{
		Source: Source{URL: "https://dj01.pm.netease.com/issues/3"},
		Fields: []SourceField{
			{Name: "链接", Value: "https://a.example/x, https://b.example/y"},
			{Name: "链接", Value: "https://a.example/x"},
			{Name: "易协作链接", Value: "see the ticket"},
			{Name: "Start Date", Value: "2026-08-01"},
			{Name: "end_date", Value: "not a date"},
		},
	}, nil)
	assertProperty(t, got, "链接", "multi_url", []string{"https://a.example/x", "https://b.example/y"})
	assertProperty(t, got, SourceLinkPropertyName, "multi_url", []string{"https://dj01.pm.netease.com/issues/3"})
	if !fieldRetained(got, "易协作链接") {
		t.Fatal("text field that collides with the source link was absorbed")
	}
	if got.StartDate != "2026-08-01" {
		t.Fatalf("Start Date column = %q", got.StartDate)
	}
	if !fieldRetained(got, "end_date") {
		t.Fatal("unparsed end date was dropped")
	}
}

func TestClassifyPersonFieldTypesStayStable(t *testing.T) {
	ada := "11111111-1111-4111-8111-111111111111"
	grace := "22222222-2222-4222-8222-222222222222"
	members := MemberIDs{"Ada": {ada}, "Grace": {grace}}
	for _, name := range []string{"参与者", "参与人", "Participants", "Module owners"} {
		t.Run(name, func(t *testing.T) {
			for _, value := range []string{"Ada", "Ada、Grace", "Grace"} {
				got := Classify(Snapshot{Fields: []SourceField{{Name: name, Value: value}}}, members)
				property, ok := findProperty(got, name)
				if !ok || property.Type != "multi_actor" {
					t.Fatalf("%s=%q changed field type: %+v", name, value, got)
				}
			}
		})
	}
	got := Classify(Snapshot{Fields: []SourceField{
		{Name: "负责人", Value: "Ada"}, {Name: "负责人", Value: "Grace"},
		{Name: "作者", Value: "Ada、Grace"},
	}}, members)
	assertProperty(t, got, "负责人", "actor", "member:"+ada)
	if !fieldRetained(got, "负责人") || !fieldRetained(got, "作者") {
		t.Fatal("multi-person material for a single-person field must be retained")
	}
}

func assertProperty(t *testing.T, got Classification, name, typ string, value any) {
	t.Helper()
	property, ok := findProperty(got, name)
	if !ok {
		t.Fatalf("missing property %s in %+v", name, got.Properties)
	}
	if property.Type != typ {
		t.Fatalf("property %s type = %s, want %s", name, property.Type, typ)
	}
	var decoded any
	if err := json.Unmarshal(property.Value, &decoded); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(value)
	gotRaw, _ := json.Marshal(decoded)
	if string(want) != string(gotRaw) {
		t.Fatalf("property %s = %s, want %s", name, gotRaw, want)
	}
}

func findProperty(got Classification, name string) (MappedProperty, bool) {
	for _, property := range got.Properties {
		if property.Name == name {
			return property, true
		}
	}
	return MappedProperty{}, false
}

func fieldRetained(got Classification, name string) bool {
	for _, field := range got.Retained {
		if strings.TrimSpace(field.Name) == name {
			return true
		}
	}
	return false
}
