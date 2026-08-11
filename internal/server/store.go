package server

import (
	"io"
	"sort"
	"sync"
	"time"
)

type Runner interface {
	Run(command string) (RunResult, error)
	RunIdle(command string) (RunResult, error)
	Send(input string) error
	SendWait(input string, quietFor time.Duration) (RunResult, error)
	SendFollow(input string, output io.Writer, done <-chan struct{}) error
	Text(input string) error
	Command(keys string) error
	CommandWait(keys string, quietFor time.Duration) (RunResult, error)
	CommandFollow(keys string, output io.Writer, done <-chan struct{}) error
	Keys(keys string) error
	KeysWait(keys string, quietFor time.Duration) (RunResult, error)
	KeysFollow(keys string, output io.Writer, done <-chan struct{}) error
	Follow(output io.Writer, done <-chan struct{}) error
	CtrlCFollow(output io.Writer, done <-chan struct{}) error
	Read(count int) (RunResult, error)
	Close() error
}

type runnerWithDone interface {
	Done() <-chan struct{}
}

type Store struct {
	mu       sync.Mutex
	sessions map[string]*Session
	starting map[targetKey]*startingTarget
}

type targetKey struct {
	session string
	pane    string
	tab     string
}

type startingTarget struct {
	done     chan struct{}
	canceled bool
	startErr error
	closeErr error
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
	Name       string
	Runner     Runner
	LastUsedAt time.Time
	active     int
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
	return &Store{
		sessions: make(map[string]*Session),
		starting: make(map[targetKey]*startingTarget),
	}
}

func (s *Store) GetOrCreate(sessionName, paneName, tabName string, newRunner func() Runner) *Tab {
	tab, _ := s.getOrCreate(sessionName, paneName, tabName, func() (Runner, error) {
		return newRunner(), nil
	}, false)
	return tab
}

func (s *Store) BeginUse(sessionName, paneName, tabName string, newRunner func() Runner) (*Tab, func()) {
	tab, done, _ := s.BeginUseWithError(sessionName, paneName, tabName, func() (Runner, error) {
		return newRunner(), nil
	})
	return tab, done
}

func (s *Store) BeginUseWithError(sessionName, paneName, tabName string, newRunner func() (Runner, error)) (*Tab, func(), error) {
	tab, err := s.getOrCreate(sessionName, paneName, tabName, newRunner, true)
	if tab == nil {
		return nil, func() {}, err
	}
	return tab, s.finishUse(tab), nil
}

func (s *Store) BeginExistingUse(sessionName, paneName, tabName string) (*Tab, func()) {
	s.mu.Lock()
	tab := s.findTabLocked(sessionName, paneName, tabName)
	if tab == nil {
		s.mu.Unlock()
		return nil, func() {}
	}
	tab.active++
	tab.LastUsedAt = time.Now()
	s.mu.Unlock()

	return tab, s.finishUse(tab)
}

func (s *Store) finishUse(tab *Tab) func() {
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if tab.active > 0 {
			tab.active--
		}
		tab.LastUsedAt = time.Now()
	}
}

func (s *Store) TouchTarget(sessionName, paneName, tabName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tab := s.findTabLocked(sessionName, paneName, tabName)
	if tab != nil {
		tab.LastUsedAt = time.Now()
	}
}

func (s *Store) Empty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions) == 0
}

func (s *Store) targetExists(sessionName, paneName, tabName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findTabLocked(sessionName, paneName, tabName) != nil
}

func (s *Store) targetCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, session := range s.sessions {
		for _, pane := range session.Panes {
			count += len(pane.Tabs)
		}
	}
	return count
}

func (s *Store) CloseIdleTargets(now time.Time, idleFor time.Duration) error {
	if idleFor <= 0 {
		return nil
	}

	s.mu.Lock()
	var tabs []*Tab
	for sessionName, session := range s.sessions {
		for paneName, pane := range session.Panes {
			for tabName, tab := range pane.Tabs {
				if tab.active > 0 || tab.LastUsedAt.IsZero() || now.Sub(tab.LastUsedAt) < idleFor {
					continue
				}
				tabs = append(tabs, tab)
				s.removeTabLocked(sessionName, paneName, tabName)
			}
		}
	}
	s.mu.Unlock()

	return closeTabs(tabs)
}

func (s *Store) getOrCreate(sessionName, paneName, tabName string, newRunner func() (Runner, error), active bool) (*Tab, error) {
	if err := ValidateCompleteTarget(sessionName, paneName, tabName); err != nil {
		return nil, err
	}
	key := targetKey{session: sessionName, pane: paneName, tab: tabName}
	for {
		s.mu.Lock()
		if tab := s.findTabLocked(sessionName, paneName, tabName); tab != nil {
			if active {
				tab.active++
			}
			tab.LastUsedAt = time.Now()
			s.mu.Unlock()
			return tab, nil
		}
		if starting := s.starting[key]; starting != nil {
			done := starting.done
			s.mu.Unlock()
			<-done
			if starting.canceled {
				return nil, nil
			}
			if starting.startErr != nil {
				return nil, starting.startErr
			}
			continue
		}
		starting := &startingTarget{done: make(chan struct{})}
		s.starting[key] = starting
		s.mu.Unlock()

		runner, startErr := newRunner()

		s.mu.Lock()
		if starting.canceled {
			s.mu.Unlock()
			var closeErr error
			if runner != nil {
				closeErr = runner.Close()
			}
			s.mu.Lock()
			starting.closeErr = closeErr
			delete(s.starting, key)
			close(starting.done)
			s.mu.Unlock()
			return nil, nil
		}
		if startErr != nil {
			starting.startErr = startErr
			delete(s.starting, key)
			close(starting.done)
			s.mu.Unlock()
			return nil, startErr
		}
		tab := &Tab{Name: tabName, Runner: runner, LastUsedAt: time.Now()}
		s.installTabLocked(sessionName, paneName, tabName, tab)
		if active {
			tab.active++
		}
		delete(s.starting, key)
		close(starting.done)
		s.mu.Unlock()

		if runner, ok := runner.(runnerWithDone); ok {
			go s.removeWhenDone(sessionName, paneName, tabName, tab, runner.Done())
		}
		return tab, nil
	}
}

func (s *Store) installTabLocked(sessionName, paneName, tabName string, tab *Tab) {
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
	pane.Tabs[tabName] = tab
}

func (s *Store) findTabLocked(sessionName, paneName, tabName string) *Tab {
	session := s.sessions[sessionName]
	if session == nil {
		return nil
	}
	pane := session.Panes[paneName]
	if pane == nil {
		return nil
	}
	return pane.Tabs[tabName]
}

func (s *Store) removeWhenDone(sessionName, paneName, tabName string, tab *Tab, done <-chan struct{}) {
	<-done

	s.mu.Lock()
	if s.findTabLocked(sessionName, paneName, tabName) != tab {
		s.mu.Unlock()
		return
	}
	s.removeTabLocked(sessionName, paneName, tabName)
	s.mu.Unlock()

	_ = tab.Runner.Close()
}

func (s *Store) removeTabLocked(sessionName, paneName, tabName string) {
	session := s.sessions[sessionName]
	if session == nil {
		return
	}
	pane := session.Panes[paneName]
	if pane == nil {
		return
	}
	delete(pane.Tabs, tabName)
	if len(pane.Tabs) == 0 {
		delete(session.Panes, paneName)
	}
	if len(session.Panes) == 0 {
		delete(s.sessions, sessionName)
	}
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
	var tabs []*Tab
	for _, session := range s.sessions {
		for _, pane := range session.Panes {
			for _, tab := range pane.Tabs {
				tabs = append(tabs, tab)
			}
		}
	}
	s.sessions = make(map[string]*Session)
	starting := make([]*startingTarget, 0, len(s.starting))
	for _, target := range s.starting {
		target.canceled = true
		starting = append(starting, target)
	}
	s.mu.Unlock()

	firstErr := closeTabs(tabs)
	for _, target := range starting {
		<-target.done
		if target.closeErr != nil && firstErr == nil {
			firstErr = target.closeErr
		}
	}
	return firstErr
}

func (s *Store) CloseTarget(sessionName, paneName, tabName string) error {
	_, err := s.closeTarget(sessionName, paneName, tabName, true)
	return err
}

func (s *Store) CloseExistingTarget(sessionName, paneName, tabName string) (bool, error) {
	return s.closeTarget(sessionName, paneName, tabName, false)
}

func (s *Store) closeTarget(sessionName, paneName, tabName string, cancelStarting bool) (bool, error) {
	key := targetKey{session: sessionName, pane: paneName, tab: tabName}
	s.mu.Lock()
	tab := s.findTabLocked(sessionName, paneName, tabName)
	if tab != nil {
		s.removeTabLocked(sessionName, paneName, tabName)
	}
	starting := s.starting[key]
	if starting != nil && cancelStarting {
		starting.canceled = true
	}
	s.mu.Unlock()

	if tab == nil && (!cancelStarting || starting == nil) {
		return false, nil
	}
	var firstErr error
	if tab != nil {
		firstErr = tab.Runner.Close()
	}
	if starting != nil && cancelStarting {
		<-starting.done
		if starting.closeErr != nil && firstErr == nil {
			firstErr = starting.closeErr
		}
	}
	return true, firstErr
}

func closeTabs(tabs []*Tab) error {
	var firstErr error
	for _, tab := range tabs {
		if err := tab.Runner.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
