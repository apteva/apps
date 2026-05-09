package world

import "container/heap"

// aStarLength returns the length of the shortest 4-connected path from
// start to goal on g, or 0 when unreachable. Used at scenario-load to
// store the optimal-steps baseline that powers optimality_ratio.
func aStarLength(g *Grid, start, goal Pos) int {
	if !g.Walkable(start.X, start.Y) || !g.Walkable(goal.X, goal.Y) {
		return 0
	}
	if start == goal {
		return 0
	}

	open := &posQueue{}
	heap.Init(open)
	heap.Push(open, posEntry{pos: start, f: manhattan(start, goal)})

	gScore := map[Pos]int{start: 0}
	closed := map[Pos]bool{}

	for open.Len() > 0 {
		cur := heap.Pop(open).(posEntry)
		if cur.pos == goal {
			return gScore[cur.pos]
		}
		if closed[cur.pos] {
			continue
		}
		closed[cur.pos] = true

		for _, d := range []Direction{North, East, South, West} {
			dx, dy := d.Step()
			nx, ny := cur.pos.X+dx, cur.pos.Y+dy
			if !g.Walkable(nx, ny) {
				continue
			}
			n := Pos{nx, ny}
			if closed[n] {
				continue
			}
			tentative := gScore[cur.pos] + 1
			if best, ok := gScore[n]; ok && tentative >= best {
				continue
			}
			gScore[n] = tentative
			heap.Push(open, posEntry{pos: n, f: tentative + manhattan(n, goal)})
		}
	}
	return 0 // unreachable
}

func manhattan(a, b Pos) int {
	dx := a.X - b.X
	if dx < 0 {
		dx = -dx
	}
	dy := a.Y - b.Y
	if dy < 0 {
		dy = -dy
	}
	return dx + dy
}

type posEntry struct {
	pos Pos
	f   int
}

type posQueue []posEntry

func (q posQueue) Len() int            { return len(q) }
func (q posQueue) Less(i, j int) bool  { return q[i].f < q[j].f }
func (q posQueue) Swap(i, j int)       { q[i], q[j] = q[j], q[i] }
func (q *posQueue) Push(x any)         { *q = append(*q, x.(posEntry)) }
func (q *posQueue) Pop() any {
	old := *q
	n := len(old)
	v := old[n-1]
	*q = old[:n-1]
	return v
}
