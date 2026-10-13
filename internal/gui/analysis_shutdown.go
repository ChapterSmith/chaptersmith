package gui

import (
	"errors"
	"sync"

	"chaptersmith/internal/store"
)

// NewAnalysisServices owns one media registry and one process scratch owner.
// The caller owns DB and closes it after this coordinator has joined both services.
func NewAnalysisServices(db *store.DB, cacheDir, projectDir string) (*BookService, *MergerService, error) {
	b, err := NewBookService(db, cacheDir, projectDir)
	if err != nil {
		return nil, nil, err
	}
	m := NewMergerServiceWithAnalysis(db, b.Media, b.cacheDir)
	m.analysisOwner = b.analysisOwner
	coordinator := &analysisShutdown{book: b, merger: m}
	b.shutdown = coordinator.close
	m.shutdown = coordinator.close
	return b, m, nil
}

type analysisShutdown struct {
	once   sync.Once
	err    error
	book   *BookService
	merger *MergerService
}

func (c *analysisShutdown) close() error {
	c.once.Do(func() {
		// Both admission gates close and both contexts cancel BEFORE either join.
		c.book.mu.Lock()
		c.merger.mu.Lock()
		c.book.closed = true
		c.merger.closed = true
		c.merger.mu.Unlock()
		c.book.mu.Unlock()
		c.merger.markAnalysisClosing()
		c.book.markAnalysisClosing()
		c.book.workers.Wait()
		c.merger.workers.Wait()
		c.merger.previewWorkers.Wait()
		c.book.mu.Lock()
		err := c.book.invalidateAudioLocked()
		c.book.mu.Unlock()
		c.merger.mu.Lock()
		if c.merger.repair != nil && c.merger.repair.report != nil {
			err = errors.Join(err, releaseAnalysisMedia(c.merger.media, c.merger.repair.report))
		}
		c.merger.repair = nil
		c.merger.mu.Unlock()
		c.err = errors.Join(err, c.book.Media.Close(), c.book.analysisOwner.Close())
	})
	return c.err
}
func (s *BookService) markAnalysisClosing() {
	s.mu.Lock()
	s.closed = true
	requestID := ""
	if s.operation != nil {
		requestID = s.operation.requestID
	}
	s.mu.Unlock()
	if requestID != "" {
		_ = s.CancelOperation(requestID)
	}
}

func (s *MergerService) markAnalysisClosing() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.cfg != nil {
		s.cfg.cancel()
	}
	if s.repair != nil && s.repair.preview != nil {
		s.repair.preview.cancel()
	}
}
func (s *BookService) ServiceShutdown() error {
	if s.shutdown != nil {
		return s.shutdown()
	}
	s.shutdownOnce.Do(func() {
		s.markAnalysisClosing()
		s.workers.Wait()
		s.mu.Lock()
		err := s.invalidateAudioLocked()
		s.mu.Unlock()
		s.shutdownErr = errors.Join(err, s.Media.Close(), s.analysisOwner.Close())
	})
	return s.shutdownErr
}
func (s *MergerService) ServiceShutdown() error {
	if s.shutdown != nil {
		return s.shutdown()
	}
	s.shutdownOnce.Do(func() {
		s.markAnalysisClosing()
		s.workers.Wait()
		s.previewWorkers.Wait()
		s.mu.Lock()
		if s.repair != nil && s.repair.report != nil {
			s.shutdownErr = releaseAnalysisMedia(s.media, s.repair.report)
		}
		s.repair = nil
		s.mu.Unlock()
		if s.ownsAnalysisOwner {
			s.shutdownErr = errors.Join(s.shutdownErr, s.analysisOwner.Close())
		}
	})
	return s.shutdownErr
}
