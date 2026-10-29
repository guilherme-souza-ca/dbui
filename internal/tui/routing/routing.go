// Package routing lets the root model deliver asynchronous messages to the
// tab (session) that issued them.
package routing

// Msg is implemented by messages that belong to a specific session tab.
type Msg interface {
	SessionID() int
}

// ID is embedded in messages to implement Msg.
type ID int

func (id ID) SessionID() int { return int(id) }
