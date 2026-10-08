package smbclientparser

import (
	"smbclientparser/internal/parsingruler"
	"strings"
)

type RowParser struct {
	parsingRuler *parsingruler.ParsingRuler
}

type SmbClientObject interface {
	// If true, the current directory changed (row starts with "/")
	IsDir() bool
	// If true, the row indicates a dir entry (either a file or a folder).
	IsEntry() bool
}

func newRowParser(parsingRuler *parsingruler.ParsingRuler) *RowParser {
	return &RowParser{
		parsingRuler,
	}
}

// If path changes, returns SmbClientDir.
// If the resource is a directory or a file, it returns SmbClientResource.
//
// Otherwise, it returns nil.
func (r *RowParser) Parse(sharePath, currentPath string, row []byte) SmbClientObject {
	rowStr := strings.Trim(string(row), " ")
	rowStr, _ = strings.CutSuffix(rowStr, "\n")

	newDir, ok := r.parsingRuler.IsRowDir(rowStr)
	if ok {
		newDir.SetBasePath(sharePath)
		return newDir
	}

	entry, ok := r.parsingRuler.IsRowEntry(rowStr)
	if ok {
		// Ignore "current directory" and "parent directory" entries
		if entry.GetName() == "." || entry.GetName() == ".." {
			return nil
		}
		entry.SetBasePath(currentPath)
		return entry
	}
	return nil
}
