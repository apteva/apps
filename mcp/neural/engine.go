package main

import (
	"fmt"
	"math"
	"math/rand"
)

// Network is a small dense tanh network with a sigmoid output. Each row's
// last weight is its bias. Adam state is checkpointed with the weights.
type Network struct {
	Shape   []int         `json:"shape"`
	Weights [][][]float64 `json:"weights"`
	First   [][][]float64 `json:"first,omitempty"`
	Second  [][][]float64 `json:"second,omitempty"`
	Step    int           `json:"step"`
}
type Point struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Label int     `json:"label"`
}
type Metric struct {
	Epoch              int     `json:"epoch"`
	Loss               float64 `json:"loss"`
	ValidationLoss     float64 `json:"validation_loss"`
	Accuracy           float64 `json:"accuracy"`
	ValidationAccuracy float64 `json:"validation_accuracy"`
}
type Config struct {
	Name         string  `json:"name"`
	Dataset      string  `json:"dataset"`
	Hidden       []int   `json:"hidden"`
	LearningRate float64 `json:"learning_rate"`
	Epochs       int     `json:"epochs"`
	Seed         int64   `json:"seed"`
}
type State struct {
	Config  Config   `json:"config"`
	Network Network  `json:"network"`
	History []Metric `json:"history"`
	Epoch   int      `json:"epoch"`
	Error   string   `json:"error,omitempty"`
}

func validateConfig(c Config) error {
	if len(c.Name) < 1 || len(c.Name) > 100 {
		return fmt.Errorf("name must contain 1–100 bytes")
	}
	if c.Dataset != "xor" && c.Dataset != "circles" && c.Dataset != "linear" {
		return fmt.Errorf("dataset must be xor, circles, or linear")
	}
	if len(c.Hidden) < 1 || len(c.Hidden) > 2 {
		return fmt.Errorf("choose one or two hidden layers")
	}
	for _, n := range c.Hidden {
		if n < 2 || n > 12 {
			return fmt.Errorf("hidden layers must contain 2–12 neurons")
		}
	}
	if math.IsNaN(c.LearningRate) || math.IsInf(c.LearningRate, 0) || c.LearningRate < 0.0001 || c.LearningRate > 0.1 {
		return fmt.Errorf("learning_rate must be 0.0001–0.1")
	}
	if c.Epochs < 10 || c.Epochs > 2000 {
		return fmt.Errorf("epochs must be 10–2000")
	}
	if c.Seed < 0 || c.Seed > 2147483647 {
		return fmt.Errorf("seed must be 0–2147483647")
	}
	return nil
}
func matrixLike(w [][][]float64) [][][]float64 {
	out := make([][][]float64, len(w))
	for l := range w {
		out[l] = make([][]float64, len(w[l]))
		for j := range w[l] {
			out[l][j] = make([]float64, len(w[l][j]))
		}
	}
	return out
}
func newNetwork(hidden []int, seed int64) Network {
	shape := append([]int{2}, hidden...)
	shape = append(shape, 1)
	n := Network{Shape: shape, Weights: make([][][]float64, len(shape)-1)}
	rng := rand.New(rand.NewSource(seed))
	for l := range n.Weights {
		n.Weights[l] = make([][]float64, shape[l+1])
		scale := math.Sqrt(6 / float64(shape[l]+shape[l+1]))
		for j := range n.Weights[l] {
			n.Weights[l][j] = make([]float64, shape[l]+1)
			for i := 0; i < shape[l]; i++ {
				n.Weights[l][j][i] = (rng.Float64()*2 - 1) * scale
			}
		}
	}
	n.First = matrixLike(n.Weights)
	n.Second = matrixLike(n.Weights)
	return n
}
func sigmoid(x float64) float64 {
	if x >= 0 {
		return 1 / (1 + math.Exp(-x))
	}
	e := math.Exp(x)
	return e / (1 + e)
}
func (n *Network) forward(x, y float64) [][]float64 {
	a := make([][]float64, len(n.Shape))
	a[0] = []float64{x, y}
	for l, w := range n.Weights {
		a[l+1] = make([]float64, len(w))
		for j, row := range w {
			z := row[len(row)-1]
			for i, v := range a[l] {
				z += v * row[i]
			}
			if l == len(n.Weights)-1 {
				a[l+1][j] = sigmoid(z)
			} else {
				a[l+1][j] = math.Tanh(z)
			}
		}
	}
	return a
}
func dataset(kind string, seed int64, count int) []Point {
	rng := rand.New(rand.NewSource(seed))
	out := make([]Point, count)
	for i := range out {
		x, y := rng.Float64()*2-1, rng.Float64()*2-1
		label := 0
		switch kind {
		case "xor":
			if x*y > 0 {
				label = 1
			}
		case "circles":
			if x*x+y*y < 0.5 {
				label = 1
			}
		case "linear":
			if x+y > 0 {
				label = 1
			}
		}
		out[i] = Point{x, y, label}
	}
	return out
}
func (n *Network) gradients(points []Point) [][][]float64 {
	g := matrixLike(n.Weights)
	for _, p := range points {
		a := n.forward(p.X, p.Y)
		delta := []float64{a[len(a)-1][0] - float64(p.Label)}
		for l := len(n.Weights) - 1; l >= 0; l-- {
			prev := make([]float64, len(a[l]))
			for j, d := range delta {
				for i, v := range a[l] {
					g[l][j][i] += d * v
					prev[i] += n.Weights[l][j][i] * d
				}
				g[l][j][len(a[l])] += d
			}
			for i := range prev {
				prev[i] *= 1 - a[l][i]*a[l][i]
			}
			delta = prev
		}
	}
	for l := range g {
		for j := range g[l] {
			for i := range g[l][j] {
				g[l][j][i] /= float64(len(points))
			}
		}
	}
	return g
}
func (n *Network) train(points []Point, lr float64) {
	g := n.gradients(points)
	n.Step++
	b1 := 1 - math.Pow(0.9, float64(n.Step))
	b2 := 1 - math.Pow(0.999, float64(n.Step))
	for l := range g {
		for j := range g[l] {
			for i, v := range g[l][j] {
				m := 0.9*n.First[l][j][i] + 0.1*v
				s := 0.999*n.Second[l][j][i] + 0.001*v*v
				n.First[l][j][i] = m
				n.Second[l][j][i] = s
				n.Weights[l][j][i] -= lr * (m / b1) / (math.Sqrt(s/b2) + 1e-8)
			}
		}
	}
}
func (n *Network) evaluate(points []Point) (float64, float64) {
	loss, correct := 0.0, 0
	for _, p := range points {
		a := n.forward(p.X, p.Y)
		v := math.Max(1e-12, math.Min(1-1e-12, a[len(a)-1][0]))
		y := float64(p.Label)
		loss -= y*math.Log(v) + (1-y)*math.Log(1-v)
		if (v >= 0.5) == (p.Label == 1) {
			correct++
		}
	}
	return loss / float64(len(points)), float64(correct) / float64(len(points))
}
func (s *State) metric() Metric {
	loss, accuracy := s.Network.evaluate(dataset(s.Config.Dataset, s.Config.Seed+101, 192))
	vl, va := s.Network.evaluate(dataset(s.Config.Dataset, s.Config.Seed+202, 96))
	return Metric{s.Epoch, loss, vl, accuracy, va}
}
func newState(c Config) State {
	s := State{Config: c, Network: newNetwork(c.Hidden, c.Seed), History: []Metric{}}
	s.History = append(s.History, s.metric())
	return s
}
func (s *State) advance(epochs int) error {
	points := dataset(s.Config.Dataset, s.Config.Seed+101, 192)
	for i := 0; i < epochs && s.Epoch < s.Config.Epochs; i++ {
		s.Network.train(points, s.Config.LearningRate)
		s.Epoch++
		if s.Epoch%5 == 0 || s.Epoch == s.Config.Epochs {
			m := s.metric()
			if math.IsNaN(m.Loss) || math.IsInf(m.Loss, 0) {
				return fmt.Errorf("training diverged")
			}
			s.History = append(s.History, m)
		}
	}
	return nil
}
