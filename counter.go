package main

import (
	"archive/zip"
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/gzhttp"
	_ "modernc.org/sqlite"
)

const counterFile = "/var/www/vindecoder/counter.txt"
const dbFile = "/var/www/vindecoder/counted_vins.db"
const tsbDBFile = "/var/www/vindecoder/tsb.db"
const vpicDBFile = "/var/www/vindecoder/vpic_cache.db"
const tsbDataDir = "/var/www/vindecoder/tsb_data"
const tsbBaseURL = "https://static.nhtsa.gov/odi/ffdd/tsbs/"

const zipRetentionDays = 30

// Version is stamped at build time via -ldflags "-X main.Version=...".
var Version = "dev"

var mu sync.Mutex
var db *sql.DB
var tsbDB *sql.DB
var vpicDB *sql.DB
var countedVINs = make(map[string]bool)

var importRunning bool

func startScheduledImport() {
	// Run once on startup (after a brief delay to let server fully start)
	time.Sleep(10 * time.Second)
	runScheduledImportOnce()

	// Then check every hour
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		now := <-ticker.C
		hour := now.Hour()
		// Run at 3AM
		if hour == 3 {
			runScheduledImportOnce()
		}
	}
}

func runScheduledImportOnce() {
	if importRunning {
		log.Println("[Scheduler] Import already running, skipping")
		return
	}

	importRunning = true
	defer func() { importRunning = false }()

	log.Println("[Scheduler] Running scheduled TSB import...")

	// Re-initialize tsbDB if it's nil (e.g. first run)
	if tsbDB == nil {
		if err := initTSBDB(); err != nil {
			log.Printf("[Scheduler] Failed to init TSB database: %v", err)
			return
		}
	}
	// Do NOT close tsbDB here - it's shared with the server handler

	os.MkdirAll(tsbDataDir, 0755)

	files, err := fetchTSBFileList()
	if err != nil {
		log.Printf("[Scheduler] Failed to fetch file list: %v", err)
		return
	}

	if len(files) == 0 {
		log.Println("[Scheduler] No TSBS files found")
		return
	}

	log.Printf("[Scheduler] Found %d TSBS files to process", len(files))

	for _, file := range files {
		log.Printf("[Scheduler] Processing %s...", file)
		if err := downloadAndProcess(file, false); err != nil {
			log.Printf("[Scheduler] Error processing %s: %v", file, err)
		}
	}

	log.Println("[Scheduler] Import complete!")
	cleanupOldZips()
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "import":
			runTSBImport(nil)
			return
		case "cleanup":
			cleanupOldZips()
			return
		}
	}

	log.Println("Starting counter server...")

	if err := initDB(); err != nil {
		log.Fatal("Failed to init database:", err)
	}

	if err := initTSBDB(); err != nil {
		log.Printf("Warning: Failed to init TSB database: %v", err)
	} else {
		log.Println("TSB database initialized successfully")
	}

	if err := initVPICDB(); err != nil {
		log.Printf("Warning: Failed to init vPIC database: %v", err)
	} else {
		log.Println("vPIC VIN database initialized successfully")
	}

	log.Printf("Loaded %d previously counted VINs", len(countedVINs))

	http.HandleFunc("/counter", handler)
	http.HandleFunc("/api/tsbs", tsbHandler)
	http.HandleFunc("/api/decode-vin", decodeVINHandler)
	http.HandleFunc("/api/version", versionHandler)

	// Start scheduled TSB import (daily at 3AM)
	go startScheduledImport()

	log.Println("Server listening on :8097")
	http.ListenAndServe(":8097", gzhttp.GzipHandler(http.DefaultServeMux))
}

func initDB() error {
	var err error
	db, err = sql.Open("sqlite", dbFile)
	if err != nil {
		return err
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS counted_vins (
		vin_hash TEXT PRIMARY KEY,
		counted_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		return err
	}

	rows, err := db.Query("SELECT vin_hash FROM counted_vins")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err == nil {
			countedVINs[hash] = true
		}
	}
	return nil
}

func initVPICDB() error {
	var err error
	vpicDB, err = sql.Open("sqlite", vpicDBFile)
	if err != nil {
		return err
	}

	_, err = vpicDB.Exec(`
		CREATE TABLE IF NOT EXISTS vin_cache (
			vin TEXT PRIMARY KEY,
			data TEXT,
			decoded_at TEXT
		)
	`)
	if err != nil {
		log.Printf("Warning: vin_cache table init failed: %v", err)
	}

	log.Println("VIN cache database initialized successfully")
	return nil
}

func initTSBDB() error {
	var err error
	tsbDB, err = sql.Open("sqlite", tsbDBFile)
	if err != nil {
		return err
	}

	// Test connection to make sure it works
	if err := tsbDB.Ping(); err != nil {
		return err
	}

	schema := `
	CREATE TABLE IF NOT EXISTS tsbs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		external_id TEXT,
		make TEXT,
		model TEXT,
		year INTEGER,
		comm_date TEXT,
		system TEXT DEFAULT '',
		description TEXT,
		created_at TEXT DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(external_id, make, model, year)
	);
	CREATE INDEX IF NOT EXISTS idx_tsbs_vehicle ON tsbs(make, model, year);
	CREATE INDEX IF NOT EXISTS idx_tsbs_make ON tsbs(make);
	CREATE INDEX IF NOT EXISTS idx_tsbs_year ON tsbs(year);
	`
	_, err = tsbDB.Exec(schema)
	return err
}

func versionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]string{"version": Version})
}

func handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		return
	}

	mu.Lock()
	defer mu.Unlock()

	count := loadCount()

	if r.Method == http.MethodPost {
		var req struct {
			VinHash string `json:"vinHash"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		log.Printf("POST: vinHash=%q, isCounted=%v", req.VinHash, isVINCounted(req.VinHash))

		if req.VinHash != "" && !isVINCounted(req.VinHash) {
			markVINCounted(req.VinHash)
			count++
			saveCount(count)
			log.Printf("Counted! New total: %d", count)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"count": count})
}

func isVINCounted(vinHash string) bool {
	return countedVINs[vinHash]
}

func markVINCounted(vinHash string) {
	countedVINs[vinHash] = true
	_, err := db.Exec("INSERT OR IGNORE INTO counted_vins (vin_hash, counted_at) VALUES (?, ?)",
		vinHash, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		log.Printf("DB insert error: %v", err)
	}
}

func loadCount() int {
	data, err := os.ReadFile(counterFile)
	if err != nil {
		return 0
	}
	if n, err := strconv.Atoi(string(data)); err == nil && n > 0 {
		return n
	}
	return 0
}

func saveCount(n int) {
	os.WriteFile(counterFile, []byte(strconv.Itoa(n)), 0644)
}

// ===== TSB IMPORT FUNCTIONS =====

func runTSBImport(files []string) {
	if err := initTSBDB(); err != nil {
		log.Fatal("Failed to init TSB database:", err)
	}
	defer tsbDB.Close()

	log.Println("Starting TSB import...")

	os.MkdirAll(tsbDataDir, 0755)

	forceRedownload := false
	if len(os.Args) > 2 && os.Args[2] == "-force" {
		forceRedownload = true
		log.Println("Force redownload enabled")
	}

	// If no files provided, fetch from NHTSA or use hardcoded list
	if files == nil {
		var err error
		files, err = fetchTSBFileList()
		if err != nil {
			log.Printf("Failed to fetch file list: %v", err)
			log.Println("Using hardcoded file list as fallback")
			files = []string{
				"TSBS_RECEIVED_1995-1999.zip",
				"TSBS_RECEIVED_2000-2004.zip",
				"TSBS_RECEIVED_2005-2009.zip",
				"TSBS_RECEIVED_2010-2014.zip",
				"TSBS_RECEIVED_2015-2019.zip",
				"TSBS_RECEIVED_2020-2024.zip",
				"TSBS_RECEIVED_2025-2026.zip",
			}
		}
	}

	for _, file := range files {
		log.Printf("Processing %s...", file)
		if err := downloadAndProcess(file, forceRedownload); err != nil {
			log.Printf("Error processing %s: %v", file, err)
		}
	}

	log.Println("Import complete!")
	cleanupOldZips()
}

func downloadAndProcess(filename string, forceRedownload bool) error {
	zipPath := filepath.Join(tsbDataDir, filename)
	url := tsbBaseURL + filename

	if forceRedownload {
		if _, err := os.Stat(zipPath); err == nil {
			log.Printf("Removing old %s for forced redownload", filename)
			os.Remove(zipPath)
		}
	}

	if _, err := os.Stat(zipPath); os.IsNotExist(err) {
		log.Printf("Downloading %s...", filename)
		if err := downloadFile(url, zipPath); err != nil {
			return fmt.Errorf("download failed: %w", err)
		}
	} else {
		log.Printf("Using cached %s", filename)
	}

	return processZip(zipPath)
}

// fetchTSBFileList discovers available TSBS files by testing which ones exist
func fetchTSBFileList() ([]string, error) {
	year := time.Now().Year()

	candidates := []string{
		// Historical files
		"TSBS_RECEIVED_1995-1999.zip",
		"TSBS_RECEIVED_2000-2004.zip",
		"TSBS_RECEIVED_2005-2009.zip",
		"TSBS_RECEIVED_2010-2014.zip",
		"TSBS_RECEIVED_2015-2019.zip",
		"TSBS_RECEIVED_2020-2024.zip",
		// Current year files (2025-2026)
		"TSBS_RECEIVED_2025-2026.zip",
		// Extend current range by +1 to +5 years (2027-2030)
		fmt.Sprintf("TSBS_RECEIVED_2025-%d.zip", year+1),
		fmt.Sprintf("TSBS_RECEIVED_2025-%d.zip", year+2),
		fmt.Sprintf("TSBS_RECEIVED_2025-%d.zip", year+3),
		fmt.Sprintf("TSBS_RECEIVED_2025-%d.zip", year+4),
		fmt.Sprintf("TSBS_RECEIVED_2025-%d.zip", year+5),
		// Next decade (2030-2035) when range resets
		fmt.Sprintf("TSBS_RECEIVED_2030-%d.zip", year+5),
		fmt.Sprintf("TSBS_RECEIVED_2030-%d.zip", year+6),
		fmt.Sprintf("TSBS_RECEIVED_2030-%d.zip", year+7),
		fmt.Sprintf("TSBS_RECEIVED_2030-%d.zip", year+8),
		fmt.Sprintf("TSBS_RECEIVED_2030-%d.zip", year+9),
	}

	var files []string
	client := &http.Client{Timeout: 15 * time.Second}

	log.Printf("[Discovery] Checking for TSBS files...")

	for _, filename := range candidates {
		url := tsbBaseURL + filename
		req, err := http.NewRequest("HEAD", url, nil)
		if err != nil {
			log.Printf("[Discovery] %s - error creating request: %v", filename, err)
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("[Discovery] %s - HEAD failed: %v", filename, err)
			continue
		}
		resp.Body.Close()

		log.Printf("[Discovery] %s - Status: %d", filename, resp.StatusCode)

		if resp.StatusCode == 200 {
			files = append(files, filename)
		}
	}

	log.Printf("[Discovery] Found %d files: %v", len(files), files)

	if len(files) == 0 {
		return nil, fmt.Errorf("no TSBS files found")
	}

	sort.Strings(files)
	return files, nil
}

func downloadFile(url, path string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func processZip(zipPath string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		nameLower := strings.ToLower(f.Name)
		isCSV := strings.HasSuffix(nameLower, ".csv")
		isTSV := strings.HasSuffix(nameLower, ".tsv") || strings.HasSuffix(nameLower, ".txt")
		if !isCSV && !isTSV {
			continue
		}

		log.Printf("  Processing %s...", f.Name)

		rc, err := f.Open()
		if err != nil {
			continue
		}

		// Read line by line using simple string split - more resilient to malformed data
		var records [][]string
		lineNum := 0
		scanner := bufio.NewScanner(rc)
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			// Skip empty lines
			if strings.TrimSpace(line) == "" {
				continue
			}
			// Simple tab split - takes first 7 fields max to avoid issues with embedded tabs
			fields := strings.Split(line, "\t")
			// Trim each field
			for i := range fields {
				fields[i] = strings.TrimSpace(fields[i])
			}
			// Only keep first 15 fields (to capture: ID, dates, TSB#, type, MAKE, MODEL, YEAR, system, code, component, description)
			if len(fields) > 15 {
				fields = fields[:15]
			}
			records = append(records, fields)
		}
		rc.Close()

		if len(records) == 0 {
			log.Printf("  No records found in file")
			continue
		}

		// Check if first row is likely a header row
		if len(records) > 0 {
			hasHeader := hasHeaderRow(records[0])
			if hasHeader {
				records = records[1:]
			}
			log.Printf("  Parsed %d records (header: %v)", len(records), hasHeader)
		}

		// Process the records using positional mapping (files have no headers)
		processRecordsPositional(records)
	}

	return nil
}

func normalizeHeaders(headers []string) map[string]int {
	result := make(map[string]int)
	for i, h := range headers {
		result[strings.ToLower(strings.TrimSpace(h))] = i
	}
	return result
}

// Check if first row is likely data (not headers) by looking for known header patterns
func hasHeaderRow(firstRow []string) bool {
	if len(firstRow) == 0 {
		return false
	}
	first := strings.ToLower(firstRow[0])
	// Check for known header keywords
	headersToCheck := []string{"nhtsa id", "tsb/document", "make", "model", "model year", "mfr communication"}
	for _, h := range headersToCheck {
		if strings.Contains(first, h) {
			return true
		}
	}
	// Also check if first field looks like a number (likely data, not "make" or "model")
	if len(firstRow) > 3 {
		_, err := strconv.Atoi(strings.TrimSpace(firstRow[1]))
		if err == nil {
			return false // Second field is a number, this is likely data
		}
	}
	return false
}

func processRecords(records [][]string, headers map[string]int) {
	// Check if file has header row or starts with data
	if len(headers) == 0 || (len(records) > 0 && !hasHeaderRow(records[0])) {
		// File has no headers - use positional mapping based on TSV format:
		// Position: 0=Type, 1=ID, 2=Date, 3=Make, 4=Model, 5=Year, 6+=Summary
		log.Printf("    No header row detected, using positional mapping")
		processRecordsPositional(records)
		return
	}

	extIDIdx := headers["tsb/document id"]
	makeIdx := headers["make"]
	modelIdx := headers["model"]
	yearIdx := headers["model year"]
	dateIdx := headers["mfr communication date"]
	descIdx := headers["summary"]

	// Skip if required columns not found (use ok check instead of == 0)
	_, makeOk := headers["make"]
	_, modelOk := headers["model"]
	_, yearOk := headers["model year"]
	if !makeOk || !modelOk || !yearOk {
		log.Printf("    Missing required columns. Found headers: %v", func() []string {
			h := make([]string, 0, len(headers))
			for k := range headers {
				h = append(h, k)
			}
			return h
		}())
		return
	}

	tx, err := tsbDB.Begin()
	if err != nil {
		log.Printf("    Error starting transaction: %v", err)
		return
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO tsbs (external_id, make, model, year, comm_date, system, description)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		log.Printf("    Error preparing statement: %v", err)
		return
	}
	defer stmt.Close()

	count := 0
	for _, row := range records {
		if len(row) <= makeIdx || len(row) <= modelIdx || len(row) <= yearIdx {
			continue
		}

		extID := ""
		if extIDIdx > 0 && len(row) > extIDIdx {
			extID = cleanValue(row[extIDIdx])
		}
		make := cleanValue(row[makeIdx])
		model := cleanValue(row[modelIdx])

		// Debug: show first few records
		if count < 3 {
			log.Printf("    Row %d: make=%q, model=%q, year=%q", count, make, model, row[yearIdx])
		}

		year := 0
		if len(row) > yearIdx {
			year, _ = strconv.Atoi(cleanValue(row[yearIdx]))
		}

		commDate := ""
		if len(row) > dateIdx {
			commDate = cleanDate(row[dateIdx])
		}

		desc := ""
		if len(row) > descIdx {
			desc = cleanValue(row[descIdx])
		}

		if make == "" || model == "" || year == 0 {
			continue
		}

		_, err := stmt.Exec(extID, make, model, year, commDate, desc)
		if err == nil {
			count++
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("    Error committing: %v", err)
		return
	}

	log.Printf("  Inserted %d records", count)
}

// Process records using positional mapping (no headers in file)
// TSV format: Type, ID, Date, Make, Model, Year, ...Summary
func processRecordsPositional(records [][]string) {
	tx, err := tsbDB.Begin()
	if err != nil {
		log.Printf("    [Positional] Error starting transaction: %v", err)
		return
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO tsbs (external_id, make, model, year, comm_date, system, description)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		log.Printf("    [Positional] Error preparing statement: %v", err)
		return
	}
	defer stmt.Close()

	count := 0
	for _, row := range records {
		// Need at least: external_id, make, model, year (positions 0, 7, 8, 9)
		if len(row) < 10 {
			continue
		}

		// Positional mapping based on TSV format:
		// 0=NHTSA_ID, 1=empty, 2=received_date, 3=TSB_number, 4=date, 5=TSB_code, 6=type, 7=MAKE, 8=MODEL, 9=YEAR, 10=system, 11=code, 12=component, 13+=description
		extID := cleanValue(row[0])
		make := cleanValue(row[7])
		model := cleanValue(row[8])

		year := 0
		if len(row) > 9 {
			year, _ = strconv.Atoi(cleanValue(row[9]))
		}

		commDate := ""
		if len(row) > 2 {
			commDate = cleanDate(row[2])
		}

		system := ""
		if len(row) > 10 {
			system = cleanValue(row[10])
		}

		// Description is all remaining fields joined (from position 13 onwards)
		desc := ""
		if len(row) > 13 {
			desc = cleanValue(strings.Join(row[13:], " "))
		}

		// Insert if we have at least make or model (year can be 0/unknown)
		if make == "" && model == "" {
			continue
		}

		_, err := stmt.Exec(extID, make, model, year, commDate, system, desc)
		if err == nil {
			count++
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("    [Positional] Error committing: %v", err)
		return
	}

	log.Printf("  [Positional] Inserted %d records", count)
}

func cleanValue(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\x00", "")
	return s
}

func cleanDate(s string) string {
	s = strings.TrimSpace(s)
	// Handle YYYYMMDD format (e.g., "20231225")
	if len(s) == 8 {
		re := regexp.MustCompile(`^\d{8}$`)
		if re.MatchString(s) {
			return s[0:4] + "-" + s[4:6] + "-" + s[6:8]
		}
	}
	// Handle ISO format YYYY-MM-DD
	re := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)
	if re.MatchString(s) {
		return s[:10]
	}
	return ""
}

func cleanupOldZips() {
	os.MkdirAll(tsbDataDir, 0755)

	entries, err := os.ReadDir(tsbDataDir)
	if err != nil {
		log.Printf("Error reading data dir: %v", err)
		return
	}

	cutoff := time.Now().AddDate(0, 0, -zipRetentionDays)
	count := 0

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".zip") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoff) {
			path := filepath.Join(tsbDataDir, entry.Name())
			if err := os.Remove(path); err == nil {
				log.Printf("Removed old zip: %s", entry.Name())
				count++
			}
		}
	}

	log.Printf("Cleanup complete. Removed %d old files", count)
}

// ===== TSB API HANDLER =====

type TSBResponse struct {
	Results []TSB `json:"results"`
	Total   int   `json:"total"`
}

type TSB struct {
	ID          int    `json:"id"`
	ExternalID  string `json:"external_id"`
	Make        string `json:"make"`
	Model       string `json:"model"`
	Year        int    `json:"year"`
	CommDate    string `json:"comm_date"`
	System      string `json:"system"`
	Description string `json:"description"`
}

func tsbHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if tsbDB == nil {
		http.Error(w, `{"error": "TSB database not initialized"}`, http.StatusServiceUnavailable)
		return
	}

	make := r.URL.Query().Get("make")
	model := r.URL.Query().Get("model")
	year := r.URL.Query().Get("year")
	search := r.URL.Query().Get("search")
	limit := r.URL.Query().Get("max")
	if limit == "" {
		limit = r.URL.Query().Get("limit")
	}
	offset := r.URL.Query().Get("offset")

	if make == "" || model == "" || year == "" {
		http.Error(w, `{"error": "make, model, and year are required parameters"}`, http.StatusBadRequest)
		return
	}

	yearInt, err := strconv.Atoi(year)
	if err != nil {
		http.Error(w, `{"error": "invalid year"}`, http.StatusBadRequest)
		return
	}

	limitInt := 20
	if l, err := strconv.Atoi(limit); err == nil && l > 0 && l <= 10000 {
		limitInt = l
	}

	offsetInt := 0
	if o, err := strconv.Atoi(offset); err == nil && o > 0 {
		offsetInt = o
	}

	var query string
	var args []interface{}
	var countQuery string
	var countArgs []interface{}

	if search != "" {
		searchTerm := "%" + search + "%"
		query = `
			SELECT id, external_id, make, model, year, comm_date, system, description
			FROM tsbs
			WHERE LOWER(make) = LOWER(?) AND LOWER(model) = LOWER(?) AND year = ?
			AND (LOWER(description) LIKE LOWER(?) OR LOWER(system) LIKE LOWER(?))
			ORDER BY comm_date DESC
			LIMIT ? OFFSET ?
		`
		args = []interface{}{make, model, yearInt, searchTerm, searchTerm, limitInt, offsetInt}
		countQuery = `SELECT COUNT(*) FROM tsbs WHERE LOWER(make) = LOWER(?) AND LOWER(model) = LOWER(?) AND year = ? AND (LOWER(description) LIKE LOWER(?) OR LOWER(system) LIKE LOWER(?))`
		countArgs = []interface{}{make, model, yearInt, searchTerm, searchTerm}
	} else {
		query = `
			SELECT id, external_id, make, model, year, comm_date, system, description
			FROM tsbs
			WHERE LOWER(make) = LOWER(?) AND LOWER(model) = LOWER(?) AND year = ?
			ORDER BY comm_date DESC
			LIMIT ? OFFSET ?
		`
		args = []interface{}{make, model, yearInt, limitInt, offsetInt}
		countQuery = `SELECT COUNT(*) FROM tsbs WHERE LOWER(make) = LOWER(?) AND LOWER(model) = LOWER(?) AND year = ?`
		countArgs = []interface{}{make, model, yearInt}
	}

	rows, err := tsbDB.Query(query, args...)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%v"}`, err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var tsbs []TSB
	for rows.Next() {
		var t TSB
		if err := rows.Scan(&t.ID, &t.ExternalID, &t.Make, &t.Model, &t.Year, &t.CommDate, &t.System, &t.Description); err != nil {
			continue
		}
		tsbs = append(tsbs, t)
	}

	var total int
	tsbDB.QueryRow(countQuery, countArgs...).Scan(&total)

	resp := TSBResponse{
		Results: tsbs,
		Total:   total,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func decodeVINHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		return
	}

	vin := strings.ToUpper(r.URL.Query().Get("vin"))
	if vin == "" {
		http.Error(w, `{"error": "vin parameter is required"}`, http.StatusBadRequest)
		return
	}

	vin = strings.ReplaceAll(vin, " ", "")
	if len(vin) != 17 {
		http.Error(w, `{"error": "VIN must be exactly 17 characters"}`, http.StatusBadRequest)
		return
	}

	log.Printf("[VIN] Decoding: %s", vin)

	type VINResult struct {
		Results []map[string]interface{} `json:"Results"`
	}

	if vpicDB != nil {
		var cachedData string
		err := vpicDB.QueryRow("SELECT data FROM vin_cache WHERE vin = ?", vin).Scan(&cachedData)
		if err == nil {
			var cached VINResult
			if json.Unmarshal([]byte(cachedData), &cached) == nil && len(cached.Results) > 0 {
				log.Printf("[VIN] Found in local cache: %s", vin)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Local-Cache", "true")
				json.NewEncoder(w).Encode(cached)
				return
			}
		}
	}

	log.Printf("[VIN] Not in local cache, trying NHTSA API: %s", vin)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(fmt.Sprintf("https://vpic.nhtsa.dot.gov/api/vehicles/DecodeVinValues/%s?format=json", vin))
	if err != nil {
		http.Error(w, `{"error": "Failed to decode VIN - service temporarily unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		http.Error(w, `{"error": "Failed to decode VIN - upstream error"}`, resp.StatusCode)
		return
	}

	var nhtsaResp VINResult
	if err := json.NewDecoder(resp.Body).Decode(&nhtsaResp); err != nil {
		http.Error(w, `{"error": "Failed to parse VIN response"}`, http.StatusInternalServerError)
		return
	}

	if vpicDB != nil && len(nhtsaResp.Results) > 0 {
		go func() {
			if data, err := json.Marshal(nhtsaResp); err == nil {
				if _, err := vpicDB.Exec("INSERT OR REPLACE INTO vin_cache (vin, data, decoded_at) VALUES (?, ?, ?)", vin, string(data), time.Now().UTC().Format(time.RFC3339)); err != nil {
					log.Printf("[VIN] Failed to save to local cache: %v", err)
				} else {
					log.Printf("[VIN] Saved to local cache: %s", vin)
				}
			}
		}()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(nhtsaResp)
}
