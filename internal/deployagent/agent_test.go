package deployagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	p "authcenter/internal/deployprotocol"
)

const currentID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const previousID = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func testAgent(t *testing.T) *Agent {
	t.Helper()
	root := t.TempDir()
	for _, file := range []string{"compose.yaml", "authctl.sh", ".env"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a, err := New(Config{Root: root, Socket: filepath.Join(root, "agent.sock"), Repository: "owner/repo"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// A Docker boundary fixture checks the selected immutable image and represents
// the existing shell tool's final health result without opening any database.
func fakeDocker(t *testing.T, a *Agent, hold <-chan struct{}, fail bool, previousSchema int) {
	t.Helper()
	var mu sync.Mutex
	active := currentID
	a.run = func(ctx context.Context, root string, args, env []string) ([]byte, error) {
		if args[0] == "sh" {
			if len(args) != 3 || args[2] != "rollback" {
				t.Errorf("unexpected command %v", args)
			}
			if !strings.Contains(strings.Join(env, "\n"), "AUTH_EXPECT_PREVIOUS_ID="+previousID) {
				t.Error("missing target guard")
			}
			if hold != nil {
				select {
				case <-hold:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			if fail {
				return nil, errors.New("health failed")
			}
			mu.Lock()
			active = previousID
			mu.Unlock()
			if err := os.WriteFile(filepath.Join(a.cfg.Root, ".deployment", "state"), []byte("1.1.0\n"+previousID+"\n1.2.0\n"+currentID+"\n"), 0600); err != nil {
				return nil, err
			}
			return nil, nil
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case args[1] == "ps":
			return []byte("container\n"), nil
		case args[1] == "inspect":
			return []byte(active + "\ntrue\nhealthy\nauth-center_auth-data\n"), nil
		case args[1] == "image":
			if args[len(args)-1] == previousID {
				return []byte("1.1.0\n" + string(rune('0'+previousSchema)) + "\n"), nil
			}
			return []byte("1.2.0\n2\n"), nil
		}
		return nil, errors.New("unexpected docker invocation")
	}
	if err := os.WriteFile(filepath.Join(a.cfg.Root, ".deployment", "state"), []byte("1.2.0\n"+currentID+"\n1.1.0\n"+previousID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func rollbackRequest() p.Request {
	return p.Request{Action: "rollback", ExpectedImage: currentID, TargetImage: previousID, IdempotencyKey: "request-123456789", Actor: "admin"}
}

func waitJob(t *testing.T, a *Agent) *p.Job {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		a.mu.Lock()
		j := cloneJob(a.last)
		active := a.active
		a.mu.Unlock()
		if !active {
			return j
		}
		select {
		case <-deadline:
			t.Fatal("job did not finish")
			return nil
		case <-time.After(time.Millisecond * 5):
		}
	}
}

func TestJobLifecycle(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "health_failure"}[fail], func(t *testing.T) {
			a := testAgent(t)
			gate := make(chan struct{})
			fakeDocker(t, a, gate, fail, 2)
			req := rollbackRequest()
			j, code, err := a.enqueue(context.Background(), req)
			if err != nil || code != 202 {
				t.Fatalf("enqueue %d %v", code, err)
			}
			duplicate, _, err := a.enqueue(context.Background(), req)
			if err != nil || duplicate.ID != j.ID {
				t.Fatal("idempotency failed")
			}
			changed := req
			changed.TargetImage = currentID
			if _, code, _ = a.enqueue(context.Background(), changed); code != 409 {
				t.Fatal("changed duplicate accepted")
			}
			changed = req
			changed.IdempotencyKey = "another-request-123"
			if _, code, _ = a.enqueue(context.Background(), changed); code != 409 {
				t.Fatal("concurrent job accepted")
			}
			close(gate)
			result := waitJob(t, a)
			want := "succeeded"
			if fail {
				want = "failed"
			}
			if result.Status != want {
				t.Fatalf("result %+v", result)
			}
			b, err := os.ReadFile(filepath.Join(a.cfg.Root, ".deployment", "agent", "jobs", j.ID+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var disk p.Job
			if json.Unmarshal(b, &disk) != nil || disk.Status != want {
				t.Fatal("final status not persisted")
			}
			if fail && !strings.Contains(result.Message, "原服务健康") {
				t.Fatal("recovery status missing")
			}
		})
	}
}

func TestAdmissionGuards(t *testing.T) {
	a := testAgent(t)
	fakeDocker(t, a, nil, false, 1)
	req := rollbackRequest()
	if _, code, _ := a.enqueue(context.Background(), req); code != 409 {
		t.Fatal("incompatible rollback accepted")
	}
	req.ExpectedImage = previousID
	if _, code, _ := a.enqueue(context.Background(), req); code != 409 {
		t.Fatal("stale image accepted")
	}
	req = rollbackRequest()
	req.Action = "sh"
	if _, code, _ := a.enqueue(context.Background(), req); code != 400 {
		t.Fatal("arbitrary command accepted")
	}
	req = rollbackRequest()
	req.IdempotencyKey = "../../arbitrary-path"
	if _, code, _ := a.enqueue(context.Background(), req); code != 400 {
		t.Fatal("path key accepted")
	}
	req = rollbackRequest()
	lock := filepath.Join(a.cfg.Root, ".deployment", "operation.lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	if _, code, _ := a.enqueue(context.Background(), req); code != 409 {
		t.Fatal("shell lock ignored")
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("lock removed")
	}
}

func TestInterruptedJobAndInstanceLock(t *testing.T) {
	a := testAgent(t)
	if _, err := New(a.cfg); err == nil {
		t.Fatal("second agent acquired same directory")
	}
	j := &p.Job{ID: strings.Repeat("a", 32), Status: "running", CreatedAt: 1}
	if err := a.save(j); err != nil {
		t.Fatal(err)
	}
	// Stop this fixture before reopening the persisted task directory.
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	if reopened.last == nil || reopened.last.Status != "interrupted" || reopened.active {
		t.Fatal("interrupted task replayed or lost")
	}
}
