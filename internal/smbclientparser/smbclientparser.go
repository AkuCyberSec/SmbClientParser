package smbclientparser

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"smbclientparser/internal/parsingruler"
	"smbclientparser/internal/smbclientresource"
	"sync"

	"github.com/rs/zerolog/log"
)

const (
	ShowGroupName bool = true
	HideGroupName bool = false

	ShowRuleName bool = true
	HideRuleName bool = true

	ShowRulesWithNoMatches bool = true
	HideRulesWithNoMatches bool = false
)

type SmbClientParser struct {
	FilePath    string                                     `json:"-"`
	Directories map[string]*smbclientresource.SmbClientDir `json:"directories"`

	parsingRuler *parsingruler.ParsingRuler
	rootDir      *smbclientresource.SmbClientDir
	// Track all the entries that are folders so that we can easily link the "directory"
	folders map[string]*smbclientresource.SmbClientFolder
	// Track the empty dirs immediately so that we can easily remove them from "Directories"
	emptyDirs map[string]*smbclientresource.SmbClientDir
	mu        sync.Mutex
	finalized bool
}

func New(filePath string, server string, shareName string, parsingRuler *parsingruler.ParsingRuler) *SmbClientParser {
	s := &SmbClientParser{
		FilePath:     filePath,
		Directories:  make(map[string]*smbclientresource.SmbClientDir),
		emptyDirs:    make(map[string]*smbclientresource.SmbClientDir),
		folders:      make(map[string]*smbclientresource.SmbClientFolder),
		parsingRuler: parsingRuler,
	}

	server = parsingRuler.NormalizePath(server)
	shareName = parsingRuler.NormalizePath(shareName)

	s.rootDir = smbclientresource.NewDir(shareName)
	s.rootDir.SetBasePath(fmt.Sprintf("%s%s", parsingRuler.SharePrefix(), server))
	s.AddDir(s.rootDir)

	return s
}

func (s *SmbClientParser) AddDir(dir *smbclientresource.SmbClientDir) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.Directories[dir.GetFullPath()]
	if !ok {
		s.Directories[dir.GetFullPath()] = dir
		s.emptyDirs[dir.GetFullPath()] = dir
	}
}
func (s *SmbClientParser) AddEntry(currentPath string, entry smbclientresource.SmbClientEntry, matchedRule *parsingruler.ParsingRule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry == nil {
		log.Fatal().Msg("unexpected error: file cannot be nil")
	}
	existingDir, ok := s.Directories[currentPath]
	if !ok {
		log.Fatal().Str("dir_name", currentPath).Str("file_name", entry.GetName()).Msg("unexpected error: cannot add file to directory because it doesn't exist")
	}
	existingDir.AddEntry(entry)

	matchedRule.AddMatchedEntry(entry)
	if entry.IsFolder() {
		s.folders[entry.GetFullPath()] = entry.(*smbclientresource.SmbClientFolder)
	}
	if !existingDir.IsAlreadyDeletedFromEmptyDirsList() {
		delete(s.emptyDirs, currentPath)
		existingDir.SetAsDeletedFromEmptyDirstList()
	}
}

func (s *SmbClientParser) Parse() error {
	fileParser := newFileParser(s, s.parsingRuler)
	if err := fileParser.Parse(s.FilePath, s.rootDir.GetFullPath()); err != nil {
		return err
	}
	return nil
}

// Remove all empty directories from s.Directories.
//
// Calling this method is NOT mandatory, but strongly suggested.
func (s *SmbClientParser) Finalize() *SmbClientParser {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finalized {
		return s
	}

	for _, folder := range s.folders {
		dir, ok := s.Directories[folder.GetFullPath()]
		if dir.IsEmpty() {
			continue
		}
		if !ok {
			log.Error().Str("folder_name", folder.Name).Str("full_path", folder.GetFullPath()).Msg("unexpected error: folder not contained in s.Directories")
			continue
		}
		folder.SetDirectory(dir)
	}

	for emptyDir := range s.emptyDirs {
		delete(s.Directories, emptyDir)
	}
	s.finalized = true
	return s
}

func (s *SmbClientParser) ExportJson(outFile string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	handle, err := os.Create(outFile)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(handle)
	encoder.SetIndent("", "	")
	if err := encoder.Encode(s); err != nil {
		return err
	}
	return nil
}

func (s *SmbClientParser) ExportGrouped(outFile string, showGroupName, showRuleName, showEmpty bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	handle, err := os.Create(outFile)
	if err != nil {
		return err
	}

	wr := bufio.NewWriter(handle)

	for _, grpName := range s.parsingRuler.GetSortedRuleGroupNames() {
		grp, ok := s.parsingRuler.RuleGroups[grpName]
		if !ok {
			log.Fatal().Str("group_name", grpName).Msg("fatal error: groupName not in parsingRuler.RuleGroups (maybe DeleteRuleGroup was called?)")
		}

		if !showEmpty && !grp.AnyMatch() {
			continue
		}
		if showGroupName {
			for range len(grpName) {
				fmt.Fprintf(wr, "=")
			}
			fmt.Fprintf(wr, "\n[%s]\n", grpName)
			for range len(grpName) {
				fmt.Fprintf(wr, "=")
			}
			fmt.Fprintf(wr, "\n")
		}

		for _, ruleName := range grp.GetSortedRuleNames() {
			rule, ok := grp.Rules[ruleName]
			if !ok {
				log.Fatal().Str("group_name", grpName).Str("rule_name", ruleName).Msg("fatal error: ruleName not in parsingRuleGroup.Rules (maybe DeleteRule was called?)")
			}

			if !showEmpty && !rule.AnyMatch() {
				continue
			}
			if showRuleName {
				fmt.Fprintf(wr, "[%s (expr: %s)]\n", ruleName, rule.Expression)
			}

			for _, entryName := range rule.GetSortedMatchedEntryNames() {
				entry, ok := rule.MatchedEntries(entryName)
				if !ok {
					log.Fatal().Str("group_name", grpName).Str("rule_name", ruleName).Str("entry_name", entryName).Msg("fatal error: entryName not in parsingRule.matchedEntries")
				}
				fmt.Fprintf(wr, "%s\n", entry.GetFullPath())
			}
			wr.WriteString("\n")
		}

		wr.WriteString("\n")
	}

	if err = wr.Flush(); err != nil {
		return err
	}
	return nil
}
