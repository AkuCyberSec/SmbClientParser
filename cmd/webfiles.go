package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	uploadsFolderID   = "uploads"
	downloadsFolderID = "downloads"

	maxFileNameLength = 255

	// While a file is being uploaded it's written as ".<name>.uploading" (hidden), then renamed
	uploadingSuffix = ".uploading"
)

// fileFolder is a folder managed from the browser (uploads or downloads).
// Only files directly inside it can be reached: no subfolders, no parent folders.
type fileFolder struct {
	id   string // used by the API: uploadsFolderID or downloadsFolderID
	path string // absolute path
}

type fileView struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// prepareFolder creates the folder if missing and returns it with its absolute path.
func prepareFolder(id string, path string) (fileFolder, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return fileFolder{}, err
	}
	if err := os.MkdirAll(absolutePath, folderPermissions); err != nil {
		return fileFolder{}, fmt.Errorf("cannot create the %s folder: %w", id, err)
	}
	return fileFolder{id: id, path: absolutePath}, nil
}

// validateFileName accepts plain file names only, so a request can never reach a file outside the folder.
func validateFileName(name string) error {
	switch {
	case strings.TrimSpace(name) == "":
		return errors.New("enter a file name")
	case len(name) > maxFileNameLength:
		return fmt.Errorf("the file name is too long (max %d characters)", maxFileNameLength)
	case name == "." || name == "..":
		return errors.New("invalid file name")
	case strings.ContainsAny(name, `/\`+"\x00"):
		return errors.New(`the file name cannot contain / or \`)
	case strings.HasPrefix(name, "."):
		return errors.New("the file name cannot start with a dot (hidden files are not listed)")
	}
	return nil
}

// filePath validates the name and returns the absolute path of the file inside the folder.
func (f fileFolder) filePath(name string) (string, error) {
	if err := validateFileName(name); err != nil {
		return "", err
	}
	return filepath.Join(f.path, name), nil
}

// list returns the regular files of the folder, newest first. Hidden files (starting with a dot) are skipped.
func (f fileFolder) list() ([]fileView, error) {
	entries, err := os.ReadDir(f.path)
	if err != nil {
		return nil, err
	}
	files := make([]fileView, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, fileView{
			Name:     entry.Name(),
			Path:     filepath.Join(f.path, entry.Name()),
			Size:     info.Size(),
			Modified: info.ModTime(),
		})
	}
	slices.SortFunc(files, func(a, b fileView) int {
		return b.Modified.Compare(a.Modified)
	})
	return files, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

func (s *webServer) folderByID(id string) (fileFolder, bool) {
	switch id {
	case uploadsFolderID:
		return s.uploads, true
	case downloadsFolderID:
		return s.downloads, true
	}
	return fileFolder{}, false
}

func (s *webServer) handleListFiles(w http.ResponseWriter, r *http.Request) {
	folder, ok := s.folderByID(r.URL.Query().Get("folder"))
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown folder")
		return
	}
	files, err := folder.list()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("cannot read %s: %v", folder.path, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"folder": folder.id,
		"path":   folder.path,
		"files":  files,
	})
}

// handleUpload stores the request body (the raw file content) in the upload folder.
// Query parameters: name, overwrite ("true" to replace an existing file).
func (s *webServer) handleUpload(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	overwrite := r.URL.Query().Get("overwrite") == "true"

	finalPath, err := s.uploads.filePath(name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if fileExists(finalPath) && !overwrite {
		writeFileExists(w, finalPath)
		return
	}

	// Write to a hidden temporary file first: an interrupted upload never leaves a half file behind
	temporaryPath := filepath.Join(s.uploads.path, "."+name+uploadingSuffix)
	file, err := os.Create(temporaryPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("cannot create %s: %v", temporaryPath, err))
		return
	}
	_, copyErr := io.Copy(file, r.Body)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(temporaryPath)
		writeError(w, http.StatusInternalServerError, "the upload was interrupted, try again")
		return
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		os.Remove(temporaryPath)
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("cannot save %s: %v", finalPath, err))
		return
	}

	s.activity.add(levelInfo, "File uploaded: %s", finalPath)
	writeJSON(w, http.StatusCreated, map[string]string{"name": name, "path": finalPath})
}

// handleDownload sends a file to the browser. Query parameters: folder, name.
func (s *webServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	folder, ok := s.folderByID(r.URL.Query().Get("folder"))
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown folder")
		return
	}
	name := r.URL.Query().Get("name")
	path, err := folder.filePath(name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("cannot open %s", path))
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, fmt.Sprintf("%s is not a file", path))
		return
	}

	// "attachment": the browser saves the file instead of opening it
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func (s *webServer) handleRenameFile(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Folder    string `json:"folder"`
		Name      string `json:"name"`
		NewName   string `json:"new_name"`
		Overwrite bool   `json:"overwrite"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	folder, ok := s.folderByID(request.Folder)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown folder")
		return
	}
	oldPath, err := folder.filePath(request.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	newPath, err := folder.filePath(request.NewName)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if fileExists(newPath) && !request.Overwrite {
		writeFileExists(w, newPath)
		return
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("cannot rename %s: %v", oldPath, err))
		return
	}
	if folder.id == downloadsFolderID {
		// The renamed file (and the one it replaced) no longer belong to a run
		s.forgetExports(request.Name, request.NewName)
	}
	s.activity.add(levelInfo, "File renamed: %s -> %s", oldPath, newPath)
	writeJSON(w, http.StatusOK, map[string]string{"name": request.NewName, "path": newPath})
}

func (s *webServer) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Folder string `json:"folder"`
		Name   string `json:"name"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	folder, ok := s.folderByID(request.Folder)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown folder")
		return
	}
	path, err := folder.filePath(request.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.Remove(path); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("cannot delete %s: %v", path, err))
		return
	}
	if folder.id == downloadsFolderID {
		s.forgetExports(request.Name)
	}
	s.activity.add(levelWarning, "File deleted: %s", path)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
