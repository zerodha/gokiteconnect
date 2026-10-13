package main

import (
	"context"
	"sync"
	"time"

	"github.com/devshoe/gokiteconnect/models"
)

type searchResult struct {
	query       string
	underlyings bool
	instruments []models.Instrument
	err         error
}

type searchCoordinator struct {
	mu         sync.Mutex
	wg         sync.WaitGroup
	delay      time.Duration
	generation uint64
	timer      *time.Timer
	cancel     context.CancelFunc
	closed     bool
}

func newSearchCoordinator(delay time.Duration) *searchCoordinator {
	return &searchCoordinator{delay: delay}
}

func (s *searchCoordinator) schedule(
	parent context.Context,
	query string,
	underlyings bool,
	run func(context.Context, string, bool) ([]models.Instrument, error),
	deliver func(searchResult),
) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.generation++
	generation := s.generation
	if s.timer != nil && s.timer.Stop() {
		s.wg.Done()
	}
	if s.cancel != nil {
		s.cancel()
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	delay := s.delay
	if underlyings {
		delay = 0
	}
	s.wg.Add(1)
	s.timer = time.AfterFunc(delay, func() {
		defer s.wg.Done()
		values, err := run(ctx, query, underlyings)
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		current := generation == s.generation
		s.mu.Unlock()
		if current {
			deliver(searchResult{query: query, underlyings: underlyings, instruments: values, err: err})
		}
	})
	s.mu.Unlock()
}

func (s *searchCoordinator) cancelPending() {
	s.mu.Lock()
	s.generation++
	if s.timer != nil && s.timer.Stop() {
		s.wg.Done()
	}
	if s.timer != nil {
		s.timer = nil
	}
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.mu.Unlock()
}

func (s *searchCoordinator) shutdown() {
	s.mu.Lock()
	s.closed = true
	s.generation++
	if s.timer != nil && s.timer.Stop() {
		s.wg.Done()
	}
	s.timer = nil
	if s.cancel != nil {
		s.cancel()
	}
	s.cancel = nil
	s.mu.Unlock()
}

func (s *searchCoordinator) wait() {
	s.wg.Wait()
}
