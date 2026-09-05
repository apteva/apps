package main

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestGradientMatchesFiniteDifference(t *testing.T) {
	n := newNetwork([]int{3, 2}, 42)
	points := dataset("xor", 101, 8)
	g := n.gradients(points)
	for l := range n.Weights {
		for j := range n.Weights[l] {
			for i := range n.Weights[l][j] {
				old := n.Weights[l][j][i]
				const eps = 1e-5
				n.Weights[l][j][i] = old + eps
				plus, _ := n.evaluate(points)
				n.Weights[l][j][i] = old - eps
				minus, _ := n.evaluate(points)
				n.Weights[l][j][i] = old
				numeric := (plus - minus) / (2 * eps)
				if math.Abs(numeric-g[l][j][i]) > 1e-6 {
					t.Fatalf("gradient %d/%d/%d got %g want %g", l, j, i, g[l][j][i], numeric)
				}
			}
		}
	}
}
func TestTrainingLearnsHeldOutData(t *testing.T) {
	for _, kind := range []string{"xor", "circles", "linear"} {
		t.Run(kind, func(t *testing.T) {
			s := newState(Config{Name: kind, Dataset: kind, Hidden: []int{6, 4}, LearningRate: .03, Epochs: 800, Seed: 42})
			initial := s.metric()
			if err := s.advance(800); err != nil {
				t.Fatal(err)
			}
			final := s.metric()
			t.Logf("loss %.4f → %.4f; held-out accuracy %.1f%%", initial.Loss, final.Loss, 100*final.ValidationAccuracy)
			if final.Loss > initial.Loss*.15 || final.ValidationAccuracy < .9 {
				t.Fatalf("did not generalize: %+v", final)
			}
		})
	}
}
func TestCheckpointResumesExactly(t *testing.T) {
	cfg := Config{Name: "resume", Dataset: "xor", Hidden: []int{6, 4}, LearningRate: .03, Epochs: 100, Seed: 7}
	uninterrupted := newState(cfg)
	_ = uninterrupted.advance(100)
	interrupted := newState(cfg)
	_ = interrupted.advance(35)
	raw, err := json.Marshal(interrupted)
	if err != nil {
		t.Fatal(err)
	}
	var resumed State
	if err = json.Unmarshal(raw, &resumed); err != nil {
		t.Fatal(err)
	}
	_ = resumed.advance(65)
	if !reflect.DeepEqual(uninterrupted, resumed) {
		t.Fatal("checkpoint failed to retain exact optimizer/network/history state")
	}
}
