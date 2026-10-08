package smbclientresource

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/rs/zerolog/log"
)

type EntryAttribute string

const (
	AttributeArchive   EntryAttribute = "A"
	AttributeDirectory EntryAttribute = "D"
	AttributeReadOnly  EntryAttribute = "R"
	AttributeHidden    EntryAttribute = "H"
	AttributeSystem    EntryAttribute = "S"

	AttributeNormal     EntryAttribute = "N"
	AttributeVolume     EntryAttribute = "V"
	AttributeTemporary  EntryAttribute = "T"
	AttributeCompressed EntryAttribute = "C"
	AttributeEncrypted  EntryAttribute = "E"
	AttributeOffline    EntryAttribute = "O"
	AttributeNotIndexed EntryAttribute = "I"

	ValidSmbAttributes string = "ADRHSNVTCEOI"
)

var entryAttrExpr = regexp.MustCompile(fmt.Sprintf(`^[%s]+$`, ValidSmbAttributes))

type SmbClientBaseEntry struct {
	Name       string         `json:"name"`
	Attributes EntryAttribute `json:"attributes"`
	Size       int64          `json:"size"`
	Timestamp  string         `json:"timestamp"`

	// basePath is empty by default.
	// Must be set using SetBasePath(string).
	basePath string
	// matchedRule *parsingruler.ParsingRule
}

// BasePath is empty by default.
// Must be set using SetBasePath(string).
func NewSmbClientEntry(name string, attributesStr string, size int64, timestamp string) SmbClientEntry {
	attributes := EntryAttribute(attributesStr)
	if !attributes.IsValid() {
		log.Fatal().Str("attributes_str", attributesStr).Msg("unexpected error: invalid entry attributes")
	}

	if attributes.File() {
		return &SmbClientFile{
			SmbClientBaseEntry{
				Name:       name,
				Size:       size,
				Attributes: attributes,
				Timestamp:  timestamp,
			},
		}
	} else if attributes.Folder() {
		return &SmbClientFolder{
			SmbClientBaseEntry: SmbClientBaseEntry{
				Name:       name,
				Size:       size,
				Attributes: attributes,
				Timestamp:  timestamp,
			},
			directory: nil,
		}
	}
	return nil
}

func (e EntryAttribute) HasAttribute(attr EntryAttribute) bool {
	return strings.Contains(string(e), string(attr))
}
func (e EntryAttribute) IsValid() bool {
	return entryAttrExpr.MatchString(string(e))
}
func (e EntryAttribute) Folder() bool {
	if !e.IsValid() {
		return false
	}
	return e.HasAttribute(AttributeDirectory)
}
func (e EntryAttribute) File() bool {
	if !e.IsValid() {
		return false
	}
	return !e.HasAttribute(AttributeDirectory)
}
func (e EntryAttribute) Archive() bool {
	if !e.IsValid() {
		return false
	}
	return e.HasAttribute(AttributeArchive)
}
func (e EntryAttribute) ReadOnly() bool {
	if !e.IsValid() {
		return false
	}
	return e.HasAttribute(AttributeReadOnly)
}
func (e EntryAttribute) Hidden() bool {
	if !e.IsValid() {
		return false
	}
	return e.HasAttribute(AttributeHidden)
}
func (e EntryAttribute) System() bool {
	if !e.IsValid() {
		return false
	}
	return e.HasAttribute(AttributeSystem)
}

func (e EntryAttribute) Normal() bool {
	if !e.IsValid() {
		return false
	}
	return e.HasAttribute(AttributeNormal)
}
func (e EntryAttribute) Volume() bool {
	if !e.IsValid() {
		return false
	}
	return e.HasAttribute(AttributeVolume)
}
func (e EntryAttribute) Temporary() bool {
	if !e.IsValid() {
		return false
	}
	return e.HasAttribute(AttributeTemporary)
}
func (e EntryAttribute) Compressed() bool {
	if !e.IsValid() {
		return false
	}
	return e.HasAttribute(AttributeCompressed)
}
func (e EntryAttribute) Encrypted() bool {
	if !e.IsValid() {
		return false
	}
	return e.HasAttribute(AttributeEncrypted)
}
func (e EntryAttribute) Offline() bool {
	if !e.IsValid() {
		return false
	}
	return e.HasAttribute(AttributeOffline)
}
func (e EntryAttribute) NotIndexed() bool {
	if !e.IsValid() {
		return false
	}
	return e.HasAttribute(AttributeNotIndexed)
}

func (s *SmbClientBaseEntry) IsEntry() bool {
	return true
}
func (s *SmbClientBaseEntry) GetName() string {
	return s.Name
}
func (s *SmbClientBaseEntry) IsDir() bool {
	return false
}
func (s *SmbClientBaseEntry) SetBasePath(basePath string) {
	s.basePath = basePath
}
func (s *SmbClientBaseEntry) GetFullPath() string {
	return fmt.Sprintf("%s/%s", s.basePath, s.Name)
}

// func (s *SmbClientBaseEntry) MatchedRule() *parsingruler.ParsingRule {
// 	return s.matchedRule
// }
