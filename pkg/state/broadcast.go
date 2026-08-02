package state

import (
	"github.com/sirupsen/logrus"
)

// Subscribe returns a channel that receives state events.
func (m *Manager) Subscribe() chan StateEvent {
	ch := make(chan StateEvent, 64)
	m.subMu.Lock()
	m.subscribers = append(m.subscribers, ch)
	m.subMu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel.
func (m *Manager) Unsubscribe(ch chan StateEvent) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for i, sub := range m.subscribers {
		if sub == ch {
			m.subscribers = append(m.subscribers[:i], m.subscribers[i+1:]...)
			close(ch)
			return
		}
	}
}

// broadcast sends an event to all subscribers.
func (m *Manager) broadcast(evt StateEvent) {
	m.subMu.RLock()
	defer m.subMu.RUnlock()
	for _, ch := range m.subscribers {
		select {
		case ch <- evt:
		default:
			// subscriber too slow, drop event
		}
	}
}

// Notice carries a human-readable backend message to the frontend so silent
// background failures (AI naming, session rename, etc.) are visible in the UI
// instead of only in server logs.
type Notice struct {
	Severity string `json:"severity"` // "error", "warn", or "info"
	Source   string `json:"source"`   // short origin tag, e.g. "ai-naming"
	Message  string `json:"message"`  // human-readable detail
}

// notice broadcasts a Notice to the frontend and mirrors it to the server log.
func (m *Manager) notice(severity, source, session, message string) {
	switch severity {
	case "error":
		logrus.WithFields(logrus.Fields{"source": source, "session": session}).Error(message)
	case "warn":
		logrus.WithFields(logrus.Fields{"source": source, "session": session}).Warn(message)
	default:
		logrus.WithFields(logrus.Fields{"source": source, "session": session}).Info(message)
	}
	m.broadcast(StateEvent{
		Type:    "notice",
		Session: session,
		Data:    Notice{Severity: severity, Source: source, Message: message},
	})
}
