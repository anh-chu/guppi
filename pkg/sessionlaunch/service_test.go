package sessionlaunch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/model"
)

type fakeDaemon struct {
	created []createCall
	killed  []string
	err     error
	killErr error
	events  *[]string // pointer to shared event log
}

type createCall struct {
	name       string
	shell      string
	cwd        string
	cols, rows uint16
}

func (f *fakeDaemon) Create(name, shell, cwd string, cols, rows uint16) error {
	f.created = append(f.created, createCall{name: name, shell: shell, cwd: cwd, cols: cols, rows: rows})
	return f.err
}

func (f *fakeDaemon) Kill(name string) error {
	f.killed = append(f.killed, name)
	if f.events != nil {
		*f.events = append(*f.events, "kill")
	}
	return f.killErr
}

type fakeStateMgr struct {
	agentTypes map[string]string
	sessions   []*model.Session
	removed    []string
	events     *[]string // pointer to shared event log
}

func (f *fakeStateMgr) SetSessionAgentType(sessionName, agentType string) {
	if f.agentTypes == nil {
		f.agentTypes = map[string]string{}
	}
	f.agentTypes[sessionName] = agentType
}

func (f *fakeStateMgr) GetSessions() []*model.Session { return f.sessions }

func (f *fakeStateMgr) RemoveSession(name string) {
	f.removed = append(f.removed, name)
	if f.events != nil {
		*f.events = append(*f.events, "remove")
	}
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

func newService() (*Service, *fakeDaemon, *fakeStateMgr, *fakeAttrs, *fakeHub) {
	d := &fakeDaemon{}
	st := &fakeStateMgr{}
	a := &fakeAttrs{}
	h := &fakeHub{}
	s := &Service{
		DaemonReg: d,
		StateMgr:  st,
		Attrs:     a,
		Hub:       h,
		Refresh:   func() {},
	}
	return s, d, st, a, h
}

func TestCreateLocalSuccess(t *testing.T) {
	s, d, st, a, h := newService()

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
	if len(d.created) != 1 {
		t.Fatalf("created calls = %d", len(d.created))
	}
	call := d.created[0]
	if call.name != "foo" || call.shell != "bash" || call.cwd != "/tmp" || call.cols != 100 || call.rows != 30 {
		t.Fatalf("unexpected call: %+v", call)
	}
	if len(st.agentTypes) != 0 {
		t.Fatalf("agent type should not be set")
	}
	if len(a.calls) != 0 || len(h.broadcasts) != 0 {
		t.Fatalf("schedule metadata should not be written without schedule id")
	}
}

func TestCreateGeneratesName(t *testing.T) {
	s, d, _, _, _ := newService()

	res, err := s.Create(context.Background(), Request{Command: "node server.js", Path: "/home/proj"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Name != "node-proj" {
		t.Fatalf("name = %q", res.Name)
	}
	if d.created[0].name != "node-proj" {
		t.Fatalf("daemon created with %q", d.created[0].name)
	}
}

func TestCreateDeduplicatesName(t *testing.T) {
	s, d, _, _, _ := newService()
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
	if d.created[0].name != "foo-3" {
		t.Fatalf("daemon created with %q", d.created[0].name)
	}
}

func TestCreateRemoteSuccess(t *testing.T) {
	s, d, _, a, h := newService()
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
	if len(d.created) != 0 {
		t.Fatalf("local daemon should not be created for remote")
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

func TestCreateLocalHostQualifiedRequestUsesLocalDaemon(t *testing.T) {
	s, d, _, a, h := newService()

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
	if len(d.created) != 1 {
		t.Fatalf("expected one local daemon create, got %d", len(d.created))
	}
	if d.created[0].name != res.Name {
		t.Fatalf("resolved name mismatch: result %q, daemon created %q", res.Name, d.created[0].name)
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
	s, _, _, _, _ := newService()

	_, err := s.Create(context.Background(), Request{Host: "peer-1", Name: "foo"})
	if !errors.Is(err, ErrPeerUnavailable) {
		t.Fatalf("expected ErrPeerUnavailable, got %v", err)
	}
}

func TestCreateRemoteQueueFull(t *testing.T) {
	s, _, _, _, _ := newService()
	remote := &fakeRemote{err: ErrPeerQueueFull}
	s.Remote = remote.Launch

	_, err := s.Create(context.Background(), Request{Host: "peer-1", Name: "foo"})
	if !errors.Is(err, ErrPeerQueueFull) {
		t.Fatalf("expected ErrPeerQueueFull, got %v", err)
	}
}

func TestCreateNormalizesPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Exact "~" becomes empty so the daemon uses its default.
	s, d, _, _, _ := newService()
	_, err := s.Create(context.Background(), Request{Name: "foo", Path: "~", Command: "bash"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.created[0].cwd != "" {
		t.Fatalf("cwd = %q, want empty", d.created[0].cwd)
	}

	// Worktree path with ~ is expanded before git worktree add.
	d.created = nil
	repo := filepath.Join(home, "repo")
	initGitRepoAt(t, repo)
	res, err := s.Create(context.Background(), Request{Name: "feat", Path: "~/repo", WorktreeBranch: "feature", Command: "bash"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(repo, ".worktrees", "feature")
	if res.Path != want {
		t.Fatalf("path = %q, want %q", res.Path, want)
	}
	if d.created[0].cwd != want {
		t.Fatalf("daemon cwd = %q", d.created[0].cwd)
	}
}

func TestCreateWorktree(t *testing.T) {
	repo := initGitRepo(t)
	t.Setenv("HOME", t.TempDir())
	s, d, _, _, _ := newService()

	res, err := s.Create(context.Background(), Request{Name: "feat", Path: repo, WorktreeBranch: "feature", Command: "bash"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(repo, ".worktrees", "feature")
	if res.Path != want {
		t.Fatalf("path = %q, want %q", res.Path, want)
	}
	if d.created[0].cwd != want {
		t.Fatalf("daemon cwd = %q", d.created[0].cwd)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("worktree dir missing: %v", err)
	}
}

func TestCreateWorktreeRollbackOnSpawnFailure(t *testing.T) {
	repo := initGitRepo(t)
	t.Setenv("HOME", t.TempDir())
	s, d, _, _, _ := newService()
	d.err = errors.New("spawn-err")

	_, err := s.Create(context.Background(), Request{Name: "feat", Path: repo, WorktreeBranch: "feature", Command: "bash"})
	if !errors.Is(err, ErrSpawn) {
		t.Fatalf("expected ErrSpawn, got %v", err)
	}
	dest := filepath.Join(repo, ".worktrees", "feature")
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("worktree dir should have been rolled back: %v", statErr)
	}
}

func TestCreateAgentType(t *testing.T) {
	s, _, st, _, _ := newService()

	_, err := s.Create(context.Background(), Request{Name: "foo", AgentType: "go"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.agentTypes["foo"] != "go" {
		t.Fatalf("agent type = %q", st.agentTypes["foo"])
	}
}

func TestCreateScheduleMetadataLocalHost(t *testing.T) {
	s, _, _, a, h := newService()

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
	s, _, _, a, _ := newService()

	_, err := s.Create(context.Background(), Request{Name: "foo", ScheduleID: "sched-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.calls[0].key != "foo" {
		t.Fatalf("attr key = %q", a.calls[0].key)
	}
}

func TestCreateRefreshOnce(t *testing.T) {
	s, _, _, _, _ := newService()
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
	s, _, _, _, _ := newService()

	_, err := s.Create(context.Background(), Request{})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}

	_, err = s.Create(context.Background(), Request{Name: "foo:bar"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for reserved char, got %v", err)
	}
}

func TestCreateNoPartialMetadataOnSpawnFailure(t *testing.T) {
	s, d, _, a, h := newService()
	d.err = errors.New("boom")

	_, err := s.Create(context.Background(), Request{Name: "foo", ScheduleID: "sched-1"})
	if !errors.Is(err, ErrSpawn) {
		t.Fatalf("expected ErrSpawn, got %v", err)
	}
	if len(a.calls) != 0 || len(h.broadcasts) != 0 {
		t.Fatalf("metadata should not be written on spawn failure")
	}
}

func TestCreateCommandShellNormalized(t *testing.T) {
	s, d, _, _, _ := newService()

	_, err := s.Create(context.Background(), Request{Name: "foo", Command: "shell"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.created[0].shell != "" {
		t.Fatalf("shell = %q", d.created[0].shell)
	}
}

func TestResolveNameUsesFallback(t *testing.T) {
	s, d, _, _, _ := newService()

	res, err := s.Create(context.Background(), Request{Fallback: "fallback-123"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(res.Name, "fallback-") {
		t.Fatalf("name = %q", res.Name)
	}
	if d.created[0].name != res.Name {
		t.Fatalf("daemon name = %q", d.created[0].name)
	}
}

func TestKillEmptyNameRejected(t *testing.T) {
	s, _, _, _, _ := newService()

	err := s.Kill("", "test-reason")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestKillWhitespaceNameRejected(t *testing.T) {
	s, _, _, _, _ := newService()

	err := s.Kill("  ", "test-reason")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestKillExecutesStepsInOrder(t *testing.T) {
	s, d, st, _, _ := newService()
	var events []string
	d.events = &events
	st.events = &events
	
	s.Forget = func(name string) error {
		events = append(events, "forget")
		return nil
	}

	err := s.Kill("session-1", "test-reason")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify steps executed in order: daemon kill, remove session, forget
	expectedOrder := []string{"kill", "remove", "forget"}
	if len(events) != len(expectedOrder) {
		t.Fatalf("expected %d events in order %v, got %d events: %v", len(expectedOrder), expectedOrder, len(events), events)
	}
	for i, e := range expectedOrder {
		if events[i] != e {
			t.Fatalf("event %d: expected %q, got %q; full sequence: %v", i, e, events[i], events)
		}
	}
}

func TestKillDaemonErrorDoesNotSkipRemaining(t *testing.T) {
	s, d, st, _, _ := newService()
	d.killErr = errors.New("daemon error")
	var forgetCalls []string
	s.Forget = func(name string) error {
		forgetCalls = append(forgetCalls, name)
		return nil
	}

	err := s.Kill("session-1", "test-reason")
	if err == nil {
		t.Fatalf("expected error from daemon failure")
	}

	// Verify remaining steps still executed despite daemon error
	if len(st.removed) != 1 || st.removed[0] != "session-1" {
		t.Fatalf("state remove should still execute, got %v", st.removed)
	}
	if len(forgetCalls) != 1 || forgetCalls[0] != "session-1" {
		t.Fatalf("forget should still execute, got %v", forgetCalls)
	}
}

func TestKillNilForgetTolerated(t *testing.T) {
	s, d, st, _, _ := newService()
	s.Forget = nil

	err := s.Kill("session-1", "test-reason")
	if err != nil {
		t.Fatalf("unexpected error when Forget is nil: %v", err)
	}

	if len(d.killed) != 1 {
		t.Fatalf("daemon kill should execute")
	}
	if len(st.removed) != 1 {
		t.Fatalf("state remove should execute")
	}
}

func TestKillNilDaemonRegTolerated(t *testing.T) {
	s, _, st, _, _ := newService()
	s.DaemonReg = nil
	var forgetCalls []string
	s.Forget = func(name string) error {
		forgetCalls = append(forgetCalls, name)
		return nil
	}

	err := s.Kill("session-1", "test-reason")
	if err != nil {
		t.Fatalf("unexpected error when DaemonReg is nil: %v", err)
	}

	// Other steps should still execute
	if len(st.removed) != 1 {
		t.Fatalf("state remove should execute")
	}
	if len(forgetCalls) != 1 {
		t.Fatalf("forget should execute")
	}
}

func TestKillNilStateMgrTolerated(t *testing.T) {
	s, d, _, _, _ := newService()
	s.StateMgr = nil
	var forgetCalls []string
	s.Forget = func(name string) error {
		forgetCalls = append(forgetCalls, name)
		return nil
	}

	err := s.Kill("session-1", "test-reason")
	if err != nil {
		t.Fatalf("unexpected error when StateMgr is nil: %v", err)
	}

	// Other steps should still execute
	if len(d.killed) != 1 {
		t.Fatalf("daemon kill should execute")
	}
	if len(forgetCalls) != 1 {
		t.Fatalf("forget should execute")
	}
}

func TestKillCombinesErrors(t *testing.T) {
	s, d, _, _, _ := newService()
	d.killErr = errors.New("kill error")
	var forgetCalls []string
	s.Forget = func(name string) error {
		forgetCalls = append(forgetCalls, name)
		return errors.New("forget error")
	}

	err := s.Kill("session-1", "test-reason")
	if err == nil {
		t.Fatalf("expected combined error")
	}

	// Both errors should be present in joined error
	errStr := err.Error()
	if !strings.Contains(errStr, "kill error") {
		t.Fatalf("error should contain 'kill error', got %v", err)
	}
	if !strings.Contains(errStr, "forget error") {
		t.Fatalf("error should contain 'forget error', got %v", err)
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	initGitRepoAt(t, dir)
	return dir
}

func initGitRepoAt(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2024-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2024-01-01T00:00:00Z")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", args, out)
		}
	}
}
