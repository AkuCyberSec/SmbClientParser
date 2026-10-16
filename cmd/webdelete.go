package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"runtime/debug"
	"slices"
)

// Deleting runs: from memory (the run leaves Queue and history and its results are freed)
// and/or from disk (the files it exported to the download folder).
//
// Only exports made by this server are tracked: files renamed, deleted or overwritten
// in another way, or exported before a restart, don't belong to any run.

// isFinished reports whether the run will never change again, so it can be deleted.
func (r *webRun) isFinished() bool {
	return r.status == runDone || r.status == runFailed || r.status == runCancelled
}

// trackExportLocked records that a run exported a file to the download folder.
// A file overwritten by another export belongs to the run that wrote it last.
// Must be called with runsMu locked.
func (s *webServer) trackExportLocked(owner *webRun, name string) {
	s.forgetExportsLocked(name)
	owner.exports = append(owner.exports, name)
}

// forgetExportsLocked stops tracking files of the download folder. Must be called with runsMu locked.
func (s *webServer) forgetExportsLocked(names ...string) {
	for _, r := range s.runs {
		r.exports = slices.DeleteFunc(r.exports, func(name string) bool { return slices.Contains(names, name) })
	}
}

// forgetExports is used when a file of the download folder is renamed, deleted or overwritten by something else.
func (s *webServer) forgetExports(names ...string) {
	s.runsMu.Lock()
	defer s.runsMu.Unlock()
	s.forgetExportsLocked(names...)
}

// handleDeleteRuns deletes finished runs from memory and/or their exported files from the download folder.
// Everything is checked before anything is deleted. A run whose files can't be deleted stays in memory,
// so the user still sees it and can try again.
func (s *webServer) handleDeleteRuns(w http.ResponseWriter, r *http.Request) {
	var request struct {
		IDs         []int    `json:"ids"`
		FromMemory  bool     `json:"from_memory"`
		ExportFiles []string `json:"export_files"` // names in the download folder, exported by these runs
	}
	if !readJSON(w, r, &request) {
		return
	}
	if len(request.IDs) == 0 || (!request.FromMemory && len(request.ExportFiles) == 0) {
		writeError(w, http.StatusBadRequest, "nothing to delete")
		return
	}
	owners, err := s.checkRunsToDelete(request.IDs, request.ExportFiles)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	deletedFiles := []string{} // paths, for the user
	goneFiles := []string{}    // names no longer on disk, to stop tracking
	failures := []string{}
	keptRuns := map[int]bool{}
	for _, name := range request.ExportFiles {
		// Tracked names were validated when exported: filePath can't fail here
		path, _ := s.downloads.filePath(name)
		err := os.Remove(path)
		switch {
		case err == nil:
			deletedFiles = append(deletedFiles, path)
			goneFiles = append(goneFiles, name)
			s.activity.add(levelWarning, "Export of run #%d deleted: %s", owners[name], path)
		case errors.Is(err, fs.ErrNotExist): // already deleted by hand
			goneFiles = append(goneFiles, name)
		default:
			failures = append(failures, fmt.Sprintf("cannot delete %s: %v", path, err))
			keptRuns[owners[name]] = true
		}
	}

	s.runsMu.Lock()
	s.forgetExportsLocked(goneFiles...)
	deletedRuns := []int{}
	if request.FromMemory {
		// DeleteFunc also clears the removed pointers, so the garbage collector can free the results
		s.runs = slices.DeleteFunc(s.runs, func(candidate *webRun) bool {
			remove := slices.Contains(request.IDs, candidate.id) && !keptRuns[candidate.id]
			if remove {
				deletedRuns = append(deletedRuns, candidate.id)
			}
			return remove
		})
	}
	s.runsMu.Unlock()

	for _, id := range deletedRuns {
		s.activity.add(levelWarning, "Run #%d deleted from memory", id)
	}
	if request.FromMemory {
		for _, id := range request.IDs {
			if keptRuns[id] {
				failures = append(failures, fmt.Sprintf("run #%d was kept in memory: delete its files again, or choose Memory only", id))
			}
		}
	}
	for _, failure := range failures {
		s.activity.add(levelError, "%s", failure)
	}
	if len(deletedRuns) > 0 {
		// Otherwise Go hands the freed memory back to the operating system only after several minutes
		debug.FreeOSMemory()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"deleted_runs":  deletedRuns,
		"deleted_files": deletedFiles,
		"errors":        failures,
	})
}

// checkRunsToDelete makes sure every run exists and is finished, and that every file is an export of one of them.
// It returns the run that owns each tracked file.
func (s *webServer) checkRunsToDelete(ids []int, exportFiles []string) (map[string]int, error) {
	s.runsMu.Lock()
	defer s.runsMu.Unlock()

	owners := map[string]int{}
	for _, id := range ids {
		target := s.findRunLocked(id)
		if target == nil {
			return nil, fmt.Errorf("run #%d doesn't exist anymore", id)
		}
		if !target.isFinished() {
			return nil, fmt.Errorf("run #%d hasn't finished: cancel it, or wait for it to stop", id)
		}
		for _, name := range target.exports {
			owners[name] = id
		}
	}
	for _, name := range exportFiles {
		if _, ok := owners[name]; !ok {
			return nil, fmt.Errorf("%s is not an export of the chosen runs", name)
		}
	}
	return owners, nil
}
