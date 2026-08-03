package state

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/anh-chu/termyard/pkg/groupsync"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/sessionattrs"
	"github.com/anh-chu/termyard/pkg/sessionorder"
)

func makeSessionFixture(i int) *model.Session {
	name := fmt.Sprintf("sess-%04d", i)
	return &model.Session{
		ID:               name,
		Name:             name,
		Backend:          "daemon",
		Created:          time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second),
		Attached:         true,
		LastActivity:     time.Now(),
		ProjectPath:      "/home/user/projects/demo",
		AgentType:        "claude",
		PromptPreview:    "working on feature",
		AgentSessionID:   fmt.Sprintf("agent-%d", i),
		UserPrompt:       "implement the thing",
		LastAgentMessage: "done",
		DisplayName:      fmt.Sprintf("Demo %d", i),
		Windows: []*model.Window{{
			ID:     name + ":0",
			Name:   "shell",
			Index:  0,
			Active: true,
			Layout: "tiled",
			Panes: []*model.Pane{{
				ID:             name + ":0.0",
				WindowID:       name + ":0",
				SessionID:      name,
				Index:          0,
				Active:         true,
				Width:          120,
				Height:         40,
				CurrentCommand: "bash",
				CurrentPath:    "/home/user/projects/demo",
				PID:            1000 + i,
			}},
		}},
	}
}

func BenchmarkSerializedSizeSessions(b *testing.B) {
	for _, n := range []int{1, 10, 100, 500} {
		b.Run(fmt.Sprintf("sessions-%d", n), func(b *testing.B) {
			mgr := NewManager()
			sessions := make([]*model.Session, n)
			for i := range sessions {
				sessions[i] = makeSessionFixture(i)
			}
			mgr.UpdateSessions(sessions)

			var buf []byte
			for i := 0; i < b.N; i++ {
				buf, _ = json.Marshal(mgr.SnapshotForManifest())
			}
			b.ReportMetric(float64(len(buf)), "bytes")
			b.SetBytes(int64(len(buf)))
			b.Logf("sessions-%d serialized size: %d bytes", n, len(buf))
		})
	}
}

func BenchmarkSerializedSizeGroups(b *testing.B) {
	for _, n := range []int{1, 10, 100} {
		b.Run(fmt.Sprintf("groups-%d", n), func(b *testing.B) {
			b.Setenv("HOME", b.TempDir())
			store, err := groupsync.NewStore()
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < n; i++ {
				id := fmt.Sprintf("group-%04d", i)
				key := fmt.Sprintf("sess-%04d", i)
				tree, _ := json.Marshal(map[string]any{
					"type":       "leaf",
					"sessionKey": key,
				})
				_, _ = store.SetTree(id, tree)
				_, _ = store.SetName(id, fmt.Sprintf("Group %d", i), groupsync.NameModeManual)
				_, _ = store.SetRank(id, fmt.Sprintf("rank-%d", i))
			}

			var buf []byte
			for i := 0; i < b.N; i++ {
				buf, _ = json.Marshal(store.Snapshot())
			}
			b.ReportMetric(float64(len(buf)), "bytes")
			b.SetBytes(int64(len(buf)))
			b.Logf("groups-%d serialized size: %d bytes", n, len(buf))
		})
	}
}

func BenchmarkSerializedSizeSessionOrder(b *testing.B) {
	for _, n := range []int{1, 10, 100, 500} {
		b.Run(fmt.Sprintf("order-%d", n), func(b *testing.B) {
			b.Setenv("HOME", b.TempDir())
			store, err := sessionorder.NewStore()
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < n; i++ {
				key := fmt.Sprintf("local/sess-%04d", i)
				_, _ = store.Set(key, fmt.Sprintf("rank-%d", i))
			}

			var buf []byte
			for i := 0; i < b.N; i++ {
				buf, _ = json.Marshal(store.Snapshot())
			}
			b.ReportMetric(float64(len(buf)), "bytes")
			b.SetBytes(int64(len(buf)))
			b.Logf("session-order-%d serialized size: %d bytes", n, len(buf))
		})
	}
}

func BenchmarkSerializedSizeSessionAttrs(b *testing.B) {
	for _, n := range []int{1, 10, 100, 500} {
		b.Run(fmt.Sprintf("attrs-%d", n), func(b *testing.B) {
			b.Setenv("HOME", b.TempDir())
			store, err := sessionattrs.NewStore()
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < n; i++ {
				key := fmt.Sprintf("local/sess-%04d", i)
				_, _ = store.Set(key, true, i%2 == 0)
				_, _ = store.SetScheduleID(key, fmt.Sprintf("sched-%d", i))
			}

			var buf []byte
			for i := 0; i < b.N; i++ {
				buf, _ = json.Marshal(store.Sets())
			}
			b.ReportMetric(float64(len(buf)), "bytes")
			b.SetBytes(int64(len(buf)))
			b.Logf("session-attrs-%d serialized size: %d bytes", n, len(buf))
		})
	}
}
