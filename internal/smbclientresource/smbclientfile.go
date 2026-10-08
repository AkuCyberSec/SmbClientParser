package smbclientresource

type SmbClientFile struct {
	SmbClientBaseEntry
}

func (s *SmbClientFile) IsFile() bool {
	return true
}
func (s *SmbClientFile) IsFolder() bool {
	return false
}
