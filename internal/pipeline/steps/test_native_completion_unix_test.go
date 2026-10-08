//go:build !windows

package steps

import (
	"context"
	"fmt"
	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTestNativeEarlyResultChildCompletion(t *testing.T) {
	for _, mode := range []string{"success", "failure", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			dir, base, head := setupGitRepo(t)
			controls := t.TempDir()
			ready, release, completed := filepath.Join(controls, "ready"), filepath.Join(controls, "release"), filepath.Join(controls, "completed")
			executable := filepath.Join(controls, "claude-child")
			recording := filepath.Join(controls, "prompt.txt")
			cancelled := filepath.Join(controls, "cancelled")
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
			payload := `{"type":"result","subtype":"success","is_error":false,"structured_output":{"findings":[],"summary":"","tested":["required production check"],"testing_summary":"Production check passed.","artifacts":[],"scenarios":[{"name":"production build","result":"pass","live":true,"evidence":"child build receipt","reason":""}],"verdict":"go"}}`
			code := 0
			if mode == "failure" {
				code = 7
			}
			script := "#!/bin/sh\ncat > " + quote(recording) + "\ntrap " + quote("printf cancelled > "+quote(cancelled)+"; exit 143") + " TERM\n(while [ ! -e " + quote(release) + " ]; do sleep 0.02; done; printf '%s' completed > " + quote(completed) + fmt.Sprintf("; exit %d) &\nchild=$!\nprintf '%%s\\n' ", code) + quote(payload) + "\nprintf '%s' ready > " + quote(ready) + "\n"
			script += "wait \"$child\"\n"
			if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			ag, err := agent.New(types.AgentClaude, executable, nil)
			if err != nil {
				t.Fatal(err)
			}
			sctx := newTestContextWithDBRecords(t, ag, dir, base, head, config.Commands{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sctx.Ctx = ctx
			sctx.Config.TestAgentTimeout = 5 * time.Second
			if mode == "deadline" {
				sctx.Config.TestAgentTimeout = 300 * time.Millisecond
				sctx.Config.TestAgentWorkingTimeout = 300 * time.Millisecond
			}
			// Logging goes through the result channel, so no test method races a late worker.
			sctx.Log = func(string) {}
			type response struct {
				outcome *pipeline.StepOutcome
				err     error
			}
			result := make(chan response, 1)
			go func() { o, e := (&TestStep{}).Execute(sctx); result <- response{o, e} }()
			limit := time.NewTimer(5 * time.Second)
			defer limit.Stop()
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				select {
				case r := <-result:
					t.Fatalf("ended before the child started: %#v %v", r.outcome, r.err)
				case <-limit.C:
					t.Fatal("child readiness deadline")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if mode != "deadline" {
				select {
				case r := <-result:
					t.Fatalf("step returned before required child completion: outcome=%#v error=%v", r.outcome, r.err)
				case <-time.After(100 * time.Millisecond):
				}
				if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var got response
			select {
			case got = <-result:
			case <-time.After(10 * time.Second):
				cancel()
				t.Fatal("native step did not settle within the independent 10s outer guard")
			}
			t.Logf("mode=%s error=%v approval=%v", mode, got.err, got.outcome != nil && got.outcome.NeedsApproval)
			raw, err := os.ReadFile(recording)
			if err != nil {
				t.Fatal(err)
			}
			if proof := os.Getenv("NM_RETRO_PROOF_DIR"); proof != "" {
				if err := os.WriteFile(filepath.Join(proof, "test-"+mode+"-prompt.txt"), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, required := range []string{"Await every required production", "actual exit", "still running", "inconclusive"} {
				if !strings.Contains(string(raw), required) {
					t.Errorf("native Test instructions lost completion obligation %q", required)
				}
			}

			switch mode {
			case "success":
				if got.err != nil || got.outcome == nil || got.outcome.NeedsApproval {
					t.Fatalf("delayed successful child did not pass: %#v %v", got.outcome, got.err)
				}
				if _, err := os.Stat(completed); err != nil {
					t.Fatalf("missing child completion receipt: %v", err)
				}
			case "failure":
				if got.err == nil {
					t.Fatalf("child exit 7 passed: %#v", got.outcome)
				}
				if !strings.Contains(got.err.Error(), "exit status 7") {
					t.Fatalf("child failure was lost: %v", got.err)
				}
			case "deadline":
				if got.err != nil || got.outcome == nil || !got.outcome.NeedsApproval {
					t.Fatalf("deadline did not park: %#v %v", got.outcome, got.err)
				}
				if !strings.Contains(got.outcome.Findings, types.FindingIDTestAgentTimeout) {
					t.Fatalf("deadline lost ownership: %s", got.outcome.Findings)
				}
				findings, err := types.ParseFindingsJSON(got.outcome.Findings)
				if err != nil {
					t.Fatal(err)
				}
				if findings.Verdict == types.TestVerdictGo {
					t.Fatal("deadline retained the early go verdict")
				}
				if _, err := os.Stat(completed); err == nil {
					t.Fatal("blocked child completed past deadline")
				}
				if _, err := os.Stat(cancelled); err != nil {
					t.Fatalf("native command did not observe cancellation: %v", err)
				}
			}
		})
	}
}
