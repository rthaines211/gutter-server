package admin

import (
	"sync"
	"time"
)

type Scheduler struct {
	scanFn   func()
	mu       sync.Mutex
	interval time.Duration
	stop     chan struct{}
	running  bool
}

func NewScheduler(scanFn func()) *Scheduler {
	return &Scheduler{scanFn: scanFn}
}

func (s *Scheduler) Start(interval string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return
	}
	s.running = true
	s.stop = make(chan struct{})

	d, err := time.ParseDuration(interval)
	if err != nil || d <= 0 {
		s.interval = 0
		return
	}
	s.interval = d
	go s.loop(d, s.stop)
}

func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	s.running = false
	close(s.stop)
}

func (s *Scheduler) UpdateInterval(interval string) {
	s.Stop()
	s.mu.Lock()
	s.running = true
	s.stop = make(chan struct{})

	d, err := time.ParseDuration(interval)
	if err != nil || d <= 0 {
		s.interval = 0
		s.mu.Unlock()
		return
	}
	s.interval = d
	go s.loop(d, s.stop)
	s.mu.Unlock()
}

func (s *Scheduler) Interval() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.interval <= 0 {
		return ""
	}
	return s.interval.String()
}

func (s *Scheduler) loop(d time.Duration, stop chan struct{}) {
	ticker := time.NewTicker(d)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.scanFn()
		case <-stop:
			return
		}
	}
}
