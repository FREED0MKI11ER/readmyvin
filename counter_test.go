package main

import "testing"

func TestCleanDate(t *testing.T) {
	cases := map[string]string{
		"20231225":             "2023-12-25",
		"2023-12-25T00:00:00Z": "2023-12-25",
		"  20240101  ":         "2024-01-01",
		"":                     "",
		"garbage":              "",
		"2023":                 "",
		"0001-01-01T00:00:00Z": "0001-01-01",
	}
	for in, want := range cases {
		if got := cleanDate(in); got != want {
			t.Errorf("cleanDate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanValue(t *testing.T) {
	cases := map[string]string{
		"  Toyota  ": "Toyota",
		"A\x00B":     "AB",
		"":           "",
		"  ":         "",
	}
	for in, want := range cases {
		if got := cleanValue(in); got != want {
			t.Errorf("cleanValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFtsMatchQuery(t *testing.T) {
	cases := map[string]string{
		"":              "",
		"!!!":           "",
		"brake failure": `"brake"* "failure"*`,
		"hello-world":   `"hello"* "world"*`,
		`he"llo`:        `"he"* "llo"*`,
		"V8 engine":     `"V8"* "engine"*`,
	}
	for in, want := range cases {
		if got := ftsMatchQuery(in); got != want {
			t.Errorf("ftsMatchQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHasHeaderRow(t *testing.T) {
	if !hasHeaderRow([]string{"NHTSA ID", "x"}) {
		t.Error("expected header row for 'NHTSA ID'")
	}
	if !hasHeaderRow([]string{"Make", "Model"}) {
		t.Error("expected header row for 'Make'")
	}
	if hasHeaderRow([]string{"20231225", "123"}) {
		t.Error("date/id row should not be a header")
	}
	if hasHeaderRow([]string{"a", "b", "c", "d"}) {
		t.Error("non-numeric second field should not be a header match here")
	}
}
