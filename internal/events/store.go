package events

import "sync"

// subscriberBufferSize is large enough that a slow stderr progress printer
// (F72) does not silently lose most lifecycle events under default{} sends.
const subscriberBufferSize = 512
const maxHistorySize = 4096

type Store struct {
	mu          sync.RWMutex
	history     []Envelope
	subscribers map[chan Envelope]struct{}
}

func NewStore() *Store {
	return &Store{subscribers: make(map[chan Envelope]struct{})}
}

func (s *Store) Append(event Envelope) {
	s.mu.Lock()
	s.history = append(s.history, event)
	if len(s.history) > maxHistorySize {
		overflow := len(s.history) - maxHistorySize
		s.history = append([]Envelope(nil), s.history[overflow:]...)
	}
	subscribers := make([]chan Envelope, 0, len(s.subscribers))
	for subscriber := range s.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	s.mu.Unlock()

	for _, subscriber := range subscribers {
		deliverToSubscriber(subscriber, event)
	}
}

// deliverToSubscriber prefers newest events: if the buffer is full, drop one
// oldest queued event then enqueue (never silently drop the newest).
func deliverToSubscriber(subscriber chan Envelope, event Envelope) {
	select {
	case subscriber <- event:
		return
	default:
	}
	select {
	case <-subscriber:
	default:
	}
	select {
	case subscriber <- event:
	default:
	}
}

func (s *Store) History() []Envelope {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cloned := make([]Envelope, len(s.history))
	copy(cloned, s.history)
	return cloned
}

func (s *Store) Subscribe() (<-chan Envelope, func()) {
	channel := make(chan Envelope, subscriberBufferSize)
	s.mu.Lock()
	s.subscribers[channel] = struct{}{}
	s.mu.Unlock()

	unsubscribe := func() {
		s.mu.Lock()
		if _, ok := s.subscribers[channel]; ok {
			delete(s.subscribers, channel)
			close(channel)
		}
		s.mu.Unlock()
	}

	return channel, unsubscribe
}
