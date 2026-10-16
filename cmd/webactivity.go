package main

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	maxActivityEvents = 1000
	maxServerLogLines = 1000
)

type activityLevel string

const (
	levelInfo    activityLevel = "info"
	levelSuccess activityLevel = "success"
	levelWarning activityLevel = "warning"
	levelError   activityLevel = "error"
)

// Level used when an activity event is also written to the server log
var activityLogLevels = map[activityLevel]zerolog.Level{
	levelInfo:    zerolog.InfoLevel,
	levelSuccess: zerolog.InfoLevel,
	levelWarning: zerolog.WarnLevel,
	levelError:   zerolog.ErrorLevel,
}

// activityEvent is something the server did, shown in the Activity tab
// (so the user can review what happened while the browser was closed).
type activityEvent struct {
	Time    time.Time     `json:"time"`
	Level   activityLevel `json:"level"`
	Message string        `json:"message"`
}

type activityLog struct {
	mu     sync.Mutex
	events []activityEvent // oldest first
}

// add records an event and writes it to the server log too.
func (a *activityLog) add(level activityLevel, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	log.WithLevel(activityLogLevels[level]).Msg("activity: " + message)

	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, activityEvent{Time: time.Now(), Level: level, Message: message})
	if len(a.events) > maxActivityEvents {
		a.events = a.events[len(a.events)-maxActivityEvents:]
	}
}

// newestFirst returns a copy of the events, newest first.
func (a *activityLog) newestFirst() []activityEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	events := slices.Clone(a.events)
	slices.Reverse(events)
	return events
}

// logBuffer keeps the latest lines written by the logger (see initializeLogger in main.go).
type logBuffer struct {
	mu    sync.Mutex
	lines []string // oldest first
}

var serverLog = &logBuffer{}

func (b *logBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := strings.TrimRight(string(data), "\n")
	b.lines = append(b.lines, strings.Split(text, "\n")...)
	if len(b.lines) > maxServerLogLines {
		b.lines = b.lines[len(b.lines)-maxServerLogLines:]
	}
	return len(data), nil
}

func (b *logBuffer) copyLines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.lines)
}

func (s *webServer) handleActivity(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"events":    s.activity.newestFirst(),
		"log_lines": serverLog.copyLines(),
	})
}
