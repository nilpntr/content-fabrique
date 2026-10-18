package editor

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
)

// checkPlanAndChecks looks at the plan and the checks the run recorded for the run feed.
func checkPlanAndChecks(t *testing.T, x *e2e) {
	t.Helper()
	plan, ok := PlanFromEvents(context.Background(), x.st, x.wsA.ID, x.run.ID)
	if !ok {
		t.Fatal("the run recorded no creative_plan artifact")
	}
	if plan.ContentType != "workout" || plan.PlaybookVersion != "workout.v6" || plan.TempoBPM != 124 || plan.BeatMs != 484 || len(plan.Shots) != 6 || plan.PromptVersion != PromptVersion {
		t.Errorf("plan: %+v", plan)
	}
	if plan.Shots[0].LengthMs != 3*484 || plan.EstimatedLengthMs != 25*484 {
		t.Errorf("plan lengths: %+v", plan)
	}
	var nth []int
	for _, art := range runArtifacts(context.Background(), x.st, x.wsA.ID, x.run.ID, CheckArtifact) {
		var rec CheckRecord
		if err := json.Unmarshal(art.Data, &rec); err != nil {
			t.Fatal(err)
		}
		t.Logf("check %d of version %d: %d pass, %d fail, %d skip\n%s", rec.Nth, rec.Version, rec.Passed, rec.Failed, rec.Skipped, rec.Text())
		nth = append(nth, rec.Nth*10+rec.Version)
	}
	if !slices.Equal(nth, []int{11, 22}) {
		t.Errorf("checks (nth*10+version): %v, want the first on version 1 and the second on version 2", nth)
	}
}
