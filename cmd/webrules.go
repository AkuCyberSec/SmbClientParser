package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"smbclientparser/internal/parsingruler"
	"strings"
	"time"
)

const (
	// Ruler files in the upload folder are recognized by their extension
	rulerFileExtension = ".json"
	// Bigger .json files in the upload folder are not considered rulers (e.g. exported results)
	maxRulerFileBytes = 1 << 20 // 1 MiB
)

// rulerView is the ruler as shown in the browser. Groups and rules are in processing order
// (the first enabled rule that matches wins), and Position is their 1-based index in that order.
type rulerView struct {
	Path          string      `json:"path"` // absolute, empty if no ruler file is defined yet
	Dirty         bool        `json:"dirty"`
	Version       int         `json:"version"`
	PathDelimiter string      `json:"path_delimiter"`
	Groups        []groupView `json:"groups"`
}

type groupView struct {
	Position  int        `json:"position"`
	Name      string     `json:"name"`
	Enabled   bool       `json:"enabled"`
	IsDefault bool       `json:"is_default"`
	Rules     []ruleView `json:"rules"`
}

type ruleView struct {
	Position int    `json:"position"`
	Name     string `json:"name"`
	Regexp   string `json:"regexp"`
	Enabled  bool   `json:"enabled"`
}

// ruleRequest is the body of every ruler edit; each edit uses only the fields it needs.
type ruleRequest struct {
	Group         string `json:"group"`
	Rule          string `json:"rule"`
	Regexp        string `json:"regexp"`
	Enabled       bool   `json:"enabled"`
	PathDelimiter string `json:"path_delimiter"`
}

// rulerSnapshot is a frozen copy of the live ruler, with the state of the Rules tab when it was copied.
type rulerSnapshot struct {
	ruler   *parsingruler.ParsingRuler
	version int
	path    string // ruler file, empty if not defined
	dirty   bool   // unsaved changes
}

// uploadedRuler is a .json file of the upload folder, offered as rules for a run or to open in the Rules tab.
type uploadedRuler struct {
	Name          string    `json:"name"`
	Path          string    `json:"path"`
	Modified      time.Time `json:"modified"`
	PathDelimiter string    `json:"path_delimiter,omitempty"`
	Error         string    `json:"error,omitempty"` // why it can't be used
}

// rulerEdit changes the live ruler (called with rulerMu locked).
// It returns a description for the activity log, or an error for the user (nothing changed).
type rulerEdit func(request ruleRequest) (string, error)

// rulerViewLocked must be called with rulerMu locked.
func (s *webServer) rulerViewLocked() rulerView {
	view := rulerView{
		Path:          s.rulerPath,
		Dirty:         s.rulerDirty,
		Version:       s.rulerVersion,
		PathDelimiter: string(s.ruler.PathDelimiter),
		Groups:        []groupView{},
	}
	for groupIndex, groupName := range s.ruler.GetSortedRuleGroupNames() {
		group := s.ruler.RuleGroups[groupName]
		groupItem := groupView{
			Position:  groupIndex + 1,
			Name:      groupName,
			Enabled:   group.Enabled,
			IsDefault: group.IsDefaultGroup,
			Rules:     []ruleView{},
		}
		for ruleIndex, ruleName := range group.GetSortedRuleNames() {
			rule := group.Rules[ruleName]
			groupItem.Rules = append(groupItem.Rules, ruleView{
				Position: ruleIndex + 1,
				Name:     ruleName,
				Regexp:   rule.Expression,
				Enabled:  rule.Enabled,
			})
		}
		view.Groups = append(view.Groups, groupItem)
	}
	return view
}

func (s *webServer) currentRulerVersion() int {
	s.rulerMu.Lock()
	defer s.rulerMu.Unlock()
	return s.rulerVersion
}

// frozenRulerCopy returns an independent copy of the live ruler.
// Runs use a copy so that later edits don't affect them, and each run has its own matched entries.
func (s *webServer) frozenRulerCopy() (rulerSnapshot, error) {
	s.rulerMu.Lock()
	data, err := json.Marshal(s.ruler)
	snapshot := rulerSnapshot{version: s.rulerVersion, path: s.rulerPath, dirty: s.rulerDirty}
	s.rulerMu.Unlock()
	if err != nil {
		return rulerSnapshot{}, err
	}

	snapshot.ruler, err = parsingruler.LoadFromReader(bytes.NewReader(data))
	if err != nil {
		return rulerSnapshot{}, err
	}
	// The parser panics on invalid expressions: find them before it does
	if err := validateExpressions(snapshot.ruler); err != nil {
		return rulerSnapshot{}, err
	}
	return snapshot, nil
}

// loadRulerFile reads a ruler file and checks that a run can use it.
// The parsingruler package panics or stops the program on some invalid files: they are caught here.
func loadRulerFile(path string) (ruler *parsingruler.ParsingRuler, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxRulerFileBytes {
		return nil, fmt.Errorf("too large to be a ruler file (max %d KiB)", maxRulerFileBytes/1024)
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			ruler, err = nil, fmt.Errorf("not a valid ruler file (%v)", recovered)
		}
	}()
	ruler, err = parsingruler.Load(path)
	if err != nil {
		return nil, fmt.Errorf("not a valid ruler file (%w)", err)
	}
	if err := validatePathDelimiter(ruler.PathDelimiter); err != nil {
		return nil, err
	}
	if err := validateExpressions(ruler); err != nil {
		return nil, err
	}
	return ruler, nil
}

// rulesFromRulesTab returns a frozen copy of the Rules tab for a run, warning if it isn't saved in a file.
func (s *webServer) rulesFromRulesTab() (runRules, error) {
	snapshot, err := s.frozenRulerCopy()
	if err != nil {
		return runRules{}, err
	}
	rules := runRules{ruler: snapshot.ruler, source: snapshot.path, usesLiveRuler: true, liveVersion: snapshot.version}
	switch {
	case snapshot.path == "":
		rules.source = "Rules tab (not saved to a file)"
		rules.warnings = append(rules.warnings, "no ruler file is defined: these rules exist only in memory (save them in the Rules tab to keep them)")
	case snapshot.dirty:
		rules.source = snapshot.path + " (with unsaved changes)"
		rules.warnings = append(rules.warnings, fmt.Sprintf("the rules have unsaved changes: the run uses the rules shown in the Rules tab, not the content of %s", snapshot.path))
	}
	return rules, nil
}

// rulesFromUploadedFile loads a ruler file of the upload folder for a run.
func (s *webServer) rulesFromUploadedFile(name string) (runRules, error) {
	path, err := s.uploads.filePath(name)
	if err != nil {
		return runRules{}, err
	}
	ruler, err := loadRulerFile(path)
	if err != nil {
		return runRules{}, fmt.Errorf("%s: %w", path, err)
	}
	return runRules{ruler: ruler, source: path}, nil
}

// enabledRulesWarnings warns when no rule is enabled outside the default group.
func enabledRulesWarnings(ruler *parsingruler.ParsingRuler) []string {
	if ruler.AnyRuleGroupEnabled() {
		return nil
	}
	if isMatchAnyEnabled(ruler) {
		return []string{fmt.Sprintf("no rule is enabled outside the %s group: every file and folder will match %q", parsingruler.DefaultParsingGroupName, parsingruler.DefaultParsingRuleName)}
	}
	return []string{"no rule is enabled: nothing will match"}
}

func isMatchAnyEnabled(ruler *parsingruler.ParsingRuler) bool {
	group, ok := ruler.RuleGroups[parsingruler.DefaultParsingGroupName]
	if !ok || !group.Enabled {
		return false
	}
	rule, ok := group.Rules[parsingruler.DefaultParsingRuleName]
	return ok && rule.Enabled
}

// validateExpressions checks the expressions of all enabled rules in enabled groups.
func validateExpressions(ruler *parsingruler.ParsingRuler) error {
	for _, groupName := range ruler.GetSortedRuleGroupNames() {
		group := ruler.RuleGroups[groupName]
		if !group.Enabled {
			continue
		}
		for _, ruleName := range group.GetSortedRuleNames() {
			rule := group.Rules[ruleName]
			if !rule.Enabled {
				continue
			}
			if _, err := regexp.Compile(rule.Expression); err != nil {
				return fmt.Errorf("rule %q in group %q has an invalid regular expression: %v", ruleName, groupName, err)
			}
		}
	}
	return nil
}

func (s *webServer) handleGetRules(w http.ResponseWriter, r *http.Request) {
	s.rulerMu.Lock()
	view := s.rulerViewLocked()
	s.rulerMu.Unlock()
	writeJSON(w, http.StatusOK, view)
}

// handleRulerEdit decodes the request, applies the edit and replies with the updated ruler.
func (s *webServer) handleRulerEdit(edit rulerEdit) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request ruleRequest
		if !readJSON(w, r, &request) {
			return
		}

		s.rulerMu.Lock()
		description, err := edit(request)
		if err == nil {
			s.rulerDirty = true
			s.rulerVersion++
		}
		view := s.rulerViewLocked()
		s.rulerMu.Unlock()

		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.activity.add(levelInfo, "%s", description)
		writeJSON(w, http.StatusOK, view)
	}
}

// editableGroup returns a group that can be modified. The default group is read-only:
// parsingruler stops the program if its rules are modified.
func (s *webServer) editableGroup(name string) (*parsingruler.ParsingRuleGroup, error) {
	group, ok := s.ruler.RuleGroups[name]
	if !ok {
		return nil, fmt.Errorf("group %q doesn't exist", name)
	}
	if group.IsDefaultGroup {
		return nil, fmt.Errorf("group %q is built in and can't be modified (only enabled or disabled)", name)
	}
	return group, nil
}

func validateRegexp(expression string) error {
	if expression == "" {
		return errors.New("enter a regular expression")
	}
	if _, err := regexp.Compile(expression); err != nil {
		return fmt.Errorf("invalid regular expression: %v", err)
	}
	return nil
}

func (s *webServer) addGroup(request ruleRequest) (string, error) {
	name := strings.TrimSpace(request.Group)
	if name == "" {
		return "", errors.New("enter a group name")
	}
	if _, exists := s.ruler.RuleGroups[name]; exists {
		return "", fmt.Errorf("group %q already exists", name)
	}
	s.ruler.AddRuleGroup(name, request.Enabled)
	return fmt.Sprintf("Group %q added", name), nil
}

func (s *webServer) deleteGroup(request ruleRequest) (string, error) {
	if _, err := s.editableGroup(request.Group); err != nil {
		return "", err
	}
	s.ruler.DeleteRuleGroup(request.Group)
	return fmt.Sprintf("Group %q deleted", request.Group), nil
}

func (s *webServer) enableGroup(request ruleRequest) (string, error) {
	if _, ok := s.ruler.RuleGroups[request.Group]; !ok {
		return "", fmt.Errorf("group %q doesn't exist", request.Group)
	}
	if request.Enabled {
		s.ruler.EnableGroup(request.Group)
		return fmt.Sprintf("Group %q enabled", request.Group), nil
	}
	s.ruler.DisableGroup(request.Group)
	return fmt.Sprintf("Group %q disabled", request.Group), nil
}

func (s *webServer) addRule(request ruleRequest) (string, error) {
	group, err := s.editableGroup(request.Group)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(request.Rule)
	if name == "" {
		return "", errors.New("enter a rule name")
	}
	if _, exists := group.Rules[name]; exists {
		return "", fmt.Errorf("rule %q already exists in group %q", name, request.Group)
	}
	if err := validateRegexp(request.Regexp); err != nil {
		return "", err
	}
	group.AddRule(name, request.Regexp, request.Enabled)
	return fmt.Sprintf("Rule %q added to group %q: %s", name, request.Group, request.Regexp), nil
}

func (s *webServer) updateRule(request ruleRequest) (string, error) {
	group, err := s.editableGroup(request.Group)
	if err != nil {
		return "", err
	}
	if _, exists := group.Rules[request.Rule]; !exists {
		return "", fmt.Errorf("rule %q doesn't exist in group %q", request.Rule, request.Group)
	}
	if err := validateRegexp(request.Regexp); err != nil {
		return "", err
	}
	group.SetExpression(request.Rule, request.Regexp)
	return fmt.Sprintf("Rule %q in group %q changed: %s", request.Rule, request.Group, request.Regexp), nil
}

func (s *webServer) enableRule(request ruleRequest) (string, error) {
	group, err := s.editableGroup(request.Group)
	if err != nil {
		return "", err
	}
	if _, exists := group.Rules[request.Rule]; !exists {
		return "", fmt.Errorf("rule %q doesn't exist in group %q", request.Rule, request.Group)
	}
	if request.Enabled {
		group.EnableRule(request.Rule)
		return fmt.Sprintf("Rule %q in group %q enabled", request.Rule, request.Group), nil
	}
	group.DisableRule(request.Rule)
	return fmt.Sprintf("Rule %q in group %q disabled", request.Rule, request.Group), nil
}

func (s *webServer) deleteRule(request ruleRequest) (string, error) {
	group, err := s.editableGroup(request.Group)
	if err != nil {
		return "", err
	}
	if _, exists := group.Rules[request.Rule]; !exists {
		return "", fmt.Errorf("rule %q doesn't exist in group %q", request.Rule, request.Group)
	}
	group.DeleteRule(request.Rule)
	return fmt.Sprintf("Rule %q deleted from group %q", request.Rule, request.Group), nil
}

func (s *webServer) setPathDelimiter(request ruleRequest) (string, error) {
	delimiter := parsingruler.PathDelimiter(request.PathDelimiter)
	if err := validatePathDelimiter(delimiter); err != nil {
		return "", err
	}
	s.ruler.PathDelimiter = delimiter
	return fmt.Sprintf("Ruler path delimiter set to %s", delimiter), nil
}

func validatePathDelimiter(delimiter parsingruler.PathDelimiter) error {
	switch delimiter {
	case parsingruler.ForwardSlash, parsingruler.BackSlash:
		return nil
	}
	return fmt.Errorf("the path delimiter must be %s or %s", parsingruler.ForwardSlash, parsingruler.BackSlash)
}

// handleValidateRegexp checks an expression while the user is typing it (nothing is changed).
func (s *webServer) handleValidateRegexp(w http.ResponseWriter, r *http.Request) {
	var request ruleRequest
	if !readJSON(w, r, &request) {
		return
	}
	if err := validateRegexp(request.Regexp); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true})
}

// handleTestName tells which rule would match a file name, using the parser's own matching logic.
func (s *webServer) handleTestName(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	if request.Name == "" {
		writeError(w, http.StatusBadRequest, "enter a file or folder name to test")
		return
	}
	snapshot, err := s.frozenRulerCopy()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	rule, matched := snapshot.ruler.MatchRule(request.Name)
	if !matched {
		writeJSON(w, http.StatusOK, map[string]any{"matched": false})
		return
	}
	group := rule.Group()
	writeJSON(w, http.StatusOK, map[string]any{
		"matched":        true,
		"group":          group.Name,
		"rule":           rule.Name,
		"group_position": slices.Index(snapshot.ruler.GetSortedRuleGroupNames(), group.Name) + 1,
		"rule_position":  slices.Index(group.GetSortedRuleNames(), rule.Name) + 1,
	})
}

// handleSaveRuler writes the ruler to a path chosen by the user, which becomes the current ruler file.
func (s *webServer) handleSaveRuler(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Path      string `json:"path"`
		Overwrite bool   `json:"overwrite"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	if strings.TrimSpace(request.Path) == "" {
		writeError(w, http.StatusBadRequest, "enter the path of the ruler file")
		return
	}
	path, err := filepath.Abs(request.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("folder %s doesn't exist", filepath.Dir(path)))
		return
	}
	if fileExists(path) && !request.Overwrite {
		writeFileExists(w, path)
		return
	}

	s.rulerMu.Lock()
	err = s.ruler.ExportJson(path)
	if err == nil {
		s.rulerPath = path
		s.rulerDirty = false
	}
	view := s.rulerViewLocked()
	s.rulerMu.Unlock()

	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("cannot save %s: %v", path, err))
		return
	}
	s.activity.add(levelSuccess, "Rules saved to %s", path)
	writeJSON(w, http.StatusOK, view)
}

// handleExportRuler saves a copy of the ruler in the download folder (the current ruler file doesn't change).
func (s *webServer) handleExportRuler(w http.ResponseWriter, r *http.Request) {
	var request struct {
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

	s.rulerMu.Lock()
	err = s.ruler.ExportJson(path)
	s.rulerMu.Unlock()

	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("cannot save %s: %v", path, err))
		return
	}
	s.forgetExports(request.FileName) // in case it overwrote exported results
	s.activity.add(levelSuccess, "Copy of the rules saved to %s", path)
	writeJSON(w, http.StatusOK, map[string]string{"folder": downloadsFolderID, "name": request.FileName, "path": path})
}

// handleListUploadedRulers lists the .json files of the upload folder, telling which ones are valid rulers.
func (s *webServer) handleListUploadedRulers(w http.ResponseWriter, r *http.Request) {
	files, err := s.uploads.list()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("cannot read %s: %v", s.uploads.path, err))
		return
	}
	rulers := []uploadedRuler{}
	for _, file := range files {
		if !strings.EqualFold(filepath.Ext(file.Name), rulerFileExtension) {
			continue
		}
		item := uploadedRuler{Name: file.Name, Path: file.Path, Modified: file.Modified}
		if ruler, err := loadRulerFile(file.Path); err != nil {
			item.Error = err.Error()
		} else {
			item.PathDelimiter = string(ruler.PathDelimiter)
		}
		rulers = append(rulers, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"folder": s.uploads.path, "rulers": rulers})
}

// handleOpenUploadedRuler replaces the rules of the Rules tab with a ruler file of the upload folder,
// which becomes the current ruler file (Save writes to it).
func (s *webServer) handleOpenUploadedRuler(w http.ResponseWriter, r *http.Request) {
	var request struct {
		FileName string `json:"file_name"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	path, err := s.uploads.filePath(request.FileName)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ruler, err := loadRulerFile(path)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s: %v", path, err))
		return
	}

	s.rulerMu.Lock()
	s.ruler = ruler
	s.rulerPath = path
	s.rulerDirty = false
	s.rulerVersion++
	view := s.rulerViewLocked()
	s.rulerMu.Unlock()

	s.activity.add(levelInfo, "Rules opened from %s", path)
	writeJSON(w, http.StatusOK, view)
}
