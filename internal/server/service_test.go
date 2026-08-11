package server

import (
	"errors"
	"testing"
	"time"
)

type cancellableFakeRunner struct {
	fakeRunner
	started     chan struct{}
	waitForDone bool
	result      RunResult
	err         error
}

func (r *cancellableFakeRunner) RunWithDone(_ string, done <-chan struct{}) (RunResult, error) {
	close(r.started)
	if r.waitForDone {
		<-done
	}
	return r.result, r.err
}

func TestServiceRunPropagatesClientCancellation(t *testing.T) {
	runner := &cancellableFakeRunner{
		started:     make(chan struct{}),
		waitForDone: true,
		err:         ErrOperationCanceled,
	}
	service := newServiceWithRunner(func() Runner { return runner })
	clientDone := make(chan struct{})
	response := make(chan Response, 1)
	go func() {
		response <- service.HandleWithDone(Request{
			Action:  "run",
			Session: "work",
			Pane:    "default",
			Tab:     "default",
			Command: "sleep 10",
		}, true, clientDone)
	}()
	<-runner.started
	close(clientDone)

	select {
	case resp := <-response:
		if resp.Error != ErrOperationCanceled.Error() {
			t.Fatalf("Error = %q, want %q", resp.Error, ErrOperationCanceled)
		}
	case <-time.After(time.Second):
		t.Fatal("HandleWithDone did not return after client cancellation")
	}
}

func TestServiceRunPropagatesTruncatedResult(t *testing.T) {
	runner := &cancellableFakeRunner{
		started: make(chan struct{}),
		result:  RunResult{Output: "partial", ExitCode: 7, Truncated: true},
	}
	service := newServiceWithRunner(func() Runner { return runner })
	clientDone := make(chan struct{})

	resp := service.HandleWithDone(Request{
		Action:  "run",
		Session: "work",
		Pane:    "default",
		Tab:     "default",
		Command: "command",
	}, true, clientDone)
	if resp.Output != "partial" || resp.ExitCode != 7 || !resp.Truncated {
		t.Fatalf("response = %+v, want truncated run result", resp)
	}
	if resp.Error != "" {
		t.Fatalf("Error = %q, want empty", resp.Error)
	}
}

func TestServiceCreatePropagatesRunnerStartupError(t *testing.T) {
	startErr := errors.New("shell startup failed")
	service := newServiceWithRunnerFactory(func() (Runner, error) {
		return nil, startErr
	})

	resp := service.Create(Request{Session: "work", Pane: "default", Tab: "default"})
	if resp.Error != startErr.Error() {
		t.Fatalf("Error = %q, want %q", resp.Error, startErr)
	}
	if service.TargetCount() != 0 {
		t.Fatalf("TargetCount = %d, want 0", service.TargetCount())
	}
}

func TestServiceRequireExistingDoesNotCreateTarget(t *testing.T) {
	service := newServiceWithRunner(func() Runner {
		t.Fatal("runner factory called for require-existing request")
		return &fakeRunner{}
	})

	resp := service.Handle(Request{
		Action:  "send",
		Session: "work",
		Pane:    "default",
		Tab:     "default",
		Command: "pwd",
	}, false)

	if resp.Error != ErrTargetNotFound.Error() {
		t.Fatalf("Error = %q, want %q", resp.Error, ErrTargetNotFound)
	}
	if got := service.Store().Snapshot(); len(got.Sessions) != 0 {
		t.Fatalf("snapshot = %+v, want no targets", got)
	}
}

func TestServiceCloseExistingIsAtomicAndDoesNotCreateTarget(t *testing.T) {
	service := newServiceWithRunner(func() Runner {
		t.Fatal("runner factory called for close-existing request")
		return &fakeRunner{}
	})

	resp := service.CloseExisting(Request{Session: "work", Pane: "default", Tab: "default"})
	if resp.Error != ErrTargetNotFound.Error() {
		t.Fatalf("Error = %q, want %q", resp.Error, ErrTargetNotFound)
	}

	runner := &fakeRunner{}
	service = newServiceWithRunner(func() Runner { return runner })
	if resp := service.Create(Request{Session: "work", Pane: "default", Tab: "default"}); resp.Error != "" {
		t.Fatalf("create error = %q", resp.Error)
	}
	if resp := service.CloseExisting(Request{Session: "work", Pane: "default", Tab: "default"}); resp.Error != "" {
		t.Fatalf("close error = %q", resp.Error)
	}
	if !runner.closed {
		t.Fatal("runner was not closed")
	}
	if resp := service.CloseExisting(Request{Session: "work", Pane: "default", Tab: "default"}); resp.Error != ErrTargetNotFound.Error() {
		t.Fatalf("second close error = %q, want %q", resp.Error, ErrTargetNotFound)
	}
}

func TestServicesKeepSameTargetNameIsolated(t *testing.T) {
	firstRunner := &fakeRunner{}
	secondRunner := &fakeRunner{}
	first := newServiceWithRunner(func() Runner { return firstRunner })
	second := newServiceWithRunner(func() Runner { return secondRunner })
	target := Request{Session: "work", Pane: "default", Tab: "default"}

	if resp := first.Create(target); resp.Error != "" {
		t.Fatalf("first create error = %q", resp.Error)
	}
	if resp := second.Create(target); resp.Error != "" {
		t.Fatalf("second create error = %q", resp.Error)
	}

	closeReq := target
	closeReq.Action = "close"
	if resp := first.Handle(closeReq, false); resp.Error != "" {
		t.Fatalf("first close error = %q", resp.Error)
	}
	if !firstRunner.closed {
		t.Fatal("first runner was not closed")
	}
	if secondRunner.closed {
		t.Fatal("second runner was closed")
	}
	if got := second.Store().Snapshot(); len(got.Sessions) != 1 {
		t.Fatalf("second snapshot = %+v, want isolated target", got)
	}
}

func TestServiceRejectsInvalidTargetBeforeRunnerStartup(t *testing.T) {
	called := false
	service := newServiceWithRunner(func() Runner {
		called = true
		return &fakeRunner{}
	})

	resp := service.Create(Request{Session: "bad/name", Pane: "default", Tab: "default"})
	if resp.Error == "" {
		t.Fatal("Create returned no validation error")
	}
	if called {
		t.Fatal("runner factory was called for invalid target")
	}
	if service.TargetCount() != 0 {
		t.Fatalf("TargetCount = %d, want 0", service.TargetCount())
	}
}

func TestServiceRequestLimits(t *testing.T) {
	service := newServiceWithRunner(func() Runner { return &fakeRunner{} })
	longName := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	if resp := service.Create(Request{Session: longName, Pane: "default", Tab: "default"}); resp.Error == "" {
		t.Fatal("Create accepted oversized target component")
	}
	if resp := service.Handle(Request{Action: "read", Session: "work", Pane: "default", Tab: "default", ReadCount: MaxReadCount + 1}, true); resp.Error == "" {
		t.Fatal("Handle accepted oversized read count")
	}
	if resp := service.Handle(Request{Action: "close"}, true); resp.Error == "" {
		t.Fatal("Handle accepted targetless close")
	}
}

func TestServiceAdmittedOperationCannotCreateAfterStopAdmission(t *testing.T) {
	called := false
	service := newServiceWithRunner(func() Runner {
		called = true
		return &fakeRunner{}
	})
	operationDone, err := service.beginOperation()
	if err != nil {
		t.Fatal(err)
	}
	service.StopAdmission()

	tab, targetDone, err := service.beginTargetUse(Request{Session: "work", Pane: "default", Tab: "default"}, true)
	operationDone()
	if !errors.Is(err, ErrServiceStopping) {
		t.Fatalf("beginTargetUse error = %v, want %v", err, ErrServiceStopping)
	}
	if tab != nil {
		t.Fatalf("beginTargetUse tab = %v, want nil", tab)
	}
	if targetDone != nil {
		t.Fatal("beginTargetUse completion is non-nil, want nil")
	}
	if called {
		t.Fatal("runner factory was called after admission stopped")
	}
	service.WaitOperations()
}

func TestServiceStopAdmissionWaitsForTargetCreationBoundary(t *testing.T) {
	factoryStarted := make(chan struct{})
	releaseFactory := make(chan struct{})
	runner := &fakeRunner{}
	service := newServiceWithRunner(func() Runner {
		close(factoryStarted)
		<-releaseFactory
		return runner
	})
	response := make(chan Response, 1)
	go func() {
		response <- service.Create(Request{Session: "work", Pane: "default", Tab: "default"})
	}()
	<-factoryStarted

	stopped := make(chan struct{})
	go func() {
		service.StopAdmission()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("StopAdmission returned before target creation crossed the lifecycle boundary")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFactory)
	<-stopped
	if resp := <-response; resp.Error != "" {
		t.Fatalf("Create error = %q", resp.Error)
	}
	if err := service.CloseAll(); err != nil {
		t.Fatal(err)
	}
	if !runner.closed {
		t.Fatal("runner created before shutdown was not closed")
	}
}

func TestServiceStopAdmissionRejectsNewOperationsButCloseAllRemainsReusable(t *testing.T) {
	service := newServiceWithRunner(func() Runner { return &fakeRunner{} })
	service.StopAdmission()
	resp := service.Create(Request{Session: "work", Pane: "default", Tab: "default"})
	if resp.Error != ErrServiceStopping.Error() {
		t.Fatalf("Create error = %q, want %q", resp.Error, ErrServiceStopping)
	}

	reusable := newServiceWithRunner(func() Runner { return &fakeRunner{} })
	if resp := reusable.Create(Request{Session: "work", Pane: "default", Tab: "default"}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	if err := reusable.CloseAll(); err != nil {
		t.Fatal(err)
	}
	if resp := reusable.Create(Request{Session: "again", Pane: "default", Tab: "default"}); resp.Error != "" {
		t.Fatalf("Create after reusable CloseAll: %s", resp.Error)
	}
}
