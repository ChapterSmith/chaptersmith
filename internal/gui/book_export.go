package gui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"chaptersmith/internal/book"
)

type ExportStarted struct {
	RequestID string `json:"requestId"`
	TaskID    int64  `json:"taskId"`
	ProjectID string `json:"projectId"`
	Revision  int64  `json:"revision"`
}
type bookLog struct {
	RequestID string `json:"requestId"`
	TaskID    int64  `json:"taskId"`
	Line      string `json:"line"`
}

// suggestedExportName proposes "<title>-<author>" from the book's tags, using
// whichever of the two is filled in and "book" when neither is. Characters a
// file name cannot hold become spaces.
func suggestedExportName(title, author string) string {
	clean := func(text string) string {
		text = strings.Map(func(r rune) rune {
			if r < 32 || r == 127 || strings.ContainsRune(`/\:*?"<>|`, r) {
				return ' '
			}
			return r
		}, text)
		return strings.Join(strings.Fields(text), " ")
	}
	var parts []string
	for _, part := range []string{clean(title), clean(author)} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	name := strings.TrimRight(strings.Join(parts, "-"), ". ")
	if runes := []rune(name); len(runes) > 150 {
		name = strings.TrimRight(string(runes[:150]), ". ")
	}
	if !validExportComponent(name) {
		return "book"
	}
	return name
}

func validExportComponent(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 200 || strings.TrimSpace(name) != name || strings.HasSuffix(name, ".") || strings.ContainsAny(name, `/\<>:"|?*`) {
		return false
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return false
		}
	}
	stem := strings.ToUpper(strings.Split(name, ".")[0])
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return false
	}
	if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9' {
		return false
	}
	return true
}
func (s *BookService) SelectExportTarget(expectedRevision int64, directoryName string) (string, error) {
	s.mu.Lock()
	err := s.checkRevisionLocked(expectedRevision)
	if err == nil {
		err = s.checkSwitchLocked()
	}
	settings := s.project.Export
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	kind := "export:" + settings.Format
	if settings.OutputMode != "book" {
		if !validExportComponent(directoryName) {
			return "", fmt.Errorf("export directory name must be one safe component")
		}
		kind = "folder"
	}
	path, err := s.deps.Pick(kind)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.checkRevisionLocked(expectedRevision); err != nil {
		return "", err
	}
	if err = s.checkSwitchLocked(); err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	if settings.OutputMode == "book" {
		path = strings.TrimSuffix(path, filepath.Ext(path)) + "." + settings.Format
	} else {
		path = filepath.Join(path, directoryName)
	}
	return validateExportTarget(path, settings)
}

// Renderer repeats the filesystem preflight immediately before staging/publication.
func validateExportTarget(path string, settings book.ExportSettings) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("export destination is empty")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if settings.OutputMode != "book" && !validExportComponent(filepath.Base(path)) {
		return "", fmt.Errorf("export directory name must be one safe component")
	}
	if settings.OutputMode == "book" && !strings.EqualFold(filepath.Ext(path), "."+settings.Format) {
		return "", fmt.Errorf("export destination must end in .%s", settings.Format)
	}
	if _, err = os.Lstat(path); err == nil {
		return "", fmt.Errorf("export destination already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return "", fmt.Errorf("export parent must exist: %w", err)
	}
	if !parent.IsDir() {
		return "", fmt.Errorf("export parent must be a directory")
	}
	return path, nil
}
func (s *BookService) StartExport(requestID string, expectedRevision int64, outputPath string) (started ExportStarted, err error) {
	ctx, snapshot, err := s.beginOperation(requestID, "export", expectedRevision)
	if err != nil {
		return started, err
	}
	handedOff := false
	defer func() {
		if !handedOff {
			status := "failed"
			if errors.Is(err, context.Canceled) {
				status = "cancelled"
			}
			s.finishOperation(requestID, status, err.Error())
		}
	}()
	if err = book.Validate(snapshot); err != nil {
		return started, err
	}
	if err = book.CheckSources(snapshot); err != nil {
		return started, err
	}
	if outputPath, err = validateExportTarget(outputPath, snapshot.Export); err != nil {
		return started, err
	}
	if err = ctx.Err(); err != nil {
		return started, err
	}
	inputDir := ""
	if len(snapshot.Sources) > 0 {
		inputDir = filepath.Dir(snapshot.Sources[0].Path)
	}
	taskID, err := s.store.InsertProjectTask(inputDir, outputPath, snapshot)
	if err != nil {
		return started, fmt.Errorf("creating export history: %w", err)
	}
	s.mu.Lock()
	state := s.operations[requestID]
	state.TaskID = taskID
	s.operations[requestID] = state
	s.mu.Unlock()
	started = ExportStarted{RequestID: requestID, TaskID: taskID, ProjectID: state.ProjectID, Revision: state.Revision}
	handedOff = true
	go s.runExport(ctx, started, snapshot, outputPath)
	return started, nil
}
func (s *BookService) runExport(ctx context.Context, started ExportStarted, snapshot book.BookProject, outputPath string) {
	writer := &exportLogWriter{service: s, requestID: started.RequestID, taskID: started.TaskID}
	logger := log.New(writer, "", 0)
	logger.Printf("Exporting project %s snapshot revision %d", started.ProjectID, started.Revision)
	result, err := s.deps.Export(ctx, snapshot, outputPath, logger, func(current, total int, message string) {
		s.updateOperation(started.RequestID, current, total, message)
	})
	status, message, historyMessage := "succeeded", "Export complete", ""
	// A nil renderer error means atomic publication succeeded, even with a late cancel.
	if err != nil {
		status = "failed"
		message = err.Error()
		historyMessage = message
		if errors.Is(err, context.Canceled) {
			status = "cancelled"
			message = "Export cancelled"
			historyMessage = "cancelled"
		}
	}
	logger.Print(message)
	historyErr := writer.err()
	if e := s.store.CompleteTask(started.TaskID, err == nil, historyMessage); e != nil {
		historyErr = errors.Join(historyErr, fmt.Errorf("completing export history: %w", e))
	}
	s.mu.Lock()
	state := s.operations[started.RequestID]
	if err == nil {
		state.Paths = append([]string{}, result.Paths...)
		state.NormalizationReport = result.NormalizationReport
		s.operation.committed = true
	}
	if historyErr != nil {
		state.Error = historyErr.Error()
		message += "; " + state.Error
	}
	s.operations[started.RequestID] = state
	s.mu.Unlock()
	s.finishOperation(started.RequestID, status, message)
}

type exportLogWriter struct {
	service    *BookService
	requestID  string
	taskID     int64
	mu         sync.Mutex
	historyErr error
}

func (w *exportLogWriter) Write(data []byte) (int, error) {
	line := strings.TrimRight(string(data), "\r\n")
	if err := w.service.store.AppendLog(w.taskID, line); err != nil {
		w.mu.Lock()
		if w.historyErr == nil {
			w.historyErr = fmt.Errorf("writing export history log: %w", err)
		}
		w.mu.Unlock()
		line += "; history log error: " + err.Error()
	}
	w.service.deps.Emit("book-log", bookLog{RequestID: w.requestID, TaskID: w.taskID, Line: line})
	return len(data), nil
}
func (w *exportLogWriter) err() error { w.mu.Lock(); defer w.mu.Unlock(); return w.historyErr }
