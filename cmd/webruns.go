package main

import (
	"cmp"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"smbclientparser/internal/parsingruler"
	"smbclientparser/internal/smbclientparser"
	"smbclientparser/internal/smbclientresource"
	"strconv"
	"strings"
	"time"
)

const (
	runQueueCapacity = 100
	resultsPageSize  = 100

	// smbclient prints dates like "Wed Sep 23 19:42:55 2026"; the UI shows them sortable
	smbclientTimeLayout = "Mon Jan 2 15:04:05 2006"
	displayTimeLayout   = "2006-01-02 15:04:05"

	entryTypeFile   = "file"
	entryTypeFolder = "folder"

	exportFormatTXT  = "txt"
	exportFormatCSV  = "csv"
	exportFormatJSON = "json"

	percentComplete = 100
)

type runStatus string

const (
	runQueued     runStatus = "queued"
	runRunning    runStatus = "running"
	runCancelling runStatus = "cancelling" // cancel requested, waiting for the parser to stop
	runDone       runStatus = "done"
	runFailed     runStatus = "failed"
	runCancelled  runStatus = "cancelled"
)

// runSettings are the values of the "New run" form.
type runSettings struct {
	InputFile     string `json:"input_file"`
	Server        string `json:"server"`
	Share         string `json:"share"`
	PathDelimiter string `json:"path_delimiter"`
}

// webRun is one parse of an smbclient output, queued from the browser.
// All fields are protected by webServer.runsMu.
type webRun struct {
	id           int
	settings     runSettings
	status       runStatus
	errorMessage string
	queuedAt     time.Time
	startedAt    time.Time
	finishedAt   time.Time
	rulerVersion int // version of the live ruler this run copied

	ruler          *parsingruler.ParsingRuler       // frozen copy of the rules, owned by this run
	parser         *smbclientparser.SmbClientParser // nil until the run starts
	cancel         context.CancelFunc               // nil until the run starts
	results        []resultRow                      // set when done; read-only from then on
	summary        []groupSummary                   // matches per group and rule, for the sidebar
	resultsDropped bool                             // results freed to save memory (see -keep-runs)
}

// resultRow is one matched file or folder.
type resultRow struct {
	Group      string `json:"group"`
	Rule       string `json:"rule"`
	Path       string `json:"path"`
	Name       string `json:"name"`
	Type       string `json:"type"` // entryTypeFile or entryTypeFolder
	Size       int64  `json:"size"`
	Modified   string `json:"modified"`
	Attributes string `json:"attributes"`

	lowercasePath string // used by the text filter
}

type groupSummary struct {
	Name    string        `json:"name"`
	Matches int           `json:"matches"`
	Rules   []ruleSummary `json:"rules"`
}

type ruleSummary struct {
	Name    string `json:"name"`
	Matches int    `json:"matches"`
}

// runView is a run as shown in the browser.
type runView struct {
	ID             int         `json:"id"`
	Settings       runSettings `json:"settings"`
	Status         runStatus   `json:"status"`
	Error          string      `json:"error,omitempty"`
	Progress       float64     `json:"progress"` // percent
	Matches        int         `json:"matches"`
	HasResults     bool        `json:"has_results"`
	ResultsDropped bool        `json:"results_dropped"`
	Outdated       bool        `json:"outdated"` // rules changed after this run copied them
	QueuedAt       time.Time   `json:"queued_at"`
	StartedAt      *time.Time  `json:"started_at,omitempty"`
	FinishedAt     *time.Time  `json:"finished_at,omitempty"`
}

// resultSorters maps a column name to its comparison function. No column means processing order.
var resultSorters = map[string]func(a, b *resultRow) int{
	"group":      func(a, b *resultRow) int { return strings.Compare(a.Group, b.Group) },
	"rule":       func(a, b *resultRow) int { return strings.Compare(a.Rule, b.Rule) },
	"path":       func(a, b *resultRow) int { return strings.Compare(a.Path, b.Path) },
	"name":       func(a, b *resultRow) int { return strings.Compare(a.Name, b.Name) },
	"type":       func(a, b *resultRow) int { return strings.Compare(a.Type, b.Type) },
	"size":       func(a, b *resultRow) int { return cmp.Compare(a.Size, b.Size) },
	"modified":   func(a, b *resultRow) int { return strings.Compare(a.Modified, b.Modified) },
	"attributes": func(a, b *resultRow) int { return strings.Compare(a.Attributes, b.Attributes) },
}

func (settings *runSettings) validate() error {
	settings.InputFile = strings.TrimSpace(settings.InputFile)
	settings.Server = strings.TrimSpace(settings.Server)
	settings.Share = strings.TrimSpace(settings.Share)
	switch {
	case settings.InputFile == "":
		return errors.New("choose the input file (smbclient output)")
	case settings.Server == "":
		return errors.New("enter the server name")
	case settings.Share == "":
		return errors.New("enter the share name")
	}
	return validatePathDelimiter(parsingruler.PathDelimiter(settings.PathDelimiter))
}

// viewLocked must be called with runsMu locked.
func (r *webRun) viewLocked(currentRulerVersion int) runView {
	view := runView{
		ID:             r.id,
		Settings:       r.settings,
		Status:         r.status,
		Error:          r.errorMessage,
		Matches:        len(r.results),
		HasResults:     r.results != nil,
		ResultsDropped: r.resultsDropped,
		Outdated:       r.rulerVersion != currentRulerVersion,
		QueuedAt:       r.queuedAt,
		StartedAt:      optionalTime(r.startedAt),
		FinishedAt:     optionalTime(r.finishedAt),
	}
	switch r.status {
	case runRunning, runCancelling:
		view.Progress = r.parser.Progress()
		// An empty input file gives NaN, which JSON can't encode
		if math.IsNaN(view.Progress) || math.IsInf(view.Progress, 0) {
			view.Progress = 0
		}
	case runDone:
		view.Progress = percentComplete
	}
	return view
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (s *webServer) findRunLocked(id int) *webRun {
	for _, r := range s.runs {
		if r.id == id {
			return r
		}
	}
	return nil
}

func (s *webServer) handleListRuns(w http.ResponseWriter, r *http.Request) {
	currentVersion := s.currentRulerVersion()

	s.runsMu.Lock()
	views := make([]runView, 0, len(s.runs))
	for i := len(s.runs) - 1; i >= 0; i-- { // newest first
		views = append(views, s.runs[i].viewLocked(currentVersion))
	}
	s.runsMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"runs": views})
}

// handleStartRun adds a run to the queue, with a frozen copy of the current rules.
func (s *webServer) handleStartRun(w http.ResponseWriter, r *http.Request) {
	var settings runSettings
	if !readJSON(w, r, &settings) {
		return
	}
	if err := settings.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	inputPath, err := filepath.Abs(settings.InputFile)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if info, err := os.Stat(inputPath); err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("input file %s not found", inputPath))
		return
	}
	settings.InputFile = inputPath

	rulerCopy, rulerVersion, err := s.frozenRulerCopy()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The delimiter chosen in the form applies to this run only
	rulerCopy.PathDelimiter = parsingruler.PathDelimiter(settings.PathDelimiter)

	s.runsMu.Lock()
	s.lastRunID++
	newRun := &webRun{
		id:           s.lastRunID,
		settings:     settings,
		status:       runQueued,
		queuedAt:     time.Now(),
		rulerVersion: rulerVersion,
		ruler:        rulerCopy,
	}
	queued := true
	select {
	case s.runQueue <- newRun:
	default:
		queued = false
		newRun.status = runFailed
		newRun.errorMessage = "the queue is full"
		newRun.finishedAt = time.Now()
	}
	s.runs = append(s.runs, newRun)
	view := newRun.viewLocked(rulerVersion)
	s.runsMu.Unlock()

	if !queued {
		writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("the queue is full (%d runs), wait for some to finish", runQueueCapacity))
		return
	}
	s.activity.add(levelInfo, "Run #%d queued: %s (server %s, share %s)", newRun.id, settings.InputFile, settings.Server, settings.Share)
	writeJSON(w, http.StatusCreated, view)
}

func (s *webServer) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID int `json:"id"`
	}
	if !readJSON(w, r, &request) {
		return
	}

	s.runsMu.Lock()
	target := s.findRunLocked(request.ID)
	var message string
	switch {
	case target == nil:
		message = fmt.Sprintf("run #%d doesn't exist", request.ID)
	case target.status == runQueued:
		// The queue worker skips runs that are no longer queued
		target.status = runCancelled
		target.finishedAt = time.Now()
	case target.status == runRunning:
		target.status = runCancelling
		target.cancel()
	default:
		message = fmt.Sprintf("run #%d is not queued or running", request.ID)
	}
	s.runsMu.Unlock()

	if message != "" {
		writeError(w, http.StatusBadRequest, message)
		return
	}
	s.activity.add(levelWarning, "Run #%d: cancel requested", request.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// processRunQueue executes queued runs one at a time. It never returns.
func (s *webServer) processRunQueue() {
	for queuedRun := range s.runQueue {
		s.executeRun(queuedRun)
	}
}

func (s *webServer) executeRun(target *webRun) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.runsMu.Lock()
	if target.status != runQueued { // cancelled while waiting
		s.runsMu.Unlock()
		return
	}
	target.status = runRunning
	target.startedAt = time.Now()
	target.cancel = cancel
	target.parser = smbclientparser.New(target.settings.InputFile, target.settings.Server, target.settings.Share, target.ruler)
	parser := target.parser
	s.runsMu.Unlock()

	s.activity.add(levelInfo, "Run #%d started", target.id)
	err := parseSafely(ctx, parser)

	var results []resultRow
	var summary []groupSummary
	if err == nil {
		results = collectResults(target.ruler)
		summary = summarizeMatches(target.ruler)
	}

	s.runsMu.Lock()
	target.finishedAt = time.Now()
	elapsed := target.finishedAt.Sub(target.startedAt).Round(time.Second)
	switch {
	case err != nil && ctx.Err() != nil:
		target.status = runCancelled
	case err != nil:
		target.status = runFailed
		target.errorMessage = err.Error()
	default:
		target.status = runDone
		target.results = results
		target.summary = summary
	}
	status := target.status
	s.dropOldResultsLocked()
	s.runsMu.Unlock()

	switch status {
	case runDone:
		s.activity.add(levelSuccess, "Run #%d finished in %s: %d matches", target.id, elapsed, len(results))
	case runCancelled:
		s.activity.add(levelWarning, "Run #%d cancelled after %s", target.id, elapsed)
	default:
		s.activity.add(levelError, "Run #%d failed after %s: %v", target.id, elapsed, err)
	}
}

// parseSafely runs the parser, turning a panic into an error so the web server keeps running.
func parseSafely(ctx context.Context, parser *smbclientparser.SmbClientParser) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("the parser crashed: %v", recovered)
		}
	}()
	if err := parser.Parse(ctx); err != nil {
		return err
	}
	parser.Finalize()
	return nil
}

// dropOldResultsLocked frees the results of old runs, keeping only the newest -keep-runs.
// Must be called with runsMu locked.
func (s *webServer) dropOldResultsLocked() {
	kept := 0
	for i := len(s.runs) - 1; i >= 0; i-- {
		old := s.runs[i]
		if old.results == nil {
			continue
		}
		kept++
		if kept > s.params.keepRuns {
			old.results = nil
			old.summary = nil
			old.parser = nil
			old.ruler = nil
			old.resultsDropped = true
		}
	}
}

// collectResults lists the matches of a finished run in processing order (group, rule, path).
func collectResults(ruler *parsingruler.ParsingRuler) []resultRow {
	results := []resultRow{}
	for _, groupName := range ruler.GetSortedRuleGroupNames() {
		group := ruler.RuleGroups[groupName]
		for _, ruleName := range group.GetSortedRuleNames() {
			rule := group.Rules[ruleName]
			for _, path := range rule.GetSortedMatchedEntryNames() {
				entry, ok := rule.MatchedEntries(path)
				if !ok {
					continue
				}
				results = append(results, newResultRow(groupName, ruleName, entry))
			}
		}
	}
	return results
}

func newResultRow(groupName string, ruleName string, entry smbclientresource.SmbClientEntry) resultRow {
	// Size, attributes and timestamp are not part of the SmbClientEntry interface
	var details smbclientresource.SmbClientBaseEntry
	entryType := entryTypeFile
	switch typedEntry := entry.(type) {
	case *smbclientresource.SmbClientFile:
		details = typedEntry.SmbClientBaseEntry
	case *smbclientresource.SmbClientFolder:
		details = typedEntry.SmbClientBaseEntry
		entryType = entryTypeFolder
	}

	return resultRow{
		Group:         groupName,
		Rule:          ruleName,
		Path:          entry.GetFullPath(),
		Name:          entry.GetName(),
		Type:          entryType,
		Size:          details.Size,
		Modified:      formatSmbclientTime(details.Timestamp),
		Attributes:    string(details.Attributes),
		lowercasePath: strings.ToLower(entry.GetFullPath()),
	}
}

// formatSmbclientTime returns "2026-09-23 19:42:55", or the original text if it can't be parsed.
func formatSmbclientTime(timestamp string) string {
	parsed, err := time.Parse(smbclientTimeLayout, timestamp)
	if err != nil {
		return timestamp
	}
	return parsed.Format(displayTimeLayout)
}

// summarizeMatches counts matches for every enabled rule of every enabled group, in processing order.
func summarizeMatches(ruler *parsingruler.ParsingRuler) []groupSummary {
	summary := []groupSummary{}
	for _, groupName := range ruler.GetSortedRuleGroupNames() {
		group := ruler.RuleGroups[groupName]
		if !group.Enabled {
			continue
		}
		groupItem := groupSummary{Name: groupName, Rules: []ruleSummary{}}
		for _, ruleName := range group.GetSortedRuleNames() {
			rule := group.Rules[ruleName]
			if !rule.Enabled {
				continue
			}
			matches := rule.MatchesNumber()
			groupItem.Rules = append(groupItem.Rules, ruleSummary{Name: ruleName, Matches: matches})
			groupItem.Matches += matches
		}
		summary = append(summary, groupItem)
	}
	return summary
}

// handleRunResults returns one page of results.
// Query parameters: run, group, rule, filter (text in the path), sort (column), order (asc or desc), page (from 1).
func (s *webServer) handleRunResults(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	id, _ := strconv.Atoi(query.Get("run"))
	currentVersion := s.currentRulerVersion()

	s.runsMu.Lock()
	target := s.findRunLocked(id)
	if target == nil || target.results == nil {
		s.runsMu.Unlock()
		writeError(w, http.StatusNotFound, fmt.Sprintf("results of run #%d are not available", id))
		return
	}
	view := target.viewLocked(currentVersion)
	results := target.results // read-only once set, safe to use after unlocking
	summary := target.summary
	s.runsMu.Unlock()

	matching := filterResults(results, query.Get("group"), query.Get("rule"), query.Get("filter"))
	if sorter, ok := resultSorters[query.Get("sort")]; ok {
		descending := query.Get("order") == "desc"
		slices.SortStableFunc(matching, func(a, b *resultRow) int {
			if descending {
				return sorter(b, a)
			}
			return sorter(a, b)
		})
	}

	pages := max((len(matching)+resultsPageSize-1)/resultsPageSize, 1)
	page, _ := strconv.Atoi(query.Get("page"))
	page = min(max(page, 1), pages)
	start := (page - 1) * resultsPageSize
	end := min(start+resultsPageSize, len(matching))

	rows := make([]resultRow, 0, end-start)
	for _, row := range matching[start:end] {
		rows = append(rows, *row)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run":       view,
		"groups":    summary,
		"rows":      rows,
		"total":     len(matching),
		"page":      page,
		"pages":     pages,
		"page_size": resultsPageSize,
	})
}

// filterResults returns pointers (not copies) to keep memory low with millions of results.
func filterResults(results []resultRow, group string, rule string, filter string) []*resultRow {
	lowercaseFilter := strings.ToLower(filter)
	matching := []*resultRow{}
	for i := range results {
		row := &results[i]
		if group != "" && row.Group != group {
			continue
		}
		if rule != "" && row.Rule != rule {
			continue
		}
		if lowercaseFilter != "" && !strings.Contains(row.lowercasePath, lowercaseFilter) {
			continue
		}
		matching = append(matching, row)
	}
	return matching
}

// handleExportRun saves all the results of a run in the download folder.
func (s *webServer) handleExportRun(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID        int    `json:"id"`
		Format    string `json:"format"`
		FileName  string `json:"file_name"`
		Overwrite bool   `json:"overwrite"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	path, err := s.downloads.filePath(request.FileName)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if fileExists(path) && !request.Overwrite {
		writeFileExists(w, path)
		return
	}

	s.runsMu.Lock()
	target := s.findRunLocked(request.ID)
	var results []resultRow
	var parser *smbclientparser.SmbClientParser
	if target != nil {
		results = target.results
		parser = target.parser
	}
	s.runsMu.Unlock()
	if results == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("results of run #%d are not available", request.ID))
		return
	}

	switch request.Format {
	case exportFormatTXT:
		// Same format as the command line output file
		err = parser.ExportGrouped(path, smbclientparser.ShowGroupName, smbclientparser.ShowRuleName, smbclientparser.HideRulesWithNoMatches)
	case exportFormatCSV:
		err = writeResultsCSV(path, results)
	case exportFormatJSON:
		err = writeResultsJSON(path, results)
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown export format %q", request.Format))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("cannot save %s: %v", path, err))
		return
	}

	s.activity.add(levelSuccess, "Run #%d exported to %s", request.ID, path)
	writeJSON(w, http.StatusOK, map[string]string{"folder": downloadsFolderID, "name": request.FileName, "path": path})
}

func writeResultsCSV(path string, results []resultRow) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	writer.Write([]string{"group", "rule", "path", "name", "type", "size", "modified", "attributes"})
	for _, row := range results {
		writer.Write([]string{row.Group, row.Rule, row.Path, row.Name, row.Type, strconv.FormatInt(row.Size, 10), row.Modified, row.Attributes})
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return err
	}
	return file.Close()
}

func writeResultsJSON(path string, results []resultRow) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "\t")
	if err := encoder.Encode(results); err != nil {
		return err
	}
	return file.Close()
}
