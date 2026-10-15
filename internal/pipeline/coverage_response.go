package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func CanContinueReviewCoverage(step types.StepName, payload string) bool {
	return validateCoverageResponse(step, payload, nil, nil, nil, nil) == nil
}

func (e *Executor) validateCoverageBinding(bindings []*ipc.CoverageBinding) error {
	if len(bindings) != 1 || bindings[0] == nil {
		return fmt.Errorf("coverage continuation requires the observed head, step and round")
	}
	b := bindings[0]
	if b.HeadSHA == "" || b.StepResultID == "" || b.RoundID == "" || b.StepResultID != e.waitingStepResultID || b.RoundID != e.waitingRoundID {
		return fmt.Errorf("coverage continuation refers to a different or stale review gate")
	}
	step, err := e.db.GetStepResult(e.waitingStepResultID)
	if err != nil || step == nil {
		return fmt.Errorf("cannot verify the parked review step for coverage continuation")
	}
	run, err := e.db.GetRun(step.RunID)
	if err != nil || run == nil || run.Status != types.RunRunning || run.HeadSHA != b.HeadSHA {
		return fmt.Errorf("coverage continuation head does not match the current run head")
	}
	ctx, cancel := context.WithTimeout(e.runContext(context.Background()), 5*time.Second)
	defer cancel()
	head, err := git.HeadSHA(ctx, e.workDir)
	if err != nil || head != b.HeadSHA {
		return fmt.Errorf("coverage continuation worktree head does not match the observed run head")
	}
	dirty, err := git.Run(ctx, e.workDir, "status", "--porcelain")
	if err != nil || strings.TrimSpace(dirty) != "" {
		return fmt.Errorf("coverage continuation requires a clean worktree; existing work was preserved")
	}
	return nil
}

func validateCoverageResponse(step types.StepName, payload string, findingIDs, ignoredIDs []string, instructions map[string]string, added []types.Finding) error {
	if step != types.StepReview {
		return fmt.Errorf("complete-coverage applies only to a parked Review")
	}
	if len(findingIDs)+len(ignoredIDs)+len(instructions)+len(added) != 0 {
		return fmt.Errorf("complete-coverage cannot select, ignore, add or change findings")
	}
	var gate struct {
		Findings json.RawMessage `json:"findings"`
	}
	if err := json.Unmarshal([]byte(payload), &gate); err != nil {
		return fmt.Errorf("read parked review before coverage continuation: %w", err)
	}
	if len(gate.Findings) == 0 || strings.TrimSpace(string(gate.Findings)) == "null" {
		return fmt.Errorf("coverage continuation requires an explicit empty findings array")
	}
	var findings []types.Finding
	if err := json.Unmarshal(gate.Findings, &findings); err != nil {
		return fmt.Errorf("read parked review findings before coverage continuation: %w", err)
	}
	if len(findings) != 0 {
		return fmt.Errorf("coverage continuation cannot resolve outstanding review findings")
	}
	return nil
}
