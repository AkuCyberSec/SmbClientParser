package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"smbclientparser/internal/parsingruler"
	"smbclientparser/internal/smbclientparser"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func initializeLogger(level zerolog.Level) {
	writer := zerolog.ConsoleWriter{
		Out: os.Stdout,
	}

	writer.FormatTimestamp = func(i any) string {
		timestamp, err := time.Parse(time.RFC3339, i.(string))
		if err != nil {
			panic(err)
		}
		return fmt.Sprintf("[%02d/%02d/%d %02d:%02d:%02d]", timestamp.Day(), timestamp.Month(), timestamp.Year(), timestamp.Local().Hour(), timestamp.Local().Minute(), timestamp.Local().Second())
	}

	multi := io.MultiWriter(writer)
	log.Logger = zerolog.New(multi).With().Timestamp().Logger().Level(level)
}
func main() {

	rulerPath := flag.String("ruler", "", "Defines the parsing rules by specifying a ruler.json file (e.g., ruler.json)")
	inputPath := flag.String("input", "", "The file to parse (smbclient output) (e.g., smbclient_out.txt)")
	serverName := flag.String("server", "", "Server name (e.g., 172.16.0.5)")
	shareName := flag.String("share", "", "Share name (e.g., for //172.16.0.5/Shared, this value would be: Shared)")
	outPath := flag.String("output", "parser_output.txt", "Output file")
	pathDelimiter := flag.String("path-delimiter", "/", "Path delimiter to use for output file only (/ or \\)")
	matchAnyIfNoRulesExist := flag.Bool("match-any-by-default", false, "If true, match any entry if and only if when no rules have been defined (used when ruler file is not defined)")
	loggingLevel := flag.Int("log-level", 1, "Logging level from -1 to 5 (-1: Trace, 0: Debug, 1: Info, 2: Warning, 3: Error, 4: Fatal, 5: Panic). Default: 1 (Info)")
	flag.Parse()

	initializeLogger(zerolog.Level(*loggingLevel))

	if *serverName == "" {
		log.Warn().Msg("server name cannot be empty")
		return
	}

	if *shareName == "" {
		log.Warn().Msg("share name cannot be empty")
		return
	}

	if *inputPath == "" {
		log.Warn().Msg("input path cannot be empty")
		return
	}

	if *outPath == "" {
		log.Warn().Msg("output path cannot be empty")
		return
	}
	var ruler *parsingruler.ParsingRuler
	if *rulerPath == "" {
		ruler = parsingruler.New(parsingruler.PathDelimiter(*pathDelimiter), *matchAnyIfNoRulesExist)
	} else {
		var err error
		ruler, err = parsingruler.Load(*rulerPath)
		if err != nil {
			log.Fatal().Err(err).Msg("fatal error")
		}
	}

	parser := smbclientparser.New(*inputPath, *serverName, *shareName, ruler)
	if err := parser.Parse(); err != nil {
		log.Fatal().Err(err).Msg("fatal error")
	}
	parser.Finalize()

	err := parser.ExportGrouped(*outPath, smbclientparser.ShowGroupName, smbclientparser.ShowRuleName, smbclientparser.HideRulesWithNoMatches)
	if err != nil {
		log.Fatal().Err(err).Msg("fatal error while exporting")
	}
}
