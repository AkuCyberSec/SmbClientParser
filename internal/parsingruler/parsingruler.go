package parsingruler

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"smbclientparser/internal/smbclientresource"

	"strconv"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
)

type PathDelimiter string

const (
	BackSlash    PathDelimiter = `\`
	ForwardSlash PathDelimiter = `/`

	DefaultParsingGroupName string = "(default)"
	DefaultParsingRuleName  string = "Match any"
	DefaultParsingRuleExpr  string = `.+`

	EnableGroup  bool = true
	DisableGroup bool = false
	EnableRule   bool = true
	DisableRule  bool = false

	EnableMatchAnyIfNoRulesExist  bool = true
	DisableMatchAnyIfNoRulesExist bool = false
)

type ParsingRuleGroup struct {
	Name                   string                  `json:"group"`
	Rules                  map[string]*ParsingRule `json:"rules"`
	Enabled                bool                    `json:"enabled"`
	IsDefaultGroup         bool                    `json:"is_default_group"`
	sortedRuleNames        []string
	ruleNamesAlreadySorted bool
	mu                     sync.Mutex
}
type ParsingRule struct {
	Name                    string `json:"name"`
	Expression              string `json:"regexp"`
	Enabled                 bool   `json:"enabled"`
	IsDefaultRule           bool   `json:"is_default_rule"`
	expression              *regexp.Regexp
	group                   *ParsingRuleGroup
	matchedEntries          map[string]smbclientresource.SmbClientEntry
	sortedMatchedEntryNames []string
	mu                      sync.Mutex
}
type ParsingRuler struct {
	PathDelimiter PathDelimiter                `json:"path_delimiter"`
	RuleGroups    map[string]*ParsingRuleGroup `json:"rule_groups"`

	sortedGroupNames        []string
	groupNamesAlreadySorted bool
	// This value is used only on startup.
	// To enable or disable the "Match Any" behavior, use EnableDefaultRule() or DisableDefaultRule()
	//
	// This behavior works if and only if
	MatchAnyIfNoRulesExist bool `json:"match_any_if_no_rules_exist"`

	defaultRuleGroup *ParsingRuleGroup
	mu               sync.Mutex
}

func (p *ParsingRuler) ExportJson(outFile string) error {
	handle, err := os.Create(outFile)
	if err != nil {
		return err
	}
	defer handle.Close()
	encoder := json.NewEncoder(handle)
	encoder.SetIndent("", "	")
	if err := encoder.Encode(p); err != nil {
		return err
	}
	return nil
}

func Load(inFile string) (*ParsingRuler, error) {
	handle, err := os.Open(inFile)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	return LoadFromReader(handle)
}

// LoadFromReader builds a ruler from JSON (same format as ruler.json).
func LoadFromReader(reader io.Reader) (*ParsingRuler, error) {
	ruler := &ParsingRuler{}
	if err := json.NewDecoder(reader).Decode(ruler); err != nil {
		return nil, err
	}

	ruler.sortedGroupNames = make([]string, 0)
	if ruler.RuleGroups == nil {
		ruler.RuleGroups = make(map[string]*ParsingRuleGroup)
	}
	for grpName, grp := range ruler.RuleGroups {
		ruler.sortedGroupNames = append(ruler.sortedGroupNames, grpName)
		grp.sortedRuleNames = make([]string, 0)
		for ruleName, rule := range grp.Rules {
			grp.sortedRuleNames = append(grp.sortedRuleNames, ruleName)
			rule.sortedMatchedEntryNames = make([]string, 0)
			rule.group = grp
		}
	}
	ruler.addDefaultGroup()
	return ruler, nil
}
func newParsingRuleGroup(name string, enabled bool) *ParsingRuleGroup {
	g := &ParsingRuleGroup{
		Name:            name,
		Enabled:         enabled,
		Rules:           make(map[string]*ParsingRule),
		sortedRuleNames: make([]string, 0),
	}
	for key := range g.Rules {
		g.sortedRuleNames = append(g.sortedRuleNames, key)
	}
	return g
}
func newParsingRule(name string, expr string, enabled bool, group *ParsingRuleGroup) *ParsingRule {
	if group == nil {
		log.Fatal().Str("rule_name", name).Msg("fatal error: group cannot be nil")
	}
	return &ParsingRule{
		Name:                    name,
		Expression:              expr,
		Enabled:                 enabled,
		group:                   group,
		matchedEntries:          make(map[string]smbclientresource.SmbClientEntry),
		sortedMatchedEntryNames: make([]string, 0),
	}
}
func New(pathDelimiter PathDelimiter, matchAnyIfNoRulexExist bool) *ParsingRuler {
	switch pathDelimiter {
	case BackSlash, ForwardSlash:
		r := &ParsingRuler{
			PathDelimiter:          pathDelimiter,
			RuleGroups:             make(map[string]*ParsingRuleGroup),
			sortedGroupNames:       make([]string, 0),
			MatchAnyIfNoRulesExist: matchAnyIfNoRulexExist,
		}
		r.addDefaultGroup()
		r.EnableDefaultRule()
		return r
	default:
		log.Fatal().Str("path_delimiter", string(pathDelimiter)).Msg("fatal error: invalid path delimiter")
	}

	return nil
}

const (
	CapturingGroupDirectory                      string = "directory"
	CapturingGroupResourceName                   string = "resource"
	CapturingGroupResourceAttribute              string = "attribute"
	CapturingGroupResourceSize                   string = "size"
	CapturingGroupResourceModificationDateDayStr string = "daystr"
	CapturingGroupResourceModificationDateMonth  string = "month"
	CapturingGroupResourceModificationDateDay    string = "day"
	CapturingGroupResourceModificationDateTime   string = "time"
	CapturingGroupResourceModificationDateYear   string = "year"

	ValidSmbAttributes
)

var changePathExpr = regexp.MustCompile(fmt.Sprintf(`^(?P<%s>\\.+)`, CapturingGroupDirectory))

var resourceFileExp = regexp.MustCompile(
	fmt.Sprintf(`(?P<%s>.+)[ ]+(?P<%s>[%s]+)[ ]+(?P<%s>[0-9]+)[ ]+(?P<%s>[a-zA-Z]{3})[ ]+(?P<%s>[a-zA-Z]{3})[ ]+(?P<%s>[0-9]{1,2})[ ]+(?P<%s>[0-9]{2}:[0-9]{2}:[0-9]{2})[ ]+(?P<%s>[0-9]{4})$`,
		CapturingGroupResourceName,
		CapturingGroupResourceAttribute,
		smbclientresource.ValidSmbAttributes,
		CapturingGroupResourceSize,
		CapturingGroupResourceModificationDateDayStr,
		CapturingGroupResourceModificationDateMonth,
		CapturingGroupResourceModificationDateDay,
		CapturingGroupResourceModificationDateTime,
		CapturingGroupResourceModificationDateYear))

var ansiExpr = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|[@-_])`)

func (p *ParsingRuler) cleanAnsiRow(row string) string {
	clean := ansiExpr.ReplaceAllString(row, "")
	clean = strings.TrimLeft(clean, "\r")
	return clean
}

var normalizePathExpr = regexp.MustCompile(`([\\]{2,}|[/]{2,})`)
var suffixPathExpr = regexp.MustCompile(`([\\]+|[/]+)$`)
var prefixPathExpr = regexp.MustCompile(`^([\\]+|[/]+)`)

func (p *ParsingRuler) NormalizePath(path string) string {
	switch p.PathDelimiter {
	case BackSlash:
		path = strings.ReplaceAll(path, string(ForwardSlash), string(BackSlash))
	case ForwardSlash:
		path = strings.ReplaceAll(path, string(BackSlash), string(ForwardSlash))
	default:
		log.Fatal().Str("path_delimiter", string(p.PathDelimiter)).Msg("fatal error: invalid path delimiter")
	}
	path = normalizePathExpr.ReplaceAllString(path, string(p.PathDelimiter))
	path = suffixPathExpr.ReplaceAllString(path, "")
	path = prefixPathExpr.ReplaceAllString(path, "")
	return path
}

// Returns either // or \\ based on the current path delimiter
func (p *ParsingRuler) SharePrefix() string {
	return fmt.Sprintf("%s%s", p.PathDelimiter, p.PathDelimiter)
}

func (p *ParsingRuler) IsRowEntry(row string) (smbclientresource.SmbClientEntry, bool) {
	row = p.cleanAnsiRow(row)
	matches := resourceFileExp.FindStringSubmatch(row)
	if len(matches) < resourceFileExp.NumSubexp() {
		return nil, false
	}
	idName := resourceFileExp.SubexpIndex(CapturingGroupResourceName)
	idAttr := resourceFileExp.SubexpIndex(CapturingGroupResourceAttribute)
	idSize := resourceFileExp.SubexpIndex(CapturingGroupResourceSize)

	name := strings.Trim(matches[idName], " ")
	attr := matches[idAttr]
	sizeStr := matches[idSize]
	size, _ := strconv.ParseInt(sizeStr, 10, 64)

	// Same format smbclient prints, e.g. "Wed Sep 23 19:42:55 2026"
	timestamp := fmt.Sprintf("%s %s %s %s %s",
		matches[resourceFileExp.SubexpIndex(CapturingGroupResourceModificationDateDayStr)],
		matches[resourceFileExp.SubexpIndex(CapturingGroupResourceModificationDateMonth)],
		matches[resourceFileExp.SubexpIndex(CapturingGroupResourceModificationDateDay)],
		matches[resourceFileExp.SubexpIndex(CapturingGroupResourceModificationDateTime)],
		matches[resourceFileExp.SubexpIndex(CapturingGroupResourceModificationDateYear)])

	entry := smbclientresource.NewSmbClientEntry(p.NormalizePath(name), attr, size, timestamp)
	return entry, true
}

// If current path is chaning, returns the new path and result is true.
// Otherwise returns nil, false.
func (p *ParsingRuler) IsRowDir(row string) (*smbclientresource.SmbClientDir, bool) {
	row = p.cleanAnsiRow(row)
	matches := changePathExpr.FindStringSubmatch(row)
	if len(matches) < changePathExpr.NumSubexp() {
		return nil, false
	}
	id := changePathExpr.SubexpIndex(CapturingGroupDirectory)
	dir := matches[id]

	return smbclientresource.NewDir(p.NormalizePath(dir)), true

}

// Returns true if and only if the group has at least one enabled rule
func (p *ParsingRuleGroup) AnyRuleEnabled() bool {
	for _, rule := range p.Rules {
		if rule.IsDefaultRule {
			continue
		}
		if rule.Enabled {
			return true
		}
	}
	return false
}

// Returns true if and only if at least one enabled group has at least one enabled rule
func (p *ParsingRuler) AnyRuleGroupEnabled() bool {
	for _, grp := range p.RuleGroups {
		if grp.IsDefaultGroup {
			continue
		}
		if !grp.Enabled {
			continue
		}
		if grp.AnyRuleEnabled() {
			return true
		}
	}
	return false
}
func (p *ParsingRuler) MatchRule(val string) (matchedRule *ParsingRule, matched bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ignoreDefaultRule := p.AnyRuleGroupEnabled()

	for _, grpName := range p.GetSortedRuleGroupNames() {
		grp, ok := p.RuleGroups[grpName]
		if !ok {
			log.Fatal().Str("group_name", grpName).Msg("fatal error: group name doesn't exist in RuleGroups")
		}
		if !grp.Enabled {
			continue
		}
		if grp.IsDefaultGroup && ignoreDefaultRule {
			continue
		}
		for _, ruleName := range grp.GetSortedRuleNames() {
			rule, ok := grp.Rules[ruleName]
			if !ok {
				log.Fatal().Str("rule_name", ruleName).Msg("fatal error: rule name doesn't exist in Rules")
			}
			if !rule.Enabled {
				continue
			}
			if rule.expression == nil {
				rule.expression = regexp.MustCompile(rule.Expression)
			}
			if rule.expression.MatchString(val) {
				return rule, true
			}
		}
	}
	return nil, false
}
func (p *ParsingRuler) EnableDefaultRule() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.defaultRuleGroup == nil {
		log.Fatal().Msg("fatal error: defaultRuleGroup cannot be nil")
	}
	p.defaultRuleGroup.Enabled = true
	p.defaultRuleGroup.EnableRule(DefaultParsingRuleName)
}
func (p *ParsingRuler) DisableDefaultRule() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.defaultRuleGroup == nil {
		log.Fatal().Msg("fatal error: defaultRuleGroup cannot be nil")
	}
	p.defaultRuleGroup.Enabled = false
	p.defaultRuleGroup.DisableRule(DefaultParsingRuleName)
}
func (p *ParsingRuler) AddRuleGroup(name string, enabled bool) *ParsingRuleGroup {
	p.mu.Lock()
	defer p.mu.Unlock()
	if name == DefaultParsingGroupName {
		log.Fatal().Str("group_name", DefaultParsingGroupName).Msg("fatal error: default rule group cannot be modified (AddRuleGroup)")
	}
	return p.addRuleGroup(name, enabled)
}
func (p *ParsingRuler) DeleteRuleGroup(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if name == DefaultParsingGroupName {
		log.Fatal().Str("group_name", DefaultParsingGroupName).Msg("fatal error: default rule group cannot be modified (DeleteRuleGroup)")
	}
	p.deleteRuleGroup(name)
}
func (p *ParsingRuler) EnableGroup(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enableDisableGroup(name, EnableGroup)
}
func (p *ParsingRuler) DisableGroup(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enableDisableGroup(name, DisableGroup)
}
func (p *ParsingRuler) GetSortedRuleGroupNames() []string {
	if p.groupNamesAlreadySorted {
		return p.sortedGroupNames
	}
	slices.Sort(p.sortedGroupNames)
	p.groupNamesAlreadySorted = true
	return p.sortedGroupNames
}
func (p *ParsingRuler) addDefaultGroup() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.defaultRuleGroup != nil {
		log.Fatal().Str("group_name", DefaultParsingGroupName).Msg("fatal error: default rule group already defined")
	}
	grp := p.addRuleGroup(DefaultParsingGroupName, p.MatchAnyIfNoRulesExist)
	grp.IsDefaultGroup = true
	rule := grp.addRule(DefaultParsingRuleName, DefaultParsingRuleExpr, p.MatchAnyIfNoRulesExist)
	rule.IsDefaultRule = true
}
func (p *ParsingRuler) addRuleGroup(name string, enabled bool) *ParsingRuleGroup {
	grp, ok := p.RuleGroups[name]
	if ok {
		return grp
	}
	grp = newParsingRuleGroup(name, enabled)
	p.RuleGroups[name] = grp
	p.defaultRuleGroup = grp
	if p.sortedGroupNames == nil {
		p.sortedGroupNames = make([]string, 0)
	}
	p.sortedGroupNames = append(p.sortedGroupNames, name)
	p.groupNamesAlreadySorted = false
	return grp
}
func (p *ParsingRuler) deleteRuleGroup(name string) {
	if _, ok := p.RuleGroups[name]; !ok {
		return
	}
	delete(p.RuleGroups, name)
	p.sortedGroupNames = make([]string, 0, len(p.RuleGroups))
	for ruleGroupName := range p.RuleGroups {
		p.sortedGroupNames = append(p.sortedGroupNames, ruleGroupName)
	}
	p.groupNamesAlreadySorted = false
}
func (p *ParsingRuler) enableDisableGroup(name string, val bool) {
	grp, ok := p.RuleGroups[name]
	if !ok {
		return
	}
	grp.Enabled = val
}

func (p *ParsingRuleGroup) AddRule(name string, expr string, enabled bool) *ParsingRuleGroup {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Name == DefaultParsingGroupName {
		log.Fatal().Str("group_name", DefaultParsingGroupName).Msg("fatal error: default rule group cannot be modified (AddRule)")
	}
	p.addRule(name, expr, enabled)
	return p
}
func (p *ParsingRuleGroup) DeleteRule(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Name == DefaultParsingGroupName {
		log.Fatal().Str("group_name", DefaultParsingGroupName).Msg("fatal error: default rule group cannot be modified (DeleteRule)")
	}
	p.deleteRule(name)
}
func (p *ParsingRuleGroup) EnableRule(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enableDisableRule(name, EnableRule)
}
func (p *ParsingRuleGroup) DisableRule(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enableDisableRule(name, DisableRule)
}
func (p *ParsingRuleGroup) SetExpression(ruleName string, expr string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	rule, ok := p.Rules[ruleName]
	if !ok {
		return
	}
	rule.Expression = expr
}
func (p *ParsingRuleGroup) GetSortedRuleNames() []string {
	if p.ruleNamesAlreadySorted {
		return p.sortedRuleNames
	}
	slices.Sort(p.sortedRuleNames)
	p.ruleNamesAlreadySorted = true
	return p.sortedRuleNames
}
func (p *ParsingRuleGroup) AnyMatch() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, rule := range p.Rules {
		if rule.AnyMatch() {
			return true
		}
	}
	return false
}
func (p *ParsingRuleGroup) addRule(name string, expr string, enabled bool) *ParsingRule {
	rule, ok := p.Rules[name]
	if ok {
		return rule
	}
	rule = newParsingRule(name, expr, enabled, p)
	p.Rules[name] = rule
	if p.sortedRuleNames == nil {
		p.sortedRuleNames = make([]string, 0)
	}
	p.sortedRuleNames = append(p.sortedRuleNames, name)
	p.ruleNamesAlreadySorted = false
	return rule
}
func (p *ParsingRuleGroup) deleteRule(name string) {
	if _, ok := p.Rules[name]; !ok {
		return
	}
	delete(p.Rules, name)
	p.sortedRuleNames = make([]string, 0, len(p.Rules))
	for ruleName := range p.Rules {
		p.sortedRuleNames = append(p.sortedRuleNames, ruleName)
	}
	p.ruleNamesAlreadySorted = false
}
func (p *ParsingRuleGroup) enableDisableRule(name string, val bool) {
	rule, ok := p.Rules[name]
	if !ok {
		return
	}
	rule.Enabled = val
}

func (p *ParsingRule) SetExpression(expr string) {
	p.Expression = expr
}
func (p *ParsingRule) Group() *ParsingRuleGroup {
	if p.group == nil {
		log.Fatal().Str("rule_name", p.Name).Msg("fatal error: group is nil")
	}
	return p.group
}
func (p *ParsingRule) AnyMatch() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.matchedEntries) > 0
}
func (p *ParsingRule) MatchesNumber() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.matchedEntries)
}
func (p *ParsingRule) AddMatchedEntry(entry smbclientresource.SmbClientEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.matchedEntries == nil {
		p.matchedEntries = make(map[string]smbclientresource.SmbClientEntry)
	}
	if _, ok := p.matchedEntries[entry.GetFullPath()]; ok {
		return
	}
	p.matchedEntries[entry.GetFullPath()] = entry
	if p.sortedMatchedEntryNames == nil {
		p.sortedMatchedEntryNames = make([]string, 0)
	}
	p.sortedMatchedEntryNames = append(p.sortedMatchedEntryNames, entry.GetFullPath())
}
func (p *ParsingRule) GetSortedMatchedEntryNames() []string {
	slices.Sort(p.sortedMatchedEntryNames)
	return p.sortedMatchedEntryNames
}
func (p *ParsingRule) MatchedEntries(name string) (smbclientresource.SmbClientEntry, bool) {
	entry, ok := p.matchedEntries[name]
	return entry, ok
}
