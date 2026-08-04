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
	"github.com/anh-chu/termyard/pkg/state"
)

type fakeDaemon struct {
	created []createCall
	err     error
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

type fakeStateMgr struct {
	agentTypes map[string]string
	sessions   []*model.Session
}

func (f *fakeStateMgr) SetSessionAgentType(sessionName, agentType string) {
	if f.agentTypes == nil {
		f.agentTypes = map[string]string{}
	}
	f.agentTypes[sessionName] = agentType
}

func (f *fakeStateMgr) GetSessions() []*model.Session { return f.sessions }

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

// fakeV2Commander is a minimal V2Commander stub used only to make
// s.V2Commander non-nil in routing tests; createRemote's routing decision
// only checks V2Commander != nil, it never calls it directly (session
// creation via V2Commander is exercised by createLocal's own tests).
type fakeV2Commander struct{}

func (fakeV2Commander) ExecuteSessionCommand(ctx context.Context, cmd state.SessionCommand) (state.CommandResult, error) {
	return state.CommandResult{}, nil
}

// TestCreateRemoteV2ModePrefersV2RemoteOverLegacy proves the Finding-3 fix:
// once this node is v2-only (V2Commander set) and a v2 remote-create path is
// wired (V2Remote set), createRemote must always route through V2Remote and
// must never fall back to the legacy fire-and-forget Remote launcher, even
// though both are configured on the Service.
func TestCreateRemoteV2ModePrefersV2RemoteOverLegacy(t *testing.T) {
	s, _, _, a, h := newService()
	s.V2Commander = fakeV2Commander{}
	s.Fanout = (&fanoutSpy{}).Fanout

	legacyRemote := &fakeRemote{result: Result{Name: "should-not-be-used"}}
	s.Remote = legacyRemote.Launch

	v2Remote := &fakeRemote{result: Result{Name: "foo", Host: "peer-1"}}
	s.V2Remote = v2Remote.Launch

	res, err := s.Create(context.Background(), Request{Host: "peer-1", LocalHost: "local-fingerprint", Name: "foo", ScheduleID: "sched-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Remote {
		t.Fatalf("expected remote result")
	}
	if len(legacyRemote.called) != 0 {
		t.Fatalf("legacy Remote launcher must not be called when V2Remote is configured, got %d calls", len(legacyRemote.called))
	}
	if len(v2Remote.called) != 1 {
		t.Fatalf("expected exactly one V2Remote call, got %d", len(v2Remote.called))
	}
	// Schedule-metadata fanout must still work through the v2 remote path.
	if len(a.calls) != 1 || a.calls[0].key != "peer-1/foo" || a.calls[0].scheduleID != "sched-1" {
		t.Fatalf("unexpected attr calls: %+v", a.calls)
	}
	if len(h.broadcasts) != 1 || h.broadcasts[0]["key"] != "peer-1/foo" {
		t.Fatalf("unexpected broadcasts: %+v", h.broadcasts)
	}
}

// TestCreateRemoteLegacyModeUnaffected proves legacy-mode remote creation
// (V2Commander nil) is completely unchanged: it must still use the legacy
// Remote launcher exactly as before, even if a V2Remote happens to be set.
func TestCreateRemoteLegacyModeUnaffected(t *testing.T) {
	s, _, _, _, _ := newService()
	// V2Commander intentionally left nil: this is a legacy-mode Service.

	legacyRemote := &fakeRemote{result: Result{Name: "foo", Host: "peer-1"}}
	s.Remote = legacyRemote.Launch

	v2Remote := &fakeRemote{result: Result{Name: "should-not-be-used"}}
	s.V2Remote = v2Remote.Launch

	res, err := s.Create(context.Background(), Request{Host: "peer-1", LocalHost: "local-fingerprint", Name: "foo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Remote {
		t.Fatalf("expected remote result")
	}
	if len(v2Remote.called) != 0 {
		t.Fatalf("V2Remote must not be called in legacy mode, got %d calls", len(v2Remote.called))
	}
	if len(legacyRemote.called) != 1 {
		t.Fatalf("expected exactly one legacy Remote call, got %d", len(legacyRemote.called))
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
