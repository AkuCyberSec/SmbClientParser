package smbclientresource

type SmbClientFolder struct {
	SmbClientBaseEntry
	directory *SmbClientDir
}

func (s *SmbClientFolder) IsFile() bool {
	return false
}
func (s *SmbClientFolder) IsFolder() bool {
	return true
}

// Returns the linked directory
func (s *SmbClientFolder) Directory() *SmbClientDir {
	return s.directory
}
func (s *SmbClientFolder) SetDirectory(dir *SmbClientDir) {
	s.directory = dir
}
