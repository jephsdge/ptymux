package server

import (
	"sort"
	"sync"
)

type Runner interface {
	Run(command string) (RunResult, error)
	Close() error
}

type Store struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

type Session struct {
	Name  string
	Panes map[string]*Pane
}

type Pane struct {
	Name string
	Tabs map[string]*Tab
}

type Tab struct {
	Name   string
	Runner Runner
}

type Snapshot struct {
	Sessions []SessionSnapshot `json:"sessions"`
}

type SessionSnapshot struct {
	Name  string         `json:"name"`
	Panes []PaneSnapshot `json:"panes"`
}

type PaneSnapshot struct {
	Name string        `json:"name"`
	Tabs []TabSnapshot `json:"tabs"`
}

type TabSnapshot struct {
	Name string `json:"name"`
}

func NewStore() *Store {
	return &Store{sessions: make(map[string]*Session)}
}

func (s *Store) GetOrCreate(sessionName, paneName, tabName string, newRunner func() Runner) *Tab {
	s.mu.Lock()
	defer s.mu.Unlock()

	session := s.sessions[sessionName]
	if session == nil {
		session = &Session{Name: sessionName, Panes: make(map[string]*Pane)}
		s.sessions[sessionName] = session
	}

	pane := session.Panes[paneName]
	if pane == nil {
		pane = &Pane{Name: paneName, Tabs: make(map[string]*Tab)}
		session.Panes[paneName] = pane
	}

	tab := pane.Tabs[tabName]
	if tab == nil {
		tab = &Tab{Name: tabName, Runner: newRunner()}
		pane.Tabs[tabName] = tab
	}
	return tab
}

func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *Store) SnapshotTarget(sessionName, paneName, tabName string) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sessionName == "" {
		return s.snapshotLocked()
	}

	session := s.sessions[sessionName]
	if session == nil {
		return Snapshot{}
	}

	ss := SessionSnapshot{Name: session.Name}
	if paneName == "" {
		paneNames := make([]string, 0, len(session.Panes))
		for name := range session.Panes {
			paneNames = append(paneNames, name)
		}
		sort.Strings(paneNames)
		for _, name := range paneNames {
			ss.Panes = append(ss.Panes, PaneSnapshot{Name: name})
		}
		return Snapshot{Sessions: []SessionSnapshot{ss}}
	}

	pane := session.Panes[paneName]
	if pane == nil {
		return Snapshot{}
	}

	ps := PaneSnapshot{Name: pane.Name}
	if tabName == "" {
		tabNames := make([]string, 0, len(pane.Tabs))
		for name := range pane.Tabs {
			tabNames = append(tabNames, name)
		}
		sort.Strings(tabNames)
		for _, name := range tabNames {
			ps.Tabs = append(ps.Tabs, TabSnapshot{Name: name})
		}
	} else if _, ok := pane.Tabs[tabName]; ok {
		ps.Tabs = append(ps.Tabs, TabSnapshot{Name: tabName})
	}
	ss.Panes = append(ss.Panes, ps)
	return Snapshot{Sessions: []SessionSnapshot{ss}}
}

func (s *Store) snapshotLocked() Snapshot {
	out := Snapshot{}
	sessionNames := make([]string, 0, len(s.sessions))
	for name := range s.sessions {
		sessionNames = append(sessionNames, name)
	}
	sort.Strings(sessionNames)

	for _, sessionName := range sessionNames {
		session := s.sessions[sessionName]
		ss := SessionSnapshot{Name: session.Name}
		paneNames := make([]string, 0, len(session.Panes))
		for name := range session.Panes {
			paneNames = append(paneNames, name)
		}
		sort.Strings(paneNames)

		for _, paneName := range paneNames {
			pane := session.Panes[paneName]
			ps := PaneSnapshot{Name: pane.Name}
			tabNames := make([]string, 0, len(pane.Tabs))
			for name := range pane.Tabs {
				tabNames = append(tabNames, name)
			}
			sort.Strings(tabNames)

			for _, tabName := range tabNames {
				ps.Tabs = append(ps.Tabs, TabSnapshot{Name: tabName})
			}
			ss.Panes = append(ss.Panes, ps)
		}
		out.Sessions = append(out.Sessions, ss)
	}
	return out
}

func (s *Store) CloseAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var firstErr error
	for _, session := range s.sessions {
		for _, pane := range session.Panes {
			for _, tab := range pane.Tabs {
				if err := tab.Runner.Close(); err != nil && firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}
