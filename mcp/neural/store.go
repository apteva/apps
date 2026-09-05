package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Experiment struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	State     State  `json:"state"`
	UpdatedAt string `json:"updated_at"`
}
type Version struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	ExperimentID int64  `json:"experiment_id"`
	Epoch        int    `json:"epoch"`
	Metric       Metric `json:"metric"`
}
type Deployment struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	VersionID int64  `json:"version_id"`
	Endpoint  string `json:"endpoint"`
}

func (a *App) get(project string, id int64) (*Experiment, error) {
	var e Experiment
	var raw string
	err := a.db.QueryRow(`SELECT id,name,status,state_json,updated_at FROM experiments WHERE project_id=? AND id=?`, project, id).Scan(&e.ID, &e.Name, &e.Status, &raw, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal([]byte(raw), &e.State)
	return &e, err
}
func (a *App) save(project string, e *Experiment) error {
	raw, err := json.Marshal(e.State)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(`UPDATE experiments SET status=?,state_json=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE project_id=? AND id=?`, e.Status, string(raw), project, e.ID)
	return err
}

// Public snapshots omit optimizer tensors. The persisted copy always retains
// them so pause, restart, and step all use the same Adam state.
func publicExperiment(e *Experiment) *Experiment {
	cp := *e
	cp.State.Network.First = nil
	cp.State.Network.Second = nil
	return &cp
}
func idArg(args map[string]any, key string) (int64, error) {
	v, ok := args[key].(float64)
	if !ok || v < 1 || v > 9007199254740991 || float64(int64(v)) != v {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return int64(v), nil
}
func decode(args map[string]any, out any) error {
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
func (a *App) perform(project, tool string, args map[string]any) (any, error) {
	if strings.TrimSpace(project) == "" {
		return nil, fmt.Errorf("project context is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch tool {
	case "experiments_list":
		rows, err := a.db.Query(`SELECT id,name,status,state_json,updated_at FROM experiments WHERE project_id=? ORDER BY id DESC LIMIT 100`, project)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []any{}
		for rows.Next() {
			var e Experiment
			var raw string
			if err = rows.Scan(&e.ID, &e.Name, &e.Status, &raw, &e.UpdatedAt); err != nil {
				return nil, err
			}
			if err = json.Unmarshal([]byte(raw), &e.State); err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"id": e.ID, "name": e.Name, "status": e.Status, "epoch": e.State.Epoch, "dataset": e.State.Config.Dataset})
		}
		return map[string]any{"experiments": out}, rows.Err()
	case "experiments_create":
		c := Config{Name: "First network", Dataset: "xor", Hidden: []int{6, 4}, LearningRate: 0.03, Epochs: 800, Seed: 42}
		if err := decode(args, &c); err != nil {
			return nil, err
		}
		c.Name = strings.TrimSpace(c.Name)
		if err := validateConfig(c); err != nil {
			return nil, err
		}
		var count int
		if err := a.db.QueryRow(`SELECT count(*) FROM experiments WHERE project_id=?`, project).Scan(&count); err != nil {
			return nil, err
		}
		if count >= 100 {
			return nil, fmt.Errorf("v0.1 supports at most 100 experiments per project")
		}
		s := newState(c)
		raw, err := json.Marshal(s)
		if err != nil {
			return nil, err
		}
		res, err := a.db.Exec(`INSERT INTO experiments(project_id,name,status,state_json) VALUES(?,?,'paused',?)`, project, c.Name, string(raw))
		if err != nil {
			return nil, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		e, err := a.get(project, id)
		if err != nil {
			return nil, err
		}
		a.emit(project, "neural.experiment.created", id)
		return map[string]any{"experiment": publicExperiment(e)}, nil
	case "experiments_get", "experiments_control":
		id, err := idArg(args, "id")
		if err != nil {
			return nil, err
		}
		e, err := a.get(project, id)
		if err != nil {
			return nil, err
		}
		if tool == "experiments_control" {
			action, _ := args["action"].(string)
			switch action {
			case "start":
				if e.Status == "failed" {
					return nil, fmt.Errorf("create a new experiment after a training failure")
				}
				if e.State.Epoch >= e.State.Config.Epochs {
					return nil, fmt.Errorf("training is complete; create a new experiment to train again")
				}
				e.Status = "running"
			case "pause":
				if e.Status == "running" {
					e.Status = "paused"
				}
			case "step":
				if e.Status != "paused" {
					return nil, fmt.Errorf("pause before stepping")
				}
				if err = e.State.advance(1); err != nil {
					return nil, err
				}
				if e.State.Epoch >= e.State.Config.Epochs {
					e.Status = "completed"
				}
			default:
				return nil, fmt.Errorf("action must be start, pause, or step")
			}
			if err = a.save(project, e); err != nil {
				return nil, err
			}
			a.emit(project, "neural.experiment.updated", id)
		}
		return map[string]any{"experiment": publicExperiment(e), "points": dataset(e.State.Config.Dataset, e.State.Config.Seed+101, 192), "validation_points": dataset(e.State.Config.Dataset, e.State.Config.Seed+202, 96), "metric": e.State.metric()}, nil
	case "model_versions_create":
		id, err := idArg(args, "experiment_id")
		if err != nil {
			return nil, err
		}
		e, err := a.get(project, id)
		if err != nil {
			return nil, err
		}
		if e.State.Epoch == 0 {
			return nil, fmt.Errorf("train before saving a version")
		}
		if e.Status == "failed" {
			return nil, fmt.Errorf("cannot save a failed experiment")
		}
		raw, err := json.Marshal(publicExperiment(e).State)
		if err != nil {
			return nil, err
		}
		res, err := a.db.Exec(`INSERT INTO model_versions(project_id,experiment_id,name,state_json) VALUES(?,?,?,?)`, project, id, e.Name, string(raw))
		if err != nil {
			return nil, err
		}
		vid, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		a.emit(project, "neural.model.version.created", vid)
		return map[string]any{"version": Version{vid, e.Name, id, e.State.Epoch, e.State.metric()}}, nil
	case "model_versions_list":
		rows, err := a.db.Query(`SELECT id,name,experiment_id,state_json FROM model_versions WHERE project_id=? ORDER BY id DESC LIMIT 100`, project)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []Version{}
		for rows.Next() {
			var v Version
			var raw string
			var s State
			if err = rows.Scan(&v.ID, &v.Name, &v.ExperimentID, &raw); err != nil {
				return nil, err
			}
			if err = json.Unmarshal([]byte(raw), &s); err != nil {
				return nil, err
			}
			v.Epoch = s.Epoch
			v.Metric = s.metric()
			out = append(out, v)
		}
		return map[string]any{"versions": out}, rows.Err()
	case "deployments_create":
		vid, err := idArg(args, "version_id")
		if err != nil {
			return nil, err
		}
		var name string
		if err = a.db.QueryRow(`SELECT name FROM model_versions WHERE project_id=? AND id=?`, project, vid).Scan(&name); err != nil {
			return nil, err
		}
		res, err := a.db.Exec(`INSERT INTO deployments(project_id,version_id,name) VALUES(?,?,?)`, project, vid, name)
		if err != nil {
			return nil, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		a.emit(project, "neural.deployment.ready", id)
		return map[string]any{"deployment": Deployment{id, name, vid, fmt.Sprintf("/api/apps/neural/deployments/%d/predict", id)}}, nil
	case "deployments_list":
		rows, err := a.db.Query(`SELECT id,name,version_id FROM deployments WHERE project_id=? ORDER BY id DESC LIMIT 100`, project)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []Deployment{}
		for rows.Next() {
			var d Deployment
			if err = rows.Scan(&d.ID, &d.Name, &d.VersionID); err != nil {
				return nil, err
			}
			d.Endpoint = fmt.Sprintf("/api/apps/neural/deployments/%d/predict", d.ID)
			out = append(out, d)
		}
		return map[string]any{"deployments": out}, rows.Err()
	case "predictions_create":
		_, hasExperiment := args["experiment_id"]
		_, hasDeployment := args["deployment_id"]
		if hasExperiment == hasDeployment {
			return nil, fmt.Errorf("provide exactly one of experiment_id or deployment_id")
		}
		x, xok := args["x"].(float64)
		y, yok := args["y"].(float64)
		if !xok || !yok || x < -1 || x > 1 || y < -1 || y > 1 {
			return nil, fmt.Errorf("x and y must be numbers between -1 and 1")
		}
		var s State
		var versionID int64
		if _, ok := args["deployment_id"]; ok {
			id, err := idArg(args, "deployment_id")
			if err != nil {
				return nil, err
			}
			var raw string
			err = a.db.QueryRow(`SELECT v.id,v.state_json FROM deployments d JOIN model_versions v ON v.id=d.version_id AND v.project_id=d.project_id WHERE d.id=? AND d.project_id=?`, id, project).Scan(&versionID, &raw)
			if err != nil {
				return nil, err
			}
			if err = json.Unmarshal([]byte(raw), &s); err != nil {
				return nil, err
			}
		} else {
			id, err := idArg(args, "experiment_id")
			if err != nil {
				return nil, err
			}
			e, err := a.get(project, id)
			if err != nil {
				return nil, err
			}
			s = e.State
		}
		activations := s.Network.forward(x, y)
		p := activations[len(activations)-1][0]
		label := 0
		if p >= 0.5 {
			label = 1
		}
		return map[string]any{"probability": p, "class": label, "activations": activations, "epoch": s.Epoch, "version_id": versionID}, nil
	default:
		return nil, fmt.Errorf("unknown tool %q", tool)
	}
}
func (a *App) tick(project string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	rows, err := a.db.Query(`SELECT id FROM experiments WHERE project_id=? AND status='running' ORDER BY updated_at,id LIMIT 4`, project)
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		e, err := a.get(project, id)
		if err != nil {
			return err
		}
		if err = e.State.advance(5); err != nil {
			e.State.Error = err.Error()
			e.Status = "failed"
		}
		if e.State.Epoch >= e.State.Config.Epochs {
			e.Status = "completed"
		}
		if err = a.save(project, e); err != nil {
			return err
		}
		if e.Status == "completed" {
			a.emit(project, "neural.experiment.completed", id)
		}
	}
	return nil
}
func httpStatus(err error) int {
	if errors.Is(err, sql.ErrNoRows) {
		return 404
	}
	return 400
}
