package smbclientparser

import (
	"bufio"
	"fmt"
	"os"
	"smbclientparser/internal/parsingruler"
	"smbclientparser/internal/smbclientresource"
)

type FileParser struct {
	smbClientParser *SmbClientParser
	parsingRuler    *parsingruler.ParsingRuler
	rowParser       *RowParser
}

func newFileParser(smbClientParser *SmbClientParser, parsingRuler *parsingruler.ParsingRuler) *FileParser {
	return &FileParser{
		smbClientParser,
		parsingRuler,
		newRowParser(parsingRuler),
	}
}

// Parse the file.
//
// sharePath is the base path of the smbclient tree output.
// For example: smbclient //172.16.0.5/users | tee smbclient_out.txt
//
// sharePath would be: //172.16.0.5/users.
func (f *FileParser) Parse(filePath string, sharePath string) error {
	fHandle, err := os.Open(filePath)
	if err != nil {
		return err
	}

	currentPath := sharePath
	reader := bufio.NewReader(fHandle)

	var readNextRow bool = true
	for readNextRow {
		row, err := reader.ReadBytes('\n')
		// the last row won't end with new line
		if err != nil {
			readNextRow = false
		}

		obj := f.rowParser.Parse(sharePath, currentPath, row)

		// If object is neither a dir nor a file, it's nil.
		if obj == nil {
			continue
		}

		if obj.IsDir() {
			dir := obj.(*smbclientresource.SmbClientDir)
			currentPath = fmt.Sprintf("%s/%s", sharePath, dir.Name)
			f.smbClientParser.AddDir(dir)
		} else if obj.IsEntry() {
			entry := obj.(smbclientresource.SmbClientEntry)
			matchedRule, ok := f.parsingRuler.MatchRule(entry.GetName())
			if !ok {
				continue
			}
			f.smbClientParser.AddEntry(currentPath, entry, matchedRule)
		}
	}

	return nil
}
