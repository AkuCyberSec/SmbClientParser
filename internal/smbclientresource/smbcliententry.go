package smbclientresource

type SmbClientEntry interface {
	SetBasePath(string)
	GetName() string
	GetFullPath() string
	IsFile() bool
	IsFolder() bool
	IsDir() bool
	IsEntry() bool
}
