// Package world holds the static side of a navigation scenario:
// cells, walls, scenario JSON, A* baseline, and the partial-obs view
// the agent sees through observe().
package world

import "fmt"

// Cell is a categorical label for what's in a grid square. Empty
// strings are treated as floor by the loader.
type Cell string

const (
	CellFloor  Cell = "floor"
	CellWall   Cell = "wall"
	CellGoal   Cell = "goal"
	CellAgent  Cell = "agent"
	CellItem   Cell = "item"   // v0.2+
	CellHazard Cell = "hazard" // v0.2+
	CellFog    Cell = "fog"    // outside the agent's view radius
	CellOOB    Cell = "oob"    // outside the grid bounds
)

// Direction is a cardinal heading and a movement vector.
type Direction string

const (
	North Direction = "N"
	East  Direction = "E"
	South Direction = "S"
	West  Direction = "W"
)

func (d Direction) Step() (dx, dy int) {
	switch d {
	case North:
		return 0, -1
	case South:
		return 0, 1
	case East:
		return 1, 0
	case West:
		return -1, 0
	}
	return 0, 0
}

// Pos is a grid coordinate. Origin (0,0) is top-left; +y is down.
type Pos struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Pose is a position plus a facing direction. Heading is informational
// in v0.1 — `move` is cardinal, not relative — but ships in the
// contract so v0.3's continuous-pose physics doesn't need a re-version.
type Pose struct {
	X       int       `json:"x"`
	Y       int       `json:"y"`
	Heading Direction `json:"heading"`
}

// Grid is rectangular dimensions plus a wall set. Walls are stored as
// an x,y → bool map for O(1) lookup; the JSON form is a list of [x,y]
// pairs for terseness.
type Grid struct {
	Width  int  `json:"width"`
	Height int  `json:"height"`
	walls  map[Pos]bool
}

func (g *Grid) IsWall(x, y int) bool { return g.walls[Pos{x, y}] }

func (g *Grid) InBounds(x, y int) bool {
	return x >= 0 && x < g.Width && y >= 0 && y < g.Height
}

// Walkable: in-bounds and not a wall. Hazards become non-walkable in v0.2.
func (g *Grid) Walkable(x, y int) bool {
	return g.InBounds(x, y) && !g.IsWall(x, y)
}

// Observability is per-scenario.
//
// kind=full: the agent's view is the whole grid every observe().
// kind=partial: the agent sees a (2*radius+1) square centred on its
//	position; cells outside that square come back as CellFog.
type Observability struct {
	Kind   string `json:"kind"`             // "full" | "partial"
	Radius int    `json:"radius,omitempty"` // partial only; default 2
}

func (o Observability) effectiveRadius() int {
	if o.Radius <= 0 {
		return 2
	}
	return o.Radius
}

// SuccessSpec — only reach_goal in v0.1.
type SuccessSpec struct {
	Type string `json:"type"`
}

// Scenario is the loaded form of a scenarios/*.json file. Walls are
// kept both as the JSON-friendly slice (Walls) and as the runtime
// lookup map (Grid.walls), populated by Finalize.
type Scenario struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Description   string        `json:"description"`
	Grid          Grid          `json:"grid"`
	Walls         [][2]int      `json:"walls"`
	AgentStart    Pose          `json:"agent_start"`
	Goal          [2]int        `json:"goal"`
	MaxSteps      int           `json:"max_steps"`
	Observability Observability `json:"observability"`
	Success       SuccessSpec   `json:"success"`

	OptimalSteps int `json:"-"` // computed at load via A*; 0 = unreachable
}

// Finalize fills derived state after JSON unmarshal: walls map,
// validation, A* baseline. Returns a clear error per scenario.
func (s *Scenario) Finalize() error {
	if s.ID == "" {
		return fmt.Errorf("scenario: id required")
	}
	if s.Grid.Width <= 0 || s.Grid.Height <= 0 {
		return fmt.Errorf("scenario %s: grid width/height must be positive", s.ID)
	}
	if s.MaxSteps <= 0 {
		return fmt.Errorf("scenario %s: max_steps must be positive", s.ID)
	}
	if s.Observability.Kind == "" {
		s.Observability.Kind = "partial"
	}
	if s.Success.Type == "" {
		s.Success.Type = "reach_goal"
	}
	if s.Success.Type != "reach_goal" {
		return fmt.Errorf("scenario %s: v0.1 supports success.type=reach_goal only (got %q)", s.ID, s.Success.Type)
	}
	if s.AgentStart.Heading == "" {
		s.AgentStart.Heading = North
	}

	s.Grid.walls = make(map[Pos]bool, len(s.Walls))
	for _, w := range s.Walls {
		x, y := w[0], w[1]
		if !s.Grid.InBounds(x, y) {
			return fmt.Errorf("scenario %s: wall (%d,%d) out of bounds", s.ID, x, y)
		}
		s.Grid.walls[Pos{x, y}] = true
	}

	if !s.Grid.InBounds(s.AgentStart.X, s.AgentStart.Y) {
		return fmt.Errorf("scenario %s: agent_start out of bounds", s.ID)
	}
	if s.Grid.IsWall(s.AgentStart.X, s.AgentStart.Y) {
		return fmt.Errorf("scenario %s: agent_start sits on a wall", s.ID)
	}
	if !s.Grid.InBounds(s.Goal[0], s.Goal[1]) {
		return fmt.Errorf("scenario %s: goal out of bounds", s.ID)
	}
	if s.Grid.IsWall(s.Goal[0], s.Goal[1]) {
		return fmt.Errorf("scenario %s: goal sits on a wall", s.ID)
	}

	s.OptimalSteps = aStarLength(&s.Grid,
		Pos{s.AgentStart.X, s.AgentStart.Y},
		Pos{s.Goal[0], s.Goal[1]},
	)
	return nil
}

// View is what observe() returns. Cells is row-major, indexed [y][x]
// in view-local coordinates; ViewOrigin maps view (0,0) to a world
// position. In partial mode the dims are (2r+1)×(2r+1); in full mode
// they're the grid dims. GoalBearing is the agent's compass direction
// to the goal (or empty when the goal is on the agent's cell).
type View struct {
	Cells       [][]Cell `json:"view"`
	ViewOrigin  Pos      `json:"view_origin"`
	GoalBearing string   `json:"goal_bearing,omitempty"`
	Holding     string   `json:"holding,omitempty"`
	Step        int      `json:"step"`
	Status      string   `json:"status"` // "idle" | "done"
}

// BuildView assembles the view the agent sees from `at` in the world.
// goal is the scenario's goal; pass nil if the scenario has no goal.
func BuildView(g *Grid, at Pos, goal *Pos, obs Observability) View {
	if obs.Kind == "full" {
		cells := make([][]Cell, g.Height)
		for y := 0; y < g.Height; y++ {
			row := make([]Cell, g.Width)
			for x := 0; x < g.Width; x++ {
				row[x] = baseCell(g, x, y, goal, at)
			}
			cells[y] = row
		}
		v := View{Cells: cells, ViewOrigin: Pos{0, 0}}
		if goal != nil {
			v.GoalBearing = bearing(at, *goal)
		}
		return v
	}
	r := obs.effectiveRadius()
	size := 2*r + 1
	cells := make([][]Cell, size)
	for dy := -r; dy <= r; dy++ {
		row := make([]Cell, size)
		for dx := -r; dx <= r; dx++ {
			x, y := at.X+dx, at.Y+dy
			if !g.InBounds(x, y) {
				row[dx+r] = CellOOB
				continue
			}
			row[dx+r] = baseCell(g, x, y, goal, at)
		}
		cells[dy+r] = row
	}
	v := View{Cells: cells, ViewOrigin: Pos{at.X - r, at.Y - r}}
	if goal != nil {
		v.GoalBearing = bearing(at, *goal)
	}
	return v
}

// baseCell — what's at (x,y) in the world, ignoring fog. Goal wins
// over walls when both happen to be declared (loader rejects that
// case anyway). Agent overlay only renders if (x,y) == at.
func baseCell(g *Grid, x, y int, goal *Pos, at Pos) Cell {
	if at.X == x && at.Y == y {
		return CellAgent
	}
	if goal != nil && goal.X == x && goal.Y == y {
		return CellGoal
	}
	if g.IsWall(x, y) {
		return CellWall
	}
	return CellFloor
}

// bearing returns the 8-way compass direction from `from` to `to`.
// Empty when the two coincide.
func bearing(from, to Pos) string {
	dx, dy := to.X-from.X, to.Y-from.Y
	if dx == 0 && dy == 0 {
		return ""
	}
	// Normalise to one of {-1,0,1} on each axis. The agent's a
	// quantised navigator — finer angles aren't useful in a grid.
	sx := sign(dx)
	sy := sign(dy)
	switch {
	case sx == 0 && sy < 0:
		return "N"
	case sx == 0 && sy > 0:
		return "S"
	case sx > 0 && sy == 0:
		return "E"
	case sx < 0 && sy == 0:
		return "W"
	case sx > 0 && sy < 0:
		return "NE"
	case sx > 0 && sy > 0:
		return "SE"
	case sx < 0 && sy < 0:
		return "NW"
	case sx < 0 && sy > 0:
		return "SW"
	}
	return ""
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}
