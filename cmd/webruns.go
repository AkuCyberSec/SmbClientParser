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
	"net/url"
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

	// Results filters: date format of the browser's datetime-local inputs, and "no size limit"
	filterTimeLayout = "2006-01-02T15:04"
	noSizeLimit      = -1
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
	RulerFile     string `json:"ruler_file"` // name of a ruler file in the upload folder, empty for the Rules tab
}

// runRules are the rules a run uses: a frozen copy of the Rules tab, or a ruler file of the upload folder.
type runRules struct {
	ruler         *parsingruler.ParsingRuler
	source        string   // shown to the user: absolute path of the ruler file, or a description
	usesLiveRuler bool     // true if copied from the Rules tab
	liveVersion   int      // version of the Rules tab that was copied (only if usesLiveRuler)
	warnings      []string // things the user should know before queuing the run
}

// attributeInfo explains one SMB attribute letter (legend and filters of the Results tab).
type attributeInfo struct {
	Letter smbclientresource.EntryAttribute `json:"letter"`
	Name   string                           `json:"name"`
}

var attributeLegend = []attributeInfo{
	{smbclientresource.AttributeArchive, "Archive"},
	{smbclientresource.AttributeDirectory, "Directory"},
	{smbclientresource.AttributeReadOnly, "Read-only"},
	{smbclientresource.AttributeHidden, "Hidden"},
	{smbclientresource.AttributeSystem, "System"},
	{smbclientresource.AttributeNormal, "Normal"},
	{smbclientresource.AttributeVolume, "Volume"},
	{smbclientresource.AttributeTemporary, "Temporary"},
	{smbclientresource.AttributeCompressed, "Compressed"},
	{smbclientresource.AttributeEncrypted, "Encrypted"},
	{smbclientresource.AttributeOffline, "Offline"},
	{smbclientresource.AttributeNotIndexed, "Not indexed"},
}

// webRun is one parse of an smbclient output, queued from the browser.
// All fields are protected by webServer.runsMu.
type webRun struct {
	id            int
	settings      runSettings
	status        runStatus
	errorMessage  string
	queuedAt      time.Time
	startedAt     time.Time
	finishedAt    time.Time
	rulerSource   string // where the rules come from (see runRules)
	usesLiveRuler bool
	rulerVersion  int // version of the Rules tab this run copied (only if usesLiveRuler)

	ruler          *parsingruler.ParsingRuler       // frozen copy of the rules, owned by this run
	parser         *smbclientparser.SmbClientParser // nil until the run starts
	cancel         context.CancelFunc               // nil until the run starts
	results        []resultRow                      // set when done; read-only from then on
	summary        []groupSummary                   // enabled groups and rules, for the sidebar
	resultsDropped bool                             // results freed to save memory (see -keep-runs)
	exports        []string                         // names of the files exported to the download folder (see webdelete.go)
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

	// Used by the text filters
	lowercasePath string
	lowercaseName string
}

// resultFilter holds the filters of the Results tab. Empty values (or noSizeLimit) don't filter.
type resultFilter struct {
	group             string // sidebar selection
	rule              string // sidebar selection
	pathContains      string // lowercase
	nameContains      string // lowercase
	entryType         string
	minSize           int64
	maxSize           int64
	modifiedFrom      string // displayTimeLayout, compared as text
	modifiedTo        string
	attributesWith    string // letters the entry must have
	attributesWithout string // letters the entry must not have
}

// groupRuleKey identifies a rule inside a group (used to count matches per rule).
type groupRuleKey struct {
	group string
	rule  string
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
	RulerSource    string      `json:"ruler_source"`
	Status         runStatus   `json:"status"`
	Error          string      `json:"error,omitempty"`
	Progress       float64     `json:"progress"` // percent
	Matches        int         `json:"matches"`
	HasResults     bool        `json:"has_results"`
	ResultsDropped bool        `json:"results_dropped"`
	Outdated       bool        `json:"outdated"` // the Rules tab changed after this run copied it
	Exports        []string    `json:"exports"`  // names of the files exported to the download folder
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
		RulerSource:    r.rulerSource,
		Status:         r.status,
		Error:          r.errorMessage,
		Matches:        len(r.results),
		HasResults:     r.results != nil,
		ResultsDropped: r.resultsDropped,
		Outdated:       r.usesLiveRuler && r.rulerVersion != currentRulerVersion,
		Exports:        append([]string{}, r.exports...), // a copy: the view is encoded after unlocking
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

// prepareRun validates the settings (the input file path becomes absolute) and loads the rules of the run.
func (s *webServer) prepareRun(settings *runSettings) (runRules, error) {
	if err := settings.validate(); err != nil {
		return runRules{}, err
	}
	inputPath, err := filepath.Abs(settings.InputFile)
	if err != nil {
		return runRules{}, err
	}
	if info, err := os.Stat(inputPath); err != nil || !info.Mode().IsRegular() {
		return runRules{}, fmt.Errorf("input file %s not found", inputPath)
	}
	settings.InputFile = inputPath

	var rules runRules
	if settings.RulerFile == "" {
		rules, err = s.rulesFromRulesTab()
	} else {
		rules, err = s.rulesFromUploadedFile(settings.RulerFile)
	}
	if err != nil {
		return runRules{}, err
	}
	// The delimiter chosen in the form applies to this run only
	rules.ruler.PathDelimiter = parsingruler.PathDelimiter(settings.PathDelimiter)
	rules.warnings = append(rules.warnings, enabledRulesWarnings(rules.ruler)...)
	return rules, nil
}

// handleCheckRun validates a run without queuing it and returns the warnings to show to the user.
func (s *webServer) handleCheckRun(w http.ResponseWriter, r *http.Request) {
	var settings runSettings
	if !readJSON(w, r, &settings) {
		return
	}
	rules, err := s.prepareRun(&settings)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"warnings": append([]string{}, rules.warnings...)})
}

// handleStartRun adds a run to the queue (the browser has already shown the warnings, see handleCheckRun).
func (s *webServer) handleStartRun(w http.ResponseWriter, r *http.Request) {
	var settings runSettings
	if !readJSON(w, r, &settings) {
		return
	}
	rules, err := s.prepareRun(&settings)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.runsMu.Lock()
	s.lastRunID++
	newRun := &webRun{
		id:            s.lastRunID,
		settings:      settings,
		status:        runQueued,
		queuedAt:      time.Now(),
		rulerSource:   rules.source,
		usesLiveRuler: rules.usesLiveRuler,
		rulerVersion:  rules.liveVersion,
		ruler:         rules.ruler,
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
	view := newRun.viewLocked(rules.liveVersion)
	s.runsMu.Unlock()

	if !queued {
		writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("the queue is full (%d runs), wait for some to finish", runQueueCapacity))
		return
	}
	s.activity.add(levelInfo, "Run #%d queued: %s (server %s, share %s, rules %s)", newRun.id, settings.InputFile, settings.Server, settings.Share, rules.source)
	for _, warning := range rules.warnings {
		s.activity.add(levelWarning, "Run #%d queued despite a warning: %s", newRun.id, warning)
	}
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
		summary = listEnabledRules(target.ruler)
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
		lowercaseName: strings.ToLower(entry.GetName()),
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

// listEnabledRules returns every enabled rule of every enabled group, in processing order:
// the structure of the sidebar of the Results tab (counts are filled by countMatches).
func listEnabledRules(ruler *parsingruler.ParsingRuler) []groupSummary {
	summary := []groupSummary{}
	for _, groupName := range ruler.GetSortedRuleGroupNames() {
		group := ruler.RuleGroups[groupName]
		if !group.Enabled {
			continue
		}
		groupItem := groupSummary{Name: groupName, Rules: []ruleSummary{}}
		for _, ruleName := range group.GetSortedRuleNames() {
			if group.Rules[ruleName].Enabled {
				groupItem.Rules = append(groupItem.Rules, ruleSummary{Name: ruleName})
			}
		}
		summary = append(summary, groupItem)
	}
	return summary
}

// countMatches returns a copy of the sidebar structure with the given match counts.
func countMatches(summary []groupSummary, counts map[groupRuleKey]int) []groupSummary {
	counted := make([]groupSummary, 0, len(summary))
	for _, group := range summary {
		groupItem := groupSummary{Name: group.Name, Rules: []ruleSummary{}}
		for _, rule := range group.Rules {
			matches := counts[groupRuleKey{group.Name, rule.Name}]
			groupItem.Rules = append(groupItem.Rules, ruleSummary{Name: rule.Name, Matches: matches})
			groupItem.Matches += matches
		}
		counted = append(counted, groupItem)
	}
	return counted
}

// handleRunResults returns one page of results.
// Query parameters: run, sort (column), order (asc or desc), page (from 1) and the filters (see parseResultFilter).
func (s *webServer) handleRunResults(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	id, _ := strconv.Atoi(query.Get("run"))
	filter, err := parseResultFilter(query)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
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

	// Sidebar counts follow every filter except the sidebar selection itself (group and rule).
	// Pointers, not copies, keep memory low with millions of results.
	counts := map[groupRuleKey]int{}
	totalFiltered := 0
	matching := []*resultRow{}
	for i := range results {
		row := &results[i]
		if !filter.matches(row) {
			continue
		}
		totalFiltered++
		counts[groupRuleKey{row.Group, row.Rule}]++
		if filter.matchesSidebarSelection(row) {
			matching = append(matching, row)
		}
	}

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
		"run":            view,
		"groups":         countMatches(summary, counts),
		"total_filtered": totalFiltered, // "All matches" in the sidebar
		"rows":           rows,
		"total":          len(matching),
		"page":           page,
		"pages":          pages,
		"page_size":      resultsPageSize,
	})
}

// parseResultFilter reads the filters from the query:
// group, rule, path, name (text contained, case-insensitive), type (file or folder),
// size_min, size_max (bytes), modified_from, modified_to (filterTimeLayout, both included),
// attributes_with, attributes_without (attribute letters, e.g. "HS").
func parseResultFilter(query url.Values) (resultFilter, error) {
	filter := resultFilter{
		group:             query.Get("group"),
		rule:              query.Get("rule"),
		pathContains:      strings.ToLower(query.Get("path")),
		nameContains:      strings.ToLower(query.Get("name")),
		entryType:         query.Get("type"),
		attributesWith:    strings.ToUpper(query.Get("attributes_with")),
		attributesWithout: strings.ToUpper(query.Get("attributes_without")),
	}

	switch filter.entryType {
	case "", entryTypeFile, entryTypeFolder:
	default:
		return resultFilter{}, fmt.Errorf("unknown type %q", filter.entryType)
	}

	var err error
	if filter.minSize, err = parseSizeFilter(query.Get("size_min")); err != nil {
		return resultFilter{}, err
	}
	if filter.maxSize, err = parseSizeFilter(query.Get("size_max")); err != nil {
		return resultFilter{}, err
	}

	if text := query.Get("modified_from"); text != "" {
		from, err := time.Parse(filterTimeLayout, text)
		if err != nil {
			return resultFilter{}, fmt.Errorf("invalid date %q", text)
		}
		filter.modifiedFrom = from.Format(displayTimeLayout)
	}
	if text := query.Get("modified_to"); text != "" {
		to, err := time.Parse(filterTimeLayout, text)
		if err != nil {
			return resultFilter{}, fmt.Errorf("invalid date %q", text)
		}
		// The whole last minute is included
		filter.modifiedTo = to.Add(time.Minute - time.Second).Format(displayTimeLayout)
	}

	for _, letter := range filter.attributesWith + filter.attributesWithout {
		if !strings.ContainsRune(smbclientresource.ValidSmbAttributes, letter) {
			return resultFilter{}, fmt.Errorf("unknown attribute %q", letter)
		}
	}
	return filter, nil
}

// parseSizeFilter returns noSizeLimit for an empty text.
func parseSizeFilter(text string) (int64, error) {
	if text == "" {
		return noSizeLimit, nil
	}
	size, err := strconv.ParseInt(text, 10, 64)
	if err != nil || size < 0 {
		return 0, fmt.Errorf("invalid size %q", text)
	}
	return size, nil
}

// matches checks every filter except the sidebar selection (group and rule).
func (f resultFilter) matches(row *resultRow) bool {
	switch {
	case f.pathContains != "" && !strings.Contains(row.lowercasePath, f.pathContains),
		f.nameContains != "" && !strings.Contains(row.lowercaseName, f.nameContains),
		f.entryType != "" && row.Type != f.entryType,
		f.minSize != noSizeLimit && row.Size < f.minSize,
		f.maxSize != noSizeLimit && row.Size > f.maxSize,
		f.modifiedFrom != "" && row.Modified < f.modifiedFrom,
		f.modifiedTo != "" && row.Modified > f.modifiedTo:
		return false
	}
	for _, letter := range f.attributesWith {
		if !strings.ContainsRune(row.Attributes, letter) {
			return false
		}
	}
	for _, letter := range f.attributesWithout {
		if strings.ContainsRune(row.Attributes, letter) {
			return false
		}
	}
	return true
}

func (f resultFilter) matchesSidebarSelection(row *resultRow) bool {
	return (f.group == "" || row.Group == f.group) && (f.rule == "" || row.Rule == f.rule)
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

	s.runsMu.Lock()
	s.trackExportLocked(target, request.FileName)
	s.runsMu.Unlock()

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
