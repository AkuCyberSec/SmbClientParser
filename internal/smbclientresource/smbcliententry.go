package smbclientresource

type SmbClientEntry interface {
	SetBasePath(string)
	GetBasePath() string
	GetName() string
	GetFullPath() string
	IsFile() bool
	IsFolder() bool
	IsDir() bool
	IsEntry() bool
}
