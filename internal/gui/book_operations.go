package gui

import (
	"context"
	"fmt"

	"chaptersmith/internal/book"
)

type bookOperation struct {
	requestID string
	ctx       context.Context
	cancel    context.CancelFunc
	// A committed result has crossed the cancellation boundary under the session lock.
	committed bool
}

// beginOperation reserves a slot and snapshot before any expensive work starts.
// emitMu preserves event ordering without holding the session mutex in callbacks.
func (s *BookService) beginOperation(requestID, kind string, expectedRevision int64) (context.Context, book.BookProject, error) {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, book.BookProject{}, fmt.Errorf("book service is closed")
	}
	if s.operation != nil {
		s.mu.Unlock()
		return nil, book.BookProject{}, fmt.Errorf("operation already running")
	}
	if requestID == "" {
		s.mu.Unlock()
		return nil, book.BookProject{}, fmt.Errorf("requestId is required")
	}
	if _, exists := s.operations[requestID]; exists {
		s.mu.Unlock()
		return nil, book.BookProject{}, fmt.Errorf("requestId already used")
	}
	switch kind {
	case "load", "preview", "suggest", "export", "analyze":
	default:
		s.mu.Unlock()
		return nil, book.BookProject{}, fmt.Errorf("unknown operation kind %q", kind)
	}
	if kind != "load" {
		if err := s.checkRevisionLocked(expectedRevision); err != nil {
			s.mu.Unlock()
			return nil, book.BookProject{}, err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.operation = &bookOperation{requestID: requestID, ctx: ctx, cancel: cancel}
	state := OperationStatus{RequestID: requestID, Kind: kind, Status: "running", Paths: []string{}}
	if kind != "load" {
		state.ProjectID = s.project.ID
		state.Revision = s.revision
	}
	s.operations[requestID] = state
	snapshot := book.Clone(s.project)
	s.workers.Add(1)
	s.mu.Unlock()
	s.deps.Emit("book-operation", state)
	return ctx, snapshot, nil
}

func (s *BookService) finishOperation(requestID, status, message string) {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	s.mu.Lock()
	state, exists := s.operations[requestID]
	if !exists || terminalStatus(state.Status) {
		s.mu.Unlock()
		return
	}
	if s.operation == nil || s.operation.requestID != requestID {
		s.mu.Unlock()
		return
	}
	s.operation.cancel()
	state.Status = status
	state.Message = message
	if status == "failed" || status == "cancelled" {
		state.Error = message
	}
	s.operations[requestID] = state
	s.operation = nil
	s.mu.Unlock()
	defer s.workers.Done()
	s.deps.Emit("book-operation", cloneOperationStatus(state))
}

func (s *BookService) updateOperation(requestID string, current, total int, message string) {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	s.mu.Lock()
	state, exists := s.operations[requestID]
	if !exists || terminalStatus(state.Status) {
		s.mu.Unlock()
		return
	}
	state.Current = current
	state.Total = total
	state.Message = message
	s.operations[requestID] = state
	s.mu.Unlock()
	s.deps.Emit("book-operation", cloneOperationStatus(state))
}

// CancelOperation requests cleanup but retains the slot until work returns.
func (s *BookService) CancelOperation(requestID string) error {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	s.mu.Lock()
	state, exists := s.operations[requestID]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("unknown requestId %q", requestID)
	}
	if terminalStatus(state.Status) || state.Status == "cancelling" {
		s.mu.Unlock()
		return nil
	}
	if s.operation == nil || s.operation.requestID != requestID || s.operation.committed {
		s.mu.Unlock()
		return nil
	}
	s.operation.cancel()
	state.Status = "cancelling"
	state.Message = "Cancelling"
	s.operations[requestID] = state
	s.mu.Unlock()
	s.deps.Emit("book-operation", cloneOperationStatus(state))
	return nil
}

func (s *BookService) GetOperation(requestID string) (OperationStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, exists := s.operations[requestID]
	if !exists {
		return OperationStatus{}, fmt.Errorf("unknown requestId %q", requestID)
	}
	return cloneOperationStatus(state), nil
}

func terminalStatus(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled"
}
func cloneOperationStatus(state OperationStatus) OperationStatus {
	if state.Paths != nil {
		state.Paths = append([]string{}, state.Paths...)
	}
	return state
}
