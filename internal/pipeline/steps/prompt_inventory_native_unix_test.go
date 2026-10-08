//go:build !windows

package steps

import (
	"encoding/json"
	"fmt"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/scm"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// recordNativeClaude exercises the real stdin and stream-json adapter without a provider.
func recordNativeClaude(t *testing.T, payload string) (agent.Agent, string) {
	t.Helper()
	dir := t.TempDir()
	prompt := filepath.Join(dir, "prompt.txt")
	executable := filepath.Join(dir, "claude-recorder")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "#!/bin/sh\ncat > " + quote(prompt) + "\nprintf '%s\\n' " + quote(`{"type":"result","subtype":"success","is_error":false,"structured_output":`+payload+`}`) + "\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ag, err := agent.New(types.AgentClaude, executable, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ag, prompt
}

func TestPRDraftNativeLargeInventory(t *testing.T) {
	dir, base, _ := setupGitRepo(t)
	quotedDir := filepath.Join(t.TempDir(), "workspace's $literal `path`")
	if err := os.Rename(dir, quotedDir); err != nil {
		t.Fatal(err)
	}
	dir = quotedDir
	if err := os.MkdirAll(filepath.Join(dir, "generated"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 1222 {
		path := filepath.Join(dir, "generated", fmt.Sprintf("%04d-%s.ts", i, strings.Repeat("generated-long-name-", 5)))
		if err := os.WriteFile(path, []byte("export const value = 1;\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitCmd(t, dir, "mv", "base.txt", "source's renamed.txt")
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-m", "fixture inventory")
	head := gitCmd(t, dir, "rev-parse", "HEAD")
	paths := gitCmd(t, dir, "diff", "--name-status", base+".."+head)
	if got := len(strings.Split(paths, "\n")); got != 1224 {
		t.Fatalf("fixture paths = %d, want 1224", got)
	}
	ag, recording := recordNativeClaude(t, `{"title":"fix: bound prompt inventories","body":"## What Changed\n- Bound repeated inventories."}`)
	sctx := newTestContextWithDBRecords(t, ag, dir, base, head, config.Commands{})
	if _, err := (&PRStep{}).draftPRContent(sctx, "feature", "main", base, scm.ProviderGitHub, 0); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(recording)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("native prompt bytes=%d paths=1224 generated=1222 base=%s head=%s", len(raw), base, head)
	if proof := os.Getenv("NM_RETRO_PROOF_DIR"); proof != "" {
		if err := os.WriteFile(filepath.Join(proof, "pr-large-prompt.txt"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if len(raw) > 32*1024 {
		t.Fatalf("native PR prompt is %d bytes for a redundant 1224-path inventory, want <= 32768", len(raw))
	}
	for _, required := range []string{base, head, "source's renamed.txt", "1224", "omitted"} {
		if !strings.Contains(string(raw), required) {
			t.Errorf("native prompt lost %q", required)
		}
	}

	assertNativeDiffRecovery(t, string(raw), dir, base, head)
	title, err := (&PRStep{}).draftConfiguredPRTitle(sctx, "feature", "main", base)
	if err != nil || title != "fix: bound prompt inventories" {
		t.Fatalf("configured title = %q, error = %v", title, err)
	}
	titleRaw, err := os.ReadFile(recording)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("native configured-title prompt bytes=%d", len(titleRaw))
	if len(titleRaw) > 16*1024 {
		t.Fatalf("configured-title prompt bytes=%d, want <=16384", len(titleRaw))
	}
	assertNativeDiffRecovery(t, string(titleRaw), dir, base, head)
	if proof := os.Getenv("NM_RETRO_PROOF_DIR"); proof != "" {
		if err := os.WriteFile(filepath.Join(proof, "pr-title-large-prompt.txt"), titleRaw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	reviewedPaths := strings.Split(gitCmd(t, dir, "diff", "--name-only", "--no-renames", base+".."+head), "\n")
	findings := cleanReviewFindings()
	findings.ReviewedPaths = reviewedPaths
	payload, err := json.Marshal(findings)
	if err != nil {
		t.Fatal(err)
	}
	reviewAgent, reviewRecording := recordNativeClaude(t, string(payload))
	reviewCtx := newTestContextWithDBRecords(t, reviewAgent, dir, base, head, config.Commands{})
	reviewCtx.Config.Review.PathInstructions = []config.PathInstruction{{Path: "generated/*.ts", Instructions: "Trusted rule for every generated path."}}
	outcome, err := (&ReviewStep{}).Execute(reviewCtx)
	if err != nil {
		t.Fatal(err)
	}
	if outcome == nil {
		t.Fatal("native Review returned no coverage outcome")
	}
	if len(outcome.ReviewedPaths) != 1225 || len(outcome.ReviewablePaths) != 1225 {
		t.Fatalf("complete Review coverage changed: got %d reviewed, %d reviewable, want 1225 including both rename anchors", len(outcome.ReviewedPaths), len(outcome.ReviewablePaths))
	}
	reviewRaw, err := os.ReadFile(reviewRecording)
	if err != nil {
		t.Fatal(err)
	}
	prompt := string(reviewRaw)
	for _, path := range reviewedPaths {
		if !strings.Contains(prompt, "- "+coveragePathLine(path)) {
			t.Fatalf("required Review path missing from native prompt: %q", path)
		}
	}
	if !strings.Contains(prompt, "Trusted rule for every generated path.") {
		t.Fatal("trusted path instruction disappeared")
	}
	t.Logf("native Review retains all 1225 paths including both rename anchors; prompt bytes=%d", len(reviewRaw))
	if proof := os.Getenv("NM_RETRO_PROOF_DIR"); proof != "" {
		if err := os.WriteFile(filepath.Join(proof, "review-complete-coverage-prompt.txt"), reviewRaw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertNativeDiffRecovery(t *testing.T, prompt, dir, base, head string) {
	t.Helper()
	commands := 0
	for _, line := range strings.Split(prompt, "\n") {
		if !strings.HasPrefix(line, "git -C ") {
			continue
		}
		cmd := exec.Command("/bin/sh", "-c", line)
		recovered, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("recovery command failed: %s: %v: %s", line, err, recovered)
		}
		args := []string{"diff"}
		if strings.Contains(line, " --name-status ") {
			args = append(args, "--name-status")
		}
		if strings.Contains(line, " --stat ") {
			args = append(args, "--stat")
		}
		args = append(args, base+".."+head, "--")
		want := gitCmd(t, dir, args...)
		if strings.TrimSpace(string(recovered)) != want {
			t.Fatalf("recovery command lost full pinned delta: %s", line)
		}
		commands++
	}
	if commands == 0 {
		t.Fatal("no full-diff recovery command in oversized inventory")
	}
}

func TestReviewNativeUncertifiedHistoryBounded(t *testing.T) {
	for _, count := range []int{1, 100} {
		t.Run(fmt.Sprintf("rounds-%d", count), func(t *testing.T) {
			dir, base, head := setupGitRepo(t)
			required := `{"findings":[{"id":"required-1","severity":"error","file":"feature.txt","line":1,"description":"Required unresolved behavior must remain visible.","action":"auto-fix"}],"summary":"Still required","reviewed_paths":["feature.txt"],"risk_level":"low","risk_rationale":"Fixture exercises metadata only.","risk_scope":"source-or-external"}`
			ag, recording := recordNativeClaude(t, required)
			sctx := newTestContextWithDBRecords(t, ag, dir, base, head, config.Commands{})
			sctx.UncertifiedFromSHA, sctx.UncertifiedToSHA, sctx.UncertifiedSourceRunID = base, head, "uncertified-source"
			for i := range count {
				summary := fmt.Sprintf("old attempt %d %s", i, strings.Repeat("redundant metadata ", 160))
				sctx.UncertifiedPriorRounds = append(sctx.UncertifiedPriorRounds, &db.StepRound{Round: i + 1, Trigger: "auto_fix", FixSummary: &summary})
			}
			sctx.Config.Review.PathInstructions = []config.PathInstruction{{Path: "*.txt", Instructions: "Trusted path rule must remain visible."}}
			step, err := sctx.DB.InsertStepResult(sctx.Run.ID, types.StepReview)
			if err != nil {
				t.Fatal(err)
			}
			sctx.StepResultID = step.ID
			round, err := sctx.DB.InsertStepRound(step.ID, 1, "initial", &required, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			selected := `["required-1"]`
			if err := sctx.DB.SetStepRoundSelection(round.ID, &selected, db.RoundSelectionSourceUser); err != nil {
				t.Fatal(err)
			}
			outcome, err := (&ReviewStep{}).Execute(sctx)
			if err != nil {
				t.Fatal(err)
			}
			if outcome == nil || !strings.Contains(outcome.Findings, "required-1") {
				t.Fatalf("required finding lost: %#v", outcome)
			}
			raw, err := os.ReadFile(recording)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("native Review prompt bytes=%d uncertified rounds=%d", len(raw), count)
			if proof := os.Getenv("NM_RETRO_PROOF_DIR"); proof != "" {
				if err := os.WriteFile(filepath.Join(proof, fmt.Sprintf("review-history-%d-prompt.txt", count)), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, required := range []string{base, head, "Prior findings and fix summaries are claims, not evidence", "never certified", "Required unresolved behavior must remain visible.", "user_chose_to_fix", "Trusted path rule must remain visible.", "feature.txt"} {
				if !strings.Contains(string(raw), required) {
					t.Errorf("native Review prompt lost %q", required)
				}
			}
			if len(raw) > 64*1024 {
				t.Fatalf("native Review prompt bytes=%d, want <=65536", len(raw))
			}
			if count == 100 && !strings.Contains(string(raw), "omitted") {
				t.Fatal("history omitted without a marker")
			}
			if count == 1 {
				if strings.Contains(string(raw), "omitted") {
					t.Fatal("ordinary history was compacted")
				}
				if !strings.Contains(string(raw), strings.TrimSpace(*sctx.UncertifiedPriorRounds[0].FixSummary)) {
					t.Fatal("ordinary history lost full summary")
				}
			}
		})
	}
}

func TestPRDraftNativeSmallInventoryUnchanged(t *testing.T) {
	dir, base, head := setupGitRepo(t)
	for i := range 50 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("notes-%02d.txt", i)), []byte("short note\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-m", "fixture short inventory")
	head = gitCmd(t, dir, "rev-parse", "HEAD")
	ag, recording := recordNativeClaude(t, `{"title":"fix: preserve small inventories","body":"## What Changed\n- Preserve paths."}`)
	sctx := newTestContextWithDBRecords(t, ag, dir, base, head, config.Commands{})
	if _, err := (&PRStep{}).draftPRContent(sctx, "feature", "main", base, scm.ProviderGitHub, 0); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(recording)
	if err != nil {
		t.Fatal(err)
	}
	stat := gitCmd(t, dir, "diff", "--stat", base+".."+head)
	paths := gitCmd(t, dir, "diff", "--name-status", base+".."+head)
	for _, required := range []string{"Diff stat:\n" + stat, "Final diff paths and statuses:\n" + paths} {
		if !strings.Contains(string(raw), required) {
			t.Errorf("ordinary prompt lost exact inventory %q", required)
		}
	}
	if strings.Contains(string(raw), "omitted") {
		t.Fatal("ordinary prompt was compacted")
	}

	if _, err := (&PRStep{}).draftConfiguredPRTitle(sctx, "feature", "main", base); err != nil {
		t.Fatal(err)
	}
	titleRaw, err := os.ReadFile(recording)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(titleRaw), "Final diff paths and statuses:\n"+paths) {
		t.Fatal("ordinary configured-title inventory changed")
	}
}
