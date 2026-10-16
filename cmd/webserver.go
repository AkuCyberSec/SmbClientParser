package main

// Web mode: an HTTP server with a browser UI to manage rules, run the parser and review results.
//
// The code is split by topic:
//   - webserver.go:   startup, routes, security checks, JSON helpers
//   - webauth.go:     access key, sessions, failed-login lockout
//   - webrules.go:    editing, testing and saving the ruler
//   - webruns.go:     run queue, results, exports
//   - webfiles.go:    upload and download folders
//   - webactivity.go: activity events and server log shown in the browser
//
// The browser files (HTML, CSS, JS) are in the "web" folder at the root of the repository.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"smbclientparser/internal/parsingruler"
	"smbclientparser/web"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// JSON requests only carry small values (names, regexps, paths)
	maxJSONBodyBytes = 1 << 20 // 1 MiB

	// Protects against clients that open connections and never send the headers
	readHeaderTimeout = 10 * time.Second

	// Permissions of the upload and download folders when they are created
	folderPermissions = 0o750

	// Sent on every response: only load files from this server, never run inside a frame
	contentSecurityPolicy = "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
)

// webServer holds the state of the web mode.
type webServer struct {
	params       Parameters
	auth         *authManager
	activity     *activityLog
	uploads      fileFolder
	downloads    fileFolder
	loopbackBind bool // true if listening on 127.0.0.1, ::1 or localhost only

	// The live ruler, edited from the UI. Runs never use it directly: each run gets a frozen copy.
	rulerMu      sync.Mutex
	ruler        *parsingruler.ParsingRuler
	rulerPath    string // absolute path of the ruler file, empty if not defined yet
	rulerDirty   bool   // true if there are changes not saved to rulerPath
	rulerVersion int    // incremented on every change, used to flag outdated results

	runsMu    sync.Mutex
	runs      []*webRun // oldest first
	lastRunID int
	runQueue  chan *webRun
}

// Run in web mode by starting an HTTP server locally.
//
// This is the only mode made by AI (Claude Code).
func runHttpServer(params Parameters) {
	if params.webKey == "" {
		params.webKey = os.Getenv(webKeyEnvVariable)
	}
	keyIsGenerated := params.webKey == ""
	if keyIsGenerated {
		params.webKey = generateSecret()
	}

	if err := params.validateWeb(); err != nil {
		log.Fatal().Err(err).Msg("invalid parameter")
	}

	server, err := newWebServer(params)
	if err != nil {
		log.Fatal().Err(err).Msg("cannot start web mode")
	}
	go server.processRunQueue()

	address := net.JoinHostPort(params.localAddress, strconv.Itoa(params.localPort))
	printWebBanner(address, params.webKey, keyIsGenerated)
	if !server.loopbackBind {
		log.Warn().Str("address", address).Msg("listening on a non-loopback address over plain HTTP: the access key and the session travel unencrypted on the network")
	}

	httpServer := &http.Server{
		Addr:              address,
		Handler:           server.routes(),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatal().Err(err).Msg("web server stopped")
	}
}

func (p *Parameters) validateWeb() error {
	if p.sessionExpiration < 0 {
		return fmt.Errorf("session-expiration cannot be negative (use 0 for sessions that never expire)")
	}
	if p.keepRuns < 1 {
		return fmt.Errorf("keep-runs must be at least 1")
	}
	if p.uploadFolder == "" {
		return fmt.Errorf("upload-folder cannot be empty")
	}
	if p.downloadFolder == "" {
		return fmt.Errorf("download-folder cannot be empty")
	}
	return nil
}

func newWebServer(params Parameters) (*webServer, error) {
	uploads, err := prepareFolder(uploadsFolderID, params.uploadFolder)
	if err != nil {
		return nil, err
	}
	downloads, err := prepareFolder(downloadsFolderID, params.downloadFolder)
	if err != nil {
		return nil, err
	}

	rulerPath := ""
	if params.rulerPath != "" {
		rulerPath, err = filepath.Abs(params.rulerPath)
		if err != nil {
			return nil, err
		}
	}

	return &webServer{
		params:       params,
		auth:         newAuthManager(params.webKey, params.sessionExpiration),
		activity:     &activityLog{},
		uploads:      uploads,
		downloads:    downloads,
		loopbackBind: isLoopbackHost(params.localAddress),
		ruler:        params.ruler,
		rulerPath:    rulerPath,
		runQueue:     make(chan *webRun, runQueueCapacity),
	}, nil
}

// printWebBanner prints how to reach the UI. It uses fmt instead of the logger,
// so the information is shown even when logging is disabled.
func printWebBanner(address string, key string, keyIsGenerated bool) {
	fmt.Println()
	fmt.Println("SmbClientParser web mode")
	fmt.Printf("  URL:        http://%s\n", address)
	if keyIsGenerated {
		fmt.Printf("  Access key: %s\n", key)
		fmt.Printf("              (generated: use -web-key or %s to choose one)\n", webKeyEnvVariable)
	} else {
		fmt.Printf("  Access key: set with -web-key or %s\n", webKeyEnvVariable)
	}
	fmt.Println()
}

func (s *webServer) routes() http.Handler {
	mux := http.NewServeMux()

	// Browser files (HTML, CSS, JS): public, they contain no data
	mux.Handle("GET /", http.FileServerFS(web.Files))

	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/config", s.requireLogin(s.handleConfig))

	mux.HandleFunc("GET /api/rules", s.requireLogin(s.handleGetRules))
	mux.HandleFunc("POST /api/rules/groups/add", s.requireLogin(s.handleRulerEdit(s.addGroup)))
	mux.HandleFunc("POST /api/rules/groups/delete", s.requireLogin(s.handleRulerEdit(s.deleteGroup)))
	mux.HandleFunc("POST /api/rules/groups/enable", s.requireLogin(s.handleRulerEdit(s.enableGroup)))
	mux.HandleFunc("POST /api/rules/add", s.requireLogin(s.handleRulerEdit(s.addRule)))
	mux.HandleFunc("POST /api/rules/update", s.requireLogin(s.handleRulerEdit(s.updateRule)))
	mux.HandleFunc("POST /api/rules/enable", s.requireLogin(s.handleRulerEdit(s.enableRule)))
	mux.HandleFunc("POST /api/rules/delete", s.requireLogin(s.handleRulerEdit(s.deleteRule)))
	mux.HandleFunc("POST /api/rules/delimiter", s.requireLogin(s.handleRulerEdit(s.setPathDelimiter)))
	mux.HandleFunc("POST /api/rules/validate", s.requireLogin(s.handleValidateRegexp))
	mux.HandleFunc("POST /api/rules/test", s.requireLogin(s.handleTestName))
	mux.HandleFunc("POST /api/rules/save", s.requireLogin(s.handleSaveRuler))
	mux.HandleFunc("POST /api/rules/export", s.requireLogin(s.handleExportRuler))
	mux.HandleFunc("GET /api/rules/uploaded", s.requireLogin(s.handleListUploadedRulers))
	mux.HandleFunc("POST /api/rules/open", s.requireLogin(s.handleOpenUploadedRuler))

	mux.HandleFunc("GET /api/runs", s.requireLogin(s.handleListRuns))
	mux.HandleFunc("POST /api/runs", s.requireLogin(s.handleStartRun))
	mux.HandleFunc("POST /api/runs/check", s.requireLogin(s.handleCheckRun))
	mux.HandleFunc("POST /api/runs/cancel", s.requireLogin(s.handleCancelRun))
	mux.HandleFunc("GET /api/runs/results", s.requireLogin(s.handleRunResults))
	mux.HandleFunc("POST /api/runs/export", s.requireLogin(s.handleExportRun))
	mux.HandleFunc("POST /api/runs/delete", s.requireLogin(s.handleDeleteRuns))

	mux.HandleFunc("GET /api/files", s.requireLogin(s.handleListFiles))
	mux.HandleFunc("POST /api/files/upload", s.requireLogin(s.handleUpload))
	mux.HandleFunc("GET /api/files/download", s.requireLogin(s.handleDownload))
	mux.HandleFunc("POST /api/files/rename", s.requireLogin(s.handleRenameFile))
	mux.HandleFunc("POST /api/files/delete", s.requireLogin(s.handleDeleteFile))

	mux.HandleFunc("GET /api/activity", s.requireLogin(s.handleActivity))

	return s.withSecurityChecks(mux)
}

// withSecurityChecks adds security headers and blocks requests coming from other web sites
// (cross-site requests and DNS rebinding).
func (s *webServer) withSecurityChecks(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")

		// When listening on loopback, a different host name means a web site pointed its own name at 127.0.0.1
		if s.loopbackBind && !isLoopbackHost(r.Host) {
			writeError(w, http.StatusForbidden, "unexpected host name")
			return
		}
		if r.Method != http.MethodGet && !isSameOrigin(r) {
			writeError(w, http.StatusForbidden, "requests from other web sites are not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopbackHost accepts "host" or "host:port".
func isLoopbackHost(hostAndPort string) bool {
	host := hostAndPort
	if splitHost, _, err := net.SplitHostPort(hostAndPort); err == nil {
		host = splitHost
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isSameOrigin returns true if the request was sent by a page of this server.
// Browsers always send the Origin header on POST requests.
func isSameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	originURL, err := url.Parse(origin)
	return err == nil && originURL.Host == r.Host
}

func (s *webServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	defaultInput := s.params.inputFile
	if defaultInput != "" {
		if absolutePath, err := filepath.Abs(defaultInput); err == nil {
			defaultInput = absolutePath
		}
	}
	// Used to suggest absolute paths, e.g. where to save a new ruler file
	workingDirectory, err := os.Getwd()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"working_directory":          workingDirectory,
		"path_separator":             string(filepath.Separator),
		"upload_folder":              s.uploads.path,
		"download_folder":            s.downloads.path,
		"keep_runs":                  s.params.keepRuns,
		"session_expiration_minutes": s.params.sessionExpiration,
		"results_page_size":          resultsPageSize,
		"default_input_file":         defaultInput,
		"default_server":             s.params.serverName,
		"default_share":              s.params.shareName,
		"attributes":                 attributeLegend,
	})
}

// apiError is the body of every error response. The message is shown to the user as is.
type apiError struct {
	Error string `json:"error"`
	// Set when the request would overwrite an existing file: the UI asks for confirmation and retries
	Exists bool   `json:"exists,omitempty"`
	Path   string `json:"path,omitempty"`
	// Set when the session cookie is no longer valid (the login page says so)
	SessionExpired bool `json:"session_expired,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Error().Err(err).Msg("cannot write JSON response")
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, apiError{Error: message})
}

// writeFileExists tells the UI that a file already exists, so it can ask before overwriting.
func writeFileExists(w http.ResponseWriter, path string) {
	writeJSON(w, http.StatusConflict, apiError{
		Error:  fmt.Sprintf("%s already exists", path),
		Exists: true,
		Path:   path,
	})
}

// readJSON decodes the request body into target. On failure it writes the error response and returns false.
func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	body := http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	if err := json.NewDecoder(body).Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return false
	}
	return true
}
