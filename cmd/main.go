package main

import (
	"embed"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"smbclientparser/internal/parsingruler"
	"smbclientparser/internal/smbclientparser"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// embed ../web/*
var webFS embed.FS

type Parameters struct {
	rulerPath              string
	ruler                  *parsingruler.ParsingRuler
	serverName             string
	shareName              string
	inputFile              string
	outputFile             string
	pathDelimiter          parsingruler.PathDelimiter
	matchAnyIfNoRulesExist bool
	localAddress           string
	localPort              int
}

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

func run(params Parameters) {
	if err := params.validate(); err != nil {
		log.Warn().Err(err).Msg("invalid parameter")
	}

	now := time.Now()
	parser := smbclientparser.New(params.inputFile, params.serverName, params.shareName, params.ruler)

	if err := parser.Parse(); err != nil {
		log.Fatal().Err(err).Msg("fatal error")
	}

	diff := time.Since(now).Seconds()
	parser.Finalize()

	err := parser.ExportGrouped(params.outputFile, smbclientparser.ShowGroupName, smbclientparser.ShowRuleName, smbclientparser.HideRulesWithNoMatches)
	if err != nil {
		log.Fatal().Err(err).Msg("fatal error while exporting")
	}
	log.Info().Float64("elapsed seconds", diff).Msg("operation completed")

}
func (p *Parameters) validate() error {

	switch p.pathDelimiter {
	case "/", "\\":
		break
	default:
		return fmt.Errorf("invalid path delimiter (allowed values: \\ or /)")
	}

	if p.serverName == "" {
		return fmt.Errorf("server cannot be empty")
	}
	if p.shareName == "" {
		return fmt.Errorf("share cannot be empty")
	}
	if p.inputFile == "" {
		return fmt.Errorf("input cannot be empty")
	}
	if p.outputFile == "" {
		return fmt.Errorf("output cannot be empty")
	}

	return nil
}

var interactiveRegexp = regexp.MustCompile(`^(?i)(?P<command>run|exit|set|get|help|show params){1}( ?P<param1>(ruler|input|server|share|output|path-delimiter|match-any-by-default|log-level))?( ?P<param2>.+)$`)

// Run in web mode by starting an HTTP server locally.
//
// This is the only mode made by AI (Claude Code).
func runHttpServer(params Parameters) {

}

func main() {

	webMode := flag.Bool("web", false, "If true, enter web mode")
	httpLocalAddr := flag.String("local-addr", "127.0.0.1", "Local address to start the web mode on")
	httpLocalPort := flag.Int("local-port", 8080, "Local port to start the web mode on")

	rulerPath := flag.String("ruler", "", "Defines the parsing rules by specifying a ruler.json file (e.g., ruler.json)")
	inputFile := flag.String("input", "", "The file to parse (smbclient output) (e.g., smbclient_out.txt)")
	serverName := flag.String("server", "", "Server name (e.g., 172.16.0.5)")
	shareName := flag.String("share", "", "Share name (e.g., for //172.16.0.5/Shared, this value would be: Shared)")
	outputFile := flag.String("output", "parsed_output.txt", "Output file")
	pathDelimiter := flag.String("path-delimiter", "/", "Path delimiter to use for output file only (/ or \\)")
	matchAnyIfNoRulesExist := flag.Bool("match-any-by-default", false, "If true, match any entry if and only if when no rules have been defined (used when ruler file is not defined)")
	loggingLevel := flag.Int("log-level", 1, "Logging level from -1 to 6 (-1: Trace, 0: Debug, 1: Info (default), 2: Warning, 3: Error, 4: Fatal, 5: Panic, 6: Disabled)")
	flag.Parse()

	params := Parameters{
		rulerPath:              *rulerPath,
		serverName:             *serverName,
		shareName:              *shareName,
		inputFile:              *inputFile,
		outputFile:             *outputFile,
		pathDelimiter:          parsingruler.PathDelimiter(*pathDelimiter),
		matchAnyIfNoRulesExist: *matchAnyIfNoRulesExist,
		localAddress:           *httpLocalAddr,
		localPort:              *httpLocalPort,
	}

	initializeLogger(zerolog.Level(*loggingLevel))

	var ruler *parsingruler.ParsingRuler
	if params.rulerPath == "" {
		ruler = parsingruler.New(parsingruler.PathDelimiter(params.pathDelimiter), params.matchAnyIfNoRulesExist)
	} else {
		var err error
		ruler, err = parsingruler.Load(params.rulerPath)
		if err != nil {
			log.Fatal().Err(err).Msg("fatal error")
		}
	}

	params.ruler = ruler

	if *webMode {
		runHttpServer(params)
	} else {
		run(params)
	}

}
