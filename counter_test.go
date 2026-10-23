package main

import (
	"database/sql"
	"testing"
)

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

func TestSupersedeRefs(t *testing.T) {
	sup, by := supersedeRefs("This article supersedes TSB 25-2466 to add Bronco Sport.")
	if len(sup) != 1 || sup[0] != "25-2466" {
		t.Errorf("supersedes = %v, want [25-2466]", sup)
	}
	if by != "" {
		t.Errorf("supersededBy = %q, want empty", by)
	}

	sup2, by2 := supersedeRefs("This TSB has been superseded by TSB 24-1234.")
	if by2 != "24-1234" {
		t.Errorf("supersededBy = %q, want 24-1234", by2)
	}
	if len(sup2) != 0 {
		t.Errorf("supersedes = %v, want empty", sup2)
	}

	sup3, by3 := supersedeRefs("No references here at all.")
	if len(sup3) != 0 || by3 != "" {
		t.Errorf("unexpected refs: %v %q", sup3, by3)
	}
}

// TestTSBQuery exercises the exact window-function dedupe + FTS SQL the handler
// builds, against in-memory SQLite.
func TestTSBQuery(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		CREATE TABLE tsbs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			external_id TEXT, make TEXT, model TEXT, year INTEGER,
			comm_date TEXT, system TEXT, description TEXT,
			tsb_number TEXT, tsb_type TEXT, superseded INTEGER NOT NULL DEFAULT 0,
			UNIQUE(external_id, make, model, year)
		);
		CREATE VIRTUAL TABLE tsbs_fts USING fts5(description, system, content='tsbs', content_rowid='id');
		CREATE TRIGGER tsbs_ai AFTER INSERT ON tsbs BEGIN
			INSERT INTO tsbs_fts(rowid, description, system) VALUES (new.id, new.description, new.system);
		END;
	`); err != nil {
		t.Fatal(err)
	}

	ins := `INSERT INTO tsbs (external_id, make, model, year, comm_date, system, description, tsb_number, superseded) VALUES (?,?,?,?,?,?,?,?,?)`
	for _, r := range [][]interface{}{
		{"1", "FORD", "F-150", 2023, "2023-01-01", "ENGINE", "brake noise", "A", 0},
		{"2", "FORD", "F-150", 2023, "2024-06-01", "ENGINE", "brake noise updated", "A", 0}, // newer reissue
		{"3", "FORD", "F-150", 2023, "2023-03-01", "ENGINE", "old brake", "B", 1},           // superseded
	} {
		if _, err := db.Exec(ins, r...); err != nil {
			t.Fatal(err)
		}
	}

	const groupKey = `COALESCE(NULLIF(t.tsb_number, ''), t.external_id)`
	const innerCols = `t.id, t.external_id, t.make, t.model, t.year, COALESCE(t.comm_date, '') AS comm_date, COALESCE(t.system, '') AS system, COALESCE(t.description, '') AS description, COALESCE(t.tsb_number, '') AS tsb_number, COALESCE(t.tsb_type, '') AS tsb_type, t.superseded`
	const outerCols = `id, external_id, make, model, year, comm_date, system, description, tsb_number, tsb_type, superseded`

	run := func(hideSuperseded bool) []TSB {
		core := "FROM tsbs t WHERE t.make = ? COLLATE NOCASE AND t.model = ? COLLATE NOCASE AND t.year = ?"
		if hideSuperseded {
			core += " AND t.superseded = 0"
		}
		q := "WITH base AS (SELECT " + innerCols + ", ROW_NUMBER() OVER (PARTITION BY " + groupKey + " ORDER BY t.comm_date DESC) AS rn " + core + ") SELECT " + outerCols + " FROM base WHERE rn = 1 ORDER BY comm_date DESC LIMIT ? OFFSET ?"
		rows, err := db.Query(q, "FORD", "F-150", 2023, 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []TSB
		for rows.Next() {
			var x TSB
			if err := rows.Scan(&x.ID, &x.ExternalID, &x.Make, &x.Model, &x.Year, &x.CommDate, &x.System, &x.Description, &x.TsbNumber, &x.TsbType, &x.Superseded); err != nil {
				t.Fatal(err)
			}
			out = append(out, x)
		}
		return out
	}

	all := run(false)
	if len(all) != 2 {
		t.Fatalf("dedupe (incl. superseded): got %d rows, want 2", len(all))
	}
	if all[0].TsbNumber != "A" || all[0].ExternalID != "2" {
		t.Errorf("expected newest reissue of A (external_id 2), got %+v", all[0])
	}

	vis := run(true)
	if len(vis) != 1 || vis[0].TsbNumber != "A" {
		t.Errorf("hide superseded: got %d rows (%+v), want just A", len(vis), vis)
	}

	// FTS search path
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tsbs t JOIN tsbs_fts ON tsbs_fts.rowid = t.id WHERE tsbs_fts MATCH ?`, `"noise"*`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("FTS match \"noise\" = %d, want 2", n)
	}

	// FTS + dedupe + hide-superseded path (as the handler builds it)
	core := "FROM tsbs t JOIN tsbs_fts ON tsbs_fts.rowid = t.id WHERE t.make = ? COLLATE NOCASE AND t.model = ? COLLATE NOCASE AND t.year = ? AND t.superseded = 0 AND tsbs_fts MATCH ?"
	fq := "WITH base AS (SELECT " + innerCols + ", ROW_NUMBER() OVER (PARTITION BY " + groupKey + " ORDER BY t.comm_date DESC) AS rn " + core + ") SELECT " + outerCols + " FROM base WHERE rn = 1 ORDER BY comm_date DESC LIMIT ? OFFSET ?"
	frows, err := db.Query(fq, "FORD", "F-150", 2023, `"noise"*`, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer frows.Close()
	fcount := 0
	for frows.Next() {
		var x TSB
		if err := frows.Scan(&x.ID, &x.ExternalID, &x.Make, &x.Model, &x.Year, &x.CommDate, &x.System, &x.Description, &x.TsbNumber, &x.TsbType, &x.Superseded); err != nil {
			t.Fatal(err)
		}
		fcount++
	}
	if fcount != 1 {
		t.Errorf("FTS dedupe path: got %d rows, want 1", fcount)
	}
}
