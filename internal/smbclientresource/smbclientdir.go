package smbclientresource

import (
	"fmt"
	"sync"
)

type SmbClientDir struct {
	Name    string                    `json:"name"`
	Entries map[string]SmbClientEntry `json:"entries"`

	// basePath is empty by default.
	// Must be set using SetBasePath(string).
	basePath                        string
	alreadyDeletedFromEmptyDirsList bool
	fullPath                        string
	mu                              sync.Mutex
}

func NewDir(name string) *SmbClientDir {
	return &SmbClientDir{
		Name:    name,
		Entries: make(map[string]SmbClientEntry),
	}
}

func (s *SmbClientDir) AddEntry(entry SmbClientEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.Entries[entry.GetName()]
	if !ok {
		s.Entries[entry.GetName()] = entry
	}
}

func (s *SmbClientDir) IsEmpty() bool {
	return len(s.Entries) == 0
}
func (s *SmbClientDir) IsDir() bool {
	return true
}
func (s *SmbClientDir) IsEntry() bool {
	return false
}

func (s *SmbClientDir) IsAlreadyDeletedFromEmptyDirsList() bool {
	return s.alreadyDeletedFromEmptyDirsList
}
func (s *SmbClientDir) SetAsDeletedFromEmptyDirstList() {
	s.alreadyDeletedFromEmptyDirsList = true
}
func (s *SmbClientDir) SetBasePath(basePath string) {
	s.basePath = basePath
}
func (s *SmbClientDir) GetFullPath() string {
	return fmt.Sprintf("%s/%s", s.basePath, s.Name)
}
