package sessionlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

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

type fakeAttrs struct {
	calls []struct{ key, scheduleID string }
	attrs map[string]ScheduleAttr
}

func (f *fakeAttrs) SetScheduleID(key, scheduleID string) (ScheduleAttr, error) {
	f.calls = append(f.calls, struct{ key, scheduleID string }{key, scheduleID})
	attr := ScheduleAttr{ScheduleID: scheduleID, UpdatedAt: time.Now()}
	if f.attrs == nil {
		f.attrs = map[string]ScheduleAttr{}
	}
	f.attrs[key] = attr
	return attr, nil
}

type fakeHub struct {
	broadcasts []map[string]interface{}
}

func (f *fakeHub) BroadcastJSON(v interface{}) {
	m, _ := v.(map[string]interface{})
	f.broadcasts = append(f.broadcasts, m)
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

type fanoutSpy struct {
	calls []struct {
		key  string
		attr ScheduleAttr
	}
}

func (f *fanoutSpy) Fanout(key string, attr ScheduleAttr) {
	f.calls = append(f.calls, struct {
		key  string
		attr ScheduleAttr
	}{key, attr})
}

func newService() (*Service, *fakeCommander, *fakeAttrs, *fakeHub) {
	c := &fakeCommander{}
	a := &fakeAttrs{}
	h := &fakeHub{}
	s := &Service{
		Commander: c,
		Attrs:     a,
		Hub:       h,
		Refresh:   func() {},
	}
	return s, c, a, h
}

func TestCreateLocalSuccess(t *testing.T) {
	s, c, a, h := newService()

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
	if len(a.calls) != 0 || len(h.broadcasts) != 0 {
		t.Fatalf("schedule metadata should not be written without schedule id")
	}
}

func TestCreateGeneratesName(t *testing.T) {
	s, c, _, _ := newService()

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

func TestCreateDeduplicatesName(t *testing.T) {
	s, c, _, _ := newService()
	s.Names = func(host string) []string {
		return []string{"foo", "foo-2"}
	}

	res, err := s.Create(context.Background(), Request{Name: "foo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Name != "foo-3" {
		t.Fatalf("name = %q", res.Name)
	}
	if c.params(t, 0).Name != "foo-3" {
		t.Fatalf("commander created with %q", c.params(t, 0).Name)
	}
}

func TestCreateRemoteSuccess(t *testing.T) {
	s, c, a, h := newService()
	remote := &fakeRemote{result: Result{Name: "foo"}}
	s.Remote = remote.Launch
	s.Fanout = (&fanoutSpy{}).Fanout

	res, err := s.Create(context.Background(), Request{Host: "peer-1", LocalHost: "local-fingerprint", Name: "foo", ScheduleID: "sched-1"})
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
	if len(a.calls) != 1 || a.calls[0].key != "peer-1/foo" || a.calls[0].scheduleID != "sched-1" {
		t.Fatalf("unexpected attr calls: %+v", a.calls)
	}
	if len(h.broadcasts) != 1 || h.broadcasts[0]["key"] != "peer-1/foo" {
		t.Fatalf("unexpected broadcasts: %+v", h.broadcasts)
	}
}

// TestCreateRemotePrefersReliableRemoteOverFireAndForget proves the Finding-3 fix:
// when a reliable remote-create path is wired (ReliableRemote set),
// createRemote must always route through ReliableRemote and must never fall
// back to the fire-and-forget Remote launcher, even though both are
// configured on the Service.
func TestCreateRemotePrefersReliableRemoteOverFireAndForget(t *testing.T) {
	s, _, a, h := newService()
	s.Fanout = (&fanoutSpy{}).Fanout

	fireAndForgetRemote := &fakeRemote{result: Result{Name: "should-not-be-used"}}
	s.Remote = fireAndForgetRemote.Launch

	reliableRemote := &fakeRemote{result: Result{Name: "foo", Host: "peer-1"}}
	s.ReliableRemote = reliableRemote.Launch

	res, err := s.Create(context.Background(), Request{Host: "peer-1", LocalHost: "local-fingerprint", Name: "foo", ScheduleID: "sched-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Remote {
		t.Fatalf("expected remote result")
	}
	if len(fireAndForgetRemote.called) != 0 {
		t.Fatalf("fire-and-forget Remote launcher must not be called when ReliableRemote is configured, got %d calls", len(fireAndForgetRemote.called))
	}
	if len(reliableRemote.called) != 1 {
		t.Fatalf("expected exactly one ReliableRemote call, got %d", len(reliableRemote.called))
	}
	// Schedule-metadata fanout must still work through the reliable remote path.
	if len(a.calls) != 1 || a.calls[0].key != "peer-1/foo" || a.calls[0].scheduleID != "sched-1" {
		t.Fatalf("unexpected attr calls: %+v", a.calls)
	}
	if len(h.broadcasts) != 1 || h.broadcasts[0]["key"] != "peer-1/foo" {
		t.Fatalf("unexpected broadcasts: %+v", h.broadcasts)
	}
}

// TestCreateLocalCarriesAgentType proves AgentType flows into the
// CreateParams command payload.
func TestCreateLocalCarriesAgentType(t *testing.T) {
	s, c, _, _ := newService()
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

func TestCreateLocalHostQualifiedRequestUsesLocalDaemon(t *testing.T) {
	s, c, a, h := newService()

	var namesHost string
	s.Names = func(host string) []string {
		namesHost = host
		return nil
	}
	var remoteCalled int
	s.Remote = func(ctx context.Context, req Request) (Result, error) {
		remoteCalled++
		t.Fatalf("remote launch should not be called for local host, got req=%+v", req)
		return Result{}, nil
	}

	res, err := s.Create(context.Background(), Request{
		Name:       "shell",
		Host:       "local-fingerprint",
		LocalHost:  "local-fingerprint",
		ScheduleID: "sched-1",
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
	if res.Host != "" {
		t.Fatalf("result.Host = %q, want empty", res.Host)
	}
	if namesHost != "" {
		t.Fatalf("Names called with %q, want empty host", namesHost)
	}
	wantKey := "local-fingerprint/" + res.Name
	if len(a.calls) != 1 || a.calls[0].key != wantKey || a.calls[0].scheduleID != "sched-1" {
		t.Fatalf("unexpected attr calls: %+v", a.calls)
	}
	if len(h.broadcasts) != 1 || h.broadcasts[0]["key"] != wantKey {
		t.Fatalf("unexpected broadcasts: %+v", h.broadcasts)
	}
}

func TestCreateRemoteUnavailable(t *testing.T) {
	s, _, _, _ := newService()

	_, err := s.Create(context.Background(), Request{Host: "peer-1", Name: "foo"})
	if !errors.Is(err, ErrPeerUnavailable) {
		t.Fatalf("expected ErrPeerUnavailable, got %v", err)
	}
}

func TestCreateRemoteQueueFull(t *testing.T) {
	s, _, _, _ := newService()
	remote := &fakeRemote{err: ErrPeerQueueFull}
	s.Remote = remote.Launch

	_, err := s.Create(context.Background(), Request{Host: "peer-1", Name: "foo"})
	if !errors.Is(err, ErrPeerQueueFull) {
		t.Fatalf("expected ErrPeerQueueFull, got %v", err)
	}
}

func TestCreateScheduleMetadataLocalHost(t *testing.T) {
	s, _, a, h := newService()

	_, err := s.Create(context.Background(), Request{Name: "foo", ScheduleID: "sched-1", LocalHost: "host1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(a.calls) != 1 || a.calls[0].key != "host1/foo" {
		t.Fatalf("attr key = %q", a.calls[0].key)
	}
	if len(h.broadcasts) != 1 || h.broadcasts[0]["key"] != "host1/foo" {
		t.Fatalf("broadcast key = %v", h.broadcasts)
	}
}

func TestCreateScheduleMetadataBareKey(t *testing.T) {
	s, _, a, _ := newService()

	_, err := s.Create(context.Background(), Request{Name: "foo", ScheduleID: "sched-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.calls[0].key != "foo" {
		t.Fatalf("attr key = %q", a.calls[0].key)
	}
}

func TestCreateRefreshOnce(t *testing.T) {
	s, _, _, _ := newService()
	var refreshCount int
	s.Refresh = func() { refreshCount++ }

	_, err := s.Create(context.Background(), Request{Name: "foo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if refreshCount != 1 {
		t.Fatalf("refreshCount = %d", refreshCount)
	}
}

func TestCreateInvalidInput(t *testing.T) {
	s, _, _, _ := newService()

	_, err := s.Create(context.Background(), Request{})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}

	_, err = s.Create(context.Background(), Request{Name: "foo:bar"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for reserved char, got %v", err)
	}
}

// TestCreateNoPartialMetadataOnSpawnFailure proves that when the Commander
// fails to create the session, no schedule metadata is written and the
// underlying error propagates unwrapped.
func TestCreateNoPartialMetadataOnSpawnFailure(t *testing.T) {
	s, c, a, h := newService()
	sentinel := errors.New("boom")
	c.err = sentinel

	_, err := s.Create(context.Background(), Request{Name: "foo", ScheduleID: "sched-1"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if len(a.calls) != 0 || len(h.broadcasts) != 0 {
		t.Fatalf("metadata should not be written on create failure")
	}
}

func TestResolveNameUsesFallback(t *testing.T) {
	s, c, _, _ := newService()

	res, err := s.Create(context.Background(), Request{Fallback: "fallback-123"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(res.Name, "fallback-") {
		t.Fatalf("name = %q", res.Name)
	}
	if c.params(t, 0).Name != res.Name {
		t.Fatalf("commander name = %q", c.params(t, 0).Name)
	}
}
