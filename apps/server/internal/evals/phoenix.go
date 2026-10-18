package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Phoenix is a client of the REST API of the local Phoenix (compose profile obs): a suite is a dataset (one example per case, by the
// case's id), a suite run is an experiment, a case is an experiment run linked to its trace, and each score is an evaluation.
type Phoenix struct {
	// BaseURL is Phoenix's address, http://localhost:6006 locally.
	BaseURL string
	HTTP    *http.Client
}

// Example is one row of the dataset.
type Example struct {
	ID       string
	Input    map[string]any
	Output   map[string]any
	Metadata map[string]any
}

func (p *Phoenix) client() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (p *Phoenix) do(ctx context.Context, method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.BaseURL, "/")+path, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close() //nolint:errcheck // read below
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("phoenix %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw[:min(len(raw), 300)])))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("phoenix %s %s: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

// Reachable says Phoenix answers.
func (p *Phoenix) Reachable(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := p.do(ctx, http.MethodGet, "/healthz", nil, nil)
	return err == nil
}

// UpsertDataset makes the dataset of a suite, or updates it in place (examples matched by their ids), and returns its id and the
// Phoenix node id of each example, by its id.
func (p *Phoenix) UpsertDataset(ctx context.Context, name, description string, examples []Example) (string, map[string]string, error) {
	var found struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if _, err := p.do(ctx, http.MethodGet, "/v1/datasets?name="+url.QueryEscape(name), nil, &found); err != nil {
		return "", nil, err
	}
	action := "create"
	for _, d := range found.Data {
		if d.Name == name {
			action = "update"
		}
	}
	body := map[string]any{"action": action, "name": name, "description": description}
	var inputs, outputs, metadata []map[string]any
	var exampleIDs []string
	for _, e := range examples {
		inputs, outputs, metadata = append(inputs, nonNil(e.Input)), append(outputs, nonNil(e.Output)), append(metadata, nonNil(e.Metadata))
		exampleIDs = append(exampleIDs, e.ID)
	}
	body["inputs"], body["outputs"], body["metadata"], body["example_ids"] = inputs, outputs, metadata, exampleIDs
	var up struct {
		Data struct {
			DatasetID string `json:"dataset_id"`
		} `json:"data"`
	}
	if _, err := p.do(ctx, http.MethodPost, "/v1/datasets/upload?sync=true", body, &up); err != nil {
		return "", nil, err
	}
	var ex struct {
		Data struct {
			Examples []struct {
				ID     string `json:"id"`
				NodeID string `json:"node_id"`
			} `json:"examples"`
		} `json:"data"`
	}
	if _, err := p.do(ctx, http.MethodGet, "/v1/datasets/"+url.PathEscape(up.Data.DatasetID)+"/examples", nil, &ex); err != nil {
		return "", nil, err
	}
	nodes := map[string]string{}
	for _, e := range ex.Data.Examples {
		nodes[e.ID] = e.NodeID
	}
	return up.Data.DatasetID, nodes, nil
}

func nonNil(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// Experiment is a created experiment.
type Experiment struct {
	ID          string `json:"id"`
	ProjectName string `json:"project_name"`
	DatasetID   string `json:"dataset_id"`
}

// CreateExperiment starts an experiment over the dataset.
func (p *Phoenix) CreateExperiment(ctx context.Context, datasetID, name, description string, metadata map[string]any) (Experiment, error) {
	var out struct {
		Data Experiment `json:"data"`
	}
	_, err := p.do(ctx, http.MethodPost, "/v1/datasets/"+url.PathEscape(datasetID)+"/experiments",
		map[string]any{"name": name, "description": description, "metadata": nonNil(metadata)}, &out)
	return out.Data, err
}

// AddRun records one case's result; traceID links it to the case's trace (empty for none). It returns the run's id.
func (p *Phoenix) AddRun(ctx context.Context, experimentID, exampleNodeID string, output any, start, end time.Time, traceID, runErr string) (string, error) {
	body := map[string]any{
		"dataset_example_id": exampleNodeID, "output": output, "repetition_number": 1,
		"start_time": start.UTC().Format(time.RFC3339Nano), "end_time": end.UTC().Format(time.RFC3339Nano),
	}
	if traceID != "" {
		body["trace_id"] = traceID
	}
	if runErr != "" {
		body["error"] = runErr
	}
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_, err := p.do(ctx, http.MethodPost, "/v1/experiments/"+url.PathEscape(experimentID)+"/runs", body, &out)
	return out.Data.ID, err
}

// Annotator kinds of an evaluation.
const (
	AnnotatorLLM   = "LLM"
	AnnotatorCode  = "CODE"
	AnnotatorHuman = "HUMAN"
)

// Evaluation is one score of a run.
type Evaluation struct {
	Name, Kind, Label, Explanation string
	Score                          float64
}

// AddEvaluation records one score of a run.
func (p *Phoenix) AddEvaluation(ctx context.Context, runID string, e Evaluation, start, end time.Time) error {
	_, err := p.do(ctx, http.MethodPost, "/v1/experiment_evaluations", map[string]any{
		"experiment_run_id": runID, "name": e.Name, "annotator_kind": e.Kind,
		"start_time": start.UTC().Format(time.RFC3339Nano), "end_time": end.UTC().Format(time.RFC3339Nano),
		"result": map[string]any{"score": e.Score, "label": e.Label, "explanation": e.Explanation},
	}, nil)
	return err
}

// ProjectID is a Phoenix project's id by name ("" when there is none yet).
func (p *Phoenix) ProjectID(ctx context.Context, name string) string {
	var out struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if _, err := p.do(ctx, http.MethodGet, "/v1/projects", nil, &out); err != nil {
		return ""
	}
	for _, d := range out.Data {
		if d.Name == name {
			return d.ID
		}
	}
	return ""
}

// TraceURL is the page of a trace in a project ("" when the project is not known).
func (p *Phoenix) TraceURL(ctx context.Context, project, traceID string) string {
	id := p.ProjectID(ctx, project)
	if id == "" || traceID == "" {
		return ""
	}
	return strings.TrimRight(p.BaseURL, "/") + "/projects/" + id + "/traces/" + traceID
}

// ExperimentURL is the page of an experiment.
func (p *Phoenix) ExperimentURL(datasetID, experimentID string) string {
	return strings.TrimRight(p.BaseURL, "/") + "/datasets/" + datasetID + "/compare?experimentId=" + url.QueryEscape(experimentID)
}
