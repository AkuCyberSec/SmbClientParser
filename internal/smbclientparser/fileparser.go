package smbclientparser

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"smbclientparser/internal/parsingruler"
	"smbclientparser/internal/smbclientresource"
	"sync"

	"github.com/rs/zerolog/log"
)

type ParsedRow struct {
	id    int
	obj   SmbClientObject
	empty bool // If true, row is skipped
}
type FileParser struct {
	smbClientParser *SmbClientParser
	parsingRuler    *parsingruler.ParsingRuler
	rowParser       *RowParser
	fInfo           os.FileInfo

	parsingStatusMu sync.Mutex
	totReadBytes    int
	lastPerc        float64
}

func (f *FileParser) printCurrentParsingStatus(row []byte) {
	f.parsingStatusMu.Lock()
	defer f.parsingStatusMu.Unlock()

	f.totReadBytes += len(row)
	fileSize := float64(f.fInfo.Size())

	perc := math.Floor((100 / fileSize) * float64(f.totReadBytes))
	if f.lastPerc != perc {
		log.Info().Msg(fmt.Sprintf("current progress: %.0f%%", perc))
		f.lastPerc = perc
	}
}

func newFileParser(smbClientParser *SmbClientParser, parsingRuler *parsingruler.ParsingRuler) *FileParser {
	return &FileParser{
		smbClientParser: smbClientParser,
		parsingRuler:    parsingRuler,
		rowParser:       newRowParser(parsingRuler),
	}
}

// Parse the file.
//
// sharePath is the base path of the smbclient tree output.
// For example: smbclient //172.16.0.5/users | tee smbclient_out.txt
//
// sharePath would be: //172.16.0.5/users.
func (f *FileParser) Parse(filePath string, sharePath string) error {
	var threadId int = 0
	log.
		Info().
		Msg("started parsing in single thread mode")
	fHandle, err := os.Open(filePath)
	if err != nil {
		return err
	}
	fInfo, err := fHandle.Stat()
	if err != nil {
		return err
	}
	f.fInfo = fInfo

	currentPath := sharePath
	log.
		Debug().
		Str("current_path", currentPath).
		Msg("current path set")
	reader := bufio.NewReader(fHandle)

	var readNextRow bool = true
	var counter int
	for readNextRow {

		currentRow := counter
		counter++
		row, err := reader.ReadBytes('\n')
		// the last row won't end with new line
		if err != nil {
			log.
				Info().
				Int("row", currentRow).
				Msg("last row reached")
			readNextRow = false
		}

		f.printCurrentParsingStatus(row)

		log.
			Trace().
			Int("row", currentRow).
			Msg("parsing next row")

		obj := f.rowParser.Parse(row)

		// If object is neither a dir nor an entry, it's nil.
		if obj == nil {
			log.
				Trace().
				Int("row", currentRow).
				Msg("row parsed: not a dir nor an entry")
			continue
		}

		if obj.IsDir() {
			log.
				Trace().
				Int("row", currentRow).
				Msg("row is a directory change (starts with /)")
			dir := obj.(*smbclientresource.SmbClientDir)
			// The base path of a directory is ALWAYS the same: the sharePath
			dir.SetBasePath(sharePath)
			currentPath = fmt.Sprintf("%s/%s", sharePath, dir.Name)
			log.
				Debug().
				Str("current_path", currentPath).
				Msg("current path changed")
			f.smbClientParser.AddDir(dir, threadId)
		} else if obj.IsEntry() {

			entry := obj.(smbclientresource.SmbClientEntry)
			// The base path of an entry, is the current path
			entry.SetBasePath(currentPath)

			log.Trace().
				Int("row", currentRow).
				Bool("is_file", entry.IsFile()).
				Bool("is_folder", entry.IsFolder()).
				Str("full_path", entry.GetFullPath()).
				Msg("row is an entry")
			matchedRule, ok := f.parsingRuler.MatchRule(entry.GetName())
			if !ok {
				log.Trace().
					Int("row", currentRow).
					Bool("is_file", entry.IsFile()).
					Bool("is_folder", entry.IsFolder()).
					Str("full_path", entry.GetFullPath()).
					Msg("row not maching any rule")
				continue
			}
			log.Debug().
				Str("rule_name", matchedRule.Name).
				Str("expression", matchedRule.Expression).
				Str("rule_group", matchedRule.Group().Name).
				Int("row", currentRow).
				Bool("is_file", entry.IsFile()).
				Bool("is_folder", entry.IsFolder()).
				Str("full_path", entry.GetFullPath()).
				Msg("row matched rule")
			f.smbClientParser.AddEntry(entry, matchedRule, threadId)
		}
	}

	return nil
}
