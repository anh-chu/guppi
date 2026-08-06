package sessionlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/anh-chu/termyard/pkg/state"
)

// fakeCommander is the shared Commander stub used by every test: it records
// each SessionCommand it receives (unmarshaled CreateParams included) and,
// unless err is set, echoes back a CommandResult that mirrors what the real
// canonical Commander would produce for a create (DisplayName defaults to
// the resolved name, Path defaults to the requested cwd).
type fakeCommander struct {
	calls       []state.SessionCommand
	err         error
	displayName string // overrides the echoed DisplayName when set
	path        string // overrides the echoed Path when set
}

func (f *fakeCommander) ExecuteSessionCommand(ctx context.Context, cmd state.SessionCommand) (state.CommandResult, error) {
	f.calls = append(f.calls, cmd)
	if f.err != nil {
		return state.CommandResult{}, f.err
	}
	var params state.CreateParams
	_ = json.Unmarshal(cmd.Params, &params)
	name := f.displayName
	if name == "" {
		name = params.Name
	}
	path := f.path
	if path == "" {
		path = params.Cwd
	}
	return state.CommandResult{DisplayName: name, Path: path}, nil
}

// params unmarshals the CreateParams payload of the call at index i. Fails
// the test if the call is out of range or the payload doesn't decode.
func (f *fakeCommander) params(t *testing.T, i int) state.CreateParams {
	t.Helper()
	if i >= len(f.calls) {
		t.Fatalf("call %d out of range, only %d calls recorded", i, len(f.calls))
	}
	var params state.CreateParams
	if err := json.Unmarshal(f.calls[i].Params, &params); err != nil {
		t.Fatalf("unmarshal params for call %d: %v", i, err)
	}
	return params
}

type fakeRemote struct {
	called []Request
	result Result
	err    error
}

func (f *fakeRemote) Launch(ctx context.Context, req Request) (Result, error) {
	f.called = append(f.called, req)
	return f.result, f.err
}

func newService() (*Service, *fakeCommander) {
	c := &fakeCommander{}
	s := &Service{Commander: c}
	return s, c
}

func TestCreateLocalSuccess(t *testing.T) {
	s, c := newService()

	res, err := s.Create(context.Background(), Request{Name: "foo", Path: "/tmp", Command: "bash", Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Name != "foo" {
		t.Fatalf("name = %q", res.Name)
	}
	if res.Path != "/tmp" {
		t.Fatalf("path = %q", res.Path)
	}
	if len(c.calls) != 1 {
		t.Fatalf("commander calls = %d", len(c.calls))
	}
	params := c.params(t, 0)
	if params.Name != "foo" || params.Shell != "bash" || params.Cwd != "/tmp" || params.Cols != 100 || params.Rows != 30 {
		t.Fatalf("unexpected params: %+v", params)
	}
}

func TestCreateGeneratesName(t *testing.T) {
	s, c := newService()

	res, err := s.Create(context.Background(), Request{Command: "node server.js", Path: "/home/proj"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Name != "node-proj" {
		t.Fatalf("name = %q", res.Name)
	}
	if c.params(t, 0).Name != "node-proj" {
		t.Fatalf("commander created with %q", c.params(t, 0).Name)
	}
}

func TestCreateRemoteSuccess(t *testing.T) {
	s, c := newService()
	remote := &fakeRemote{result: Result{Name: "foo"}}
	s.RemoteCreate = remote.Launch

	res, err := s.Create(context.Background(), Request{TargetOwner: state.OwnerID("peer-1"), Name: "foo", ScheduleID: "sched-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Remote {
		t.Fatalf("expected remote result")
	}
	if len(c.calls) != 0 {
		t.Fatalf("local commander should not be called for remote")
	}
	if len(remote.called) != 1 {
		t.Fatalf("remote not called")
	}
}

// TestCreateLocalCarriesAgentType proves AgentType flows into the
// CreateParams command payload.
func TestCreateLocalCarriesAgentType(t *testing.T) {
	s, c := newService()
	c.displayName = "foo"

	res, err := s.Create(context.Background(), Request{Name: "foo", AgentType: "claude"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Name != "foo" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if len(c.calls) != 1 {
		t.Fatalf("expected exactly one create command, got %d", len(c.calls))
	}
	params := c.params(t, 0)
	if params.AgentType != "claude" {
		t.Fatalf("expected AgentType to be carried in CreateParams, got %+v", params)
	}
}

// TestCreateLocalOwnerTargetUsesLocalDaemon proves a request whose
// TargetOwner equals the Service's own LocalOwner is routed to Commander,
// not RemoteCreate.
func TestCreateLocalOwnerTargetUsesLocalDaemon(t *testing.T) {
	s, c := newService()
	s.LocalOwner = state.OwnerID("local-owner")

	var remoteCalled int
	s.RemoteCreate = func(ctx context.Context, req Request) (Result, error) {
		remoteCalled++
		t.Fatalf("remote launch should not be called for local owner target, got req=%+v", req)
		return Result{}, nil
	}

	res, err := s.Create(context.Background(), Request{
		Name:        "shell",
		TargetOwner: state.OwnerID("local-owner"),
		ScheduleID:  "sched-1",
	})
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if remoteCalled != 0 {
		t.Fatalf("remote launch called %d times", remoteCalled)
	}
	if len(c.calls) != 1 {
		t.Fatalf("expected one local commander create, got %d", len(c.calls))
	}
	if c.params(t, 0).Name != res.Name {
		t.Fatalf("resolved name mismatch: result %q, commander created %q", res.Name, c.params(t, 0).Name)
	}
	if res.Remote {
		t.Fatalf("expected local result")
	}
	if res.TargetOwner != "" {
		t.Fatalf("expected empty TargetOwner on local result, got %q", res.TargetOwner)
	}
}

func TestCreateRemoteUnavailable(t *testing.T) {
	s, _ := newService()

	_, err := s.Create(context.Background(), Request{TargetOwner: state.OwnerID("peer-1"), Name: "foo"})
	if !errors.Is(err, ErrPeerUnavailable) {
		t.Fatalf("expected ErrPeerUnavailable, got %v", err)
	}
}

func TestCreateRemoteQueueFull(t *testing.T) {
	s, _ := newService()
	remote := &fakeRemote{err: ErrPeerQueueFull}
	s.RemoteCreate = remote.Launch

	_, err := s.Create(context.Background(), Request{TargetOwner: state.OwnerID("peer-1"), Name: "foo"})
	if !errors.Is(err, ErrPeerQueueFull) {
		t.Fatalf("expected ErrPeerQueueFull, got %v", err)
	}
}

func TestCreateInvalidInput(t *testing.T) {
	s, _ := newService()

	_, err := s.Create(context.Background(), Request{})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}

	_, err = s.Create(context.Background(), Request{Name: "foo:bar"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for reserved char, got %v", err)
	}
}

// TestCreatePropagatesSpawnFailure proves that when the Commander fails to
// create the session, the underlying error propagates unwrapped.
func TestCreatePropagatesSpawnFailure(t *testing.T) {
	s, c := newService()
	sentinel := errors.New("boom")
	c.err = sentinel

	_, err := s.Create(context.Background(), Request{Name: "foo", ScheduleID: "sched-1"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
}
