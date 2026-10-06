package engine

import (
	"fmt"
	"math"
)

// Example meshes use stable, named parts and ordinary editable polygon topology.
// Colors are linear RGB, like all documents in the mesh kernel.
type exampleBuilder struct{ doc Document }
type profileRing struct{ y, x, z float64 }

func newExample() *exampleBuilder { return &exampleBuilder{doc: Empty()} }
func (b *exampleBuilder) add(id, name string, color Vec, m Mesh) {
	b.doc.Nodes = append(b.doc.Nodes, Node{ID: id, Name: name, Color: color, Mesh: m})
}
func (b *exampleBuilder) finish() (Document, error) { _, err := Validate(b.doc); return b.doc, err }

// loft joins elliptical rings and closes both ends. Even tiny tips retain a
// finite cap so selecting and extruding them never starts from a zero-area face.
func loft(rings []profileRing, segments int, phase float64) Mesh {
	m := Mesh{}
	ids := make([][]string, len(rings))
	for j, r := range rings {
		ids[j] = make([]string, segments)
		for i := 0; i < segments; i++ {
			a := phase + 2*math.Pi*float64(i)/float64(segments)
			ids[j][i] = m.vertex(Vec{r.x * math.Cos(a), r.y, r.z * math.Sin(a)})
		}
	}
	m.face(append([]string(nil), ids[0]...))
	for j := 0; j < len(rings)-1; j++ {
		for i := 0; i < segments; i++ {
			k := (i + 1) % segments
			m.face([]string{ids[j][i], ids[j+1][i], ids[j+1][k], ids[j][k]})
		}
	}
	top := append([]string(nil), ids[len(ids)-1]...)
	for i, j := 0, len(top)-1; i < j; i, j = i+1, j-1 {
		top[i], top[j] = top[j], top[i]
	}
	m.face(top)
	return m
}
func moveMesh(m Mesh, p Vec) Mesh {
	for i := range m.Vertices {
		m.Vertices[i].Position = m.Vertices[i].Position.Add(p)
	}
	return m
}
func (b *exampleBuilder) profile(id, name string, p Vec, rings []profileRing, segments int, color Vec) {
	b.add(id, name, color, moveMesh(loft(rings, segments, 0), p))
}
func (b *exampleBuilder) box(id, name string, p, size, color Vec) {
	// Four ring corners, with a 45-degree phase, produce planar rectangular faces.
	q := math.Sqrt2 / 2
	m := loft([]profileRing{{-size[1] / 2, size[0] / (2 * q), size[2] / (2 * q)}, {size[1] / 2, size[0] / (2 * q), size[2] / (2 * q)}}, 4, math.Pi/4)
	b.add(id, name, color, moveMesh(m, p))
}
func (b *exampleBuilder) limb(id, name string, a, c Vec, r1, r2 float64, color Vec) {
	axis := c.Sub(a)
	length := axis.Len()
	y := axis.Unit()
	ref := Vec{0, 0, 1}
	if math.Abs(y.Dot(ref)) > .9 {
		ref = Vec{1, 0, 0}
	}
	x := y.Cross(ref).Unit()
	z := x.Cross(y)
	m := loft([]profileRing{{0, r1, r1}, {length * .18, r1 * 1.08, r1 * 1.08}, {length * .8, r2 * 1.06, r2 * 1.06}, {length, r2, r2}}, 8, math.Pi/8)
	for i := range m.Vertices {
		p := m.Vertices[i].Position
		m.Vertices[i].Position = a.Add(x.Mul(p[0])).Add(y.Mul(p[1])).Add(z.Mul(p[2]))
	}
	b.add(id, name, color, m)
}

var steel = Vec{.29, .36, .43}
var brightSteel = Vec{.58, .67, .73}
var gold = Vec{.65, .34, .07}
var leather = Vec{.075, .028, .012}
var cloth = Vec{.28, .014, .025}

// swordParts shares the same detailed model between the standalone asset and
// the warrior's equipment. origin is the pommel; the blade points along +Y.
func swordParts(b *exampleBuilder, prefix string, origin Vec, scale float64) {
	start := len(b.doc.Nodes)
	b.profile(prefix+"blade", "Blade · diamond cross section", Vec{}, []profileRing{{.39, .075, .022}, {.48, .078, .022}, {1.20, .052, .016}, {1.43, .004, .004}}, 4, brightSteel)
	b.profile(prefix+"ricasso", "Blade collar", Vec{}, []profileRing{{.32, .075, .028}, {.40, .075, .028}}, 4, steel)
	b.box(prefix+"fuller", "Central blade inlay", Vec{0, .78, .022}, Vec{.013, .68, .006}, Vec{.12, .18, .23})
	b.box(prefix+"guard", "Crossguard", Vec{0, .33, 0}, Vec{.43, .055, .075}, gold)
	for _, sign := range []float64{-1, 1} {
		side := "left"
		if sign > 0 {
			side = "right"
		}
		b.limb(prefix+"quillon_"+side, "Swept guard · "+side, Vec{sign * .18, .33, 0}, Vec{sign * .245, .27, 0}, .035, .025, gold)
		b.profile(prefix+"guard_tip_"+side, "Guard end · "+side, Vec{sign * .245, .25, 0}, []profileRing{{0, .026, .026}, {.04, .026, .026}}, 8, brightSteel)
	}
	b.profile(prefix+"grip", "Leather grip", Vec{}, []profileRing{{.075, .032, .027}, {.13, .029, .024}, {.28, .032, .027}}, 8, leather)
	for i := 0; i < 8; i++ {
		b.profile(fmt.Sprintf("%sgrip_wrap_%02d", prefix, i), "Grip binding", Vec{0, .085 + float64(i)*.024, 0}, []profileRing{{0, .034, .029}, {.009, .034, .029}}, 8, Vec{.18, .07, .025})
	}
	b.profile(prefix+"pommel", "Faceted pommel", Vec{}, []profileRing{{0, .025, .025}, {.022, .05, .035}, {.07, .043, .033}, {.085, .025, .025}}, 8, gold)
	b.box(prefix+"gem", "Pommel inset", Vec{0, .044, .035}, Vec{.032, .034, .008}, Vec{.012, .23, .18})
	for i := start; i < len(b.doc.Nodes); i++ {
		for j := range b.doc.Nodes[i].Mesh.Vertices {
			v := &b.doc.Nodes[i].Mesh.Vertices[j]
			v.Position = v.Position.Mul(scale).Add(origin)
		}
	}
}
func ExampleSword() (Document, error) {
	b := newExample()
	swordParts(b, "", Vec{}, 1)
	return b.finish()
}

// ExampleWarrior is an unrigged, layered low-poly character, facing +Z.
func ExampleWarrior() (Document, error) {
	b := newExample()
	b.profile("torso", "Breastplate", Vec{0, 1.04, 0}, []profileRing{{0, .21, .13}, {.14, .27, .16}, {.34, .31, .17}, {.43, .22, .13}}, 8, steel)
	b.profile("waist", "Leather waist", Vec{0, .90, 0}, []profileRing{{0, .22, .14}, {.17, .23, .15}}, 8, leather)
	b.profile("belt", "Belt", Vec{0, 1.01, 0}, []profileRing{{0, .24, .16}, {.065, .24, .16}}, 8, Vec{.12, .055, .02})
	b.box("buckle", "Belt buckle", Vec{0, 1.04, .16}, Vec{.105, .075, .025}, gold)
	b.box("buckle_inset", "Buckle inset", Vec{0, 1.04, .177}, Vec{.06, .035, .012}, leather)
	b.profile("neck", "Gorget", Vec{0, 1.45, 0}, []profileRing{{0, .145, .12}, {.1, .12, .11}}, 10, gold)
	b.profile("head", "Face", Vec{0, 1.51, .008}, []profileRing{{0, .095, .09}, {.065, .13, .12}, {.23, .13, .12}, {.28, .10, .085}}, 8, Vec{.52, .29, .16})
	b.profile("helmet", "Helmet", Vec{0, 1.70, 0}, []profileRing{{0, .153, .14}, {.12, .142, .13}, {.20, .085, .09}, {.215, .035, .035}}, 10, steel)
	b.profile("helmet_rim", "Helmet brow rim", Vec{0, 1.70, 0}, []profileRing{{0, .159, .145}, {.026, .159, .145}}, 10, gold)
	b.box("nose_guard", "Nose guard", Vec{0, 1.665, .14}, Vec{.028, .14, .023}, gold)
	for _, sign := range []float64{-1, 1} {
		side := "left"
		if sign > 0 {
			side = "right"
		}
		b.box("eye_"+side, "Eye · "+side, Vec{sign * .065, 1.684, .118}, Vec{.042, .012, .018}, Vec{.018, .024, .03})
		b.box("cheek_guard_"+side, "Cheek plate · "+side, Vec{sign * .12, 1.61, .09}, Vec{.045, .135, .055}, steel)
		b.limb("thigh_"+side, "Cuisses · "+side, Vec{sign * .13, .92, 0}, Vec{sign * .18, .56, .01}, .12, .10, steel)
		b.profile("knee_"+side, "Knee guard · "+side, Vec{sign * .18, .49, .042}, []profileRing{{0, .105, .10}, {.06, .125, .12}, {.12, .095, .095}}, 8, gold)
		b.limb("shin_"+side, "Greave · "+side, Vec{sign * .18, .48, 0}, Vec{sign * .19, .16, .025}, .095, .08, steel)
		b.profile("boot_"+side, "Boot · "+side, Vec{sign * .19, 0, .065}, []profileRing{{0, .105, .18}, {.10, .11, .19}, {.17, .085, .10}}, 8, leather)
		b.profile("shoulder_"+side, "Pauldron · "+side, Vec{sign * .34, 1.29, 0}, []profileRing{{0, .145, .16}, {.13, .185, .18}, {.21, .115, .13}}, 8, steel)
		b.profile("shoulder_trim_"+side, "Pauldron trim · "+side, Vec{sign * .34, 1.29, 0}, []profileRing{{0, .15, .166}, {.028, .157, .173}}, 8, gold)
		b.limb("upper_arm_"+side, "Upper arm · "+side, Vec{sign * .39, 1.32, 0}, Vec{sign * .55, 1.10, .035}, .095, .078, leather)
		b.limb("bracer_"+side, "Vambrace · "+side, Vec{sign * .55, 1.10, .035}, Vec{sign * .72, .94, .09}, .10, .075, steel)
		b.limb("hand_"+side, "Gauntlet · "+side, Vec{sign * .72, .94, .09}, Vec{sign * .77, .89, .12}, .08, .068, gold)
		b.box("tasset_"+side, "Skirt armor · "+side, Vec{sign * .14, .90, .16}, Vec{.19, .22, .045}, steel)
		for i := 0; i < 3; i++ {
			b.profile(fmt.Sprintf("rivet_%s_%d", side, i), "Breastplate rivet", Vec{sign * (.19 + float64(i)*.027), 1.18 + float64(i)*.08, .15}, []profileRing{{0, .012, .012}, {.016, .012, .012}}, 6, gold)
		}
	}
	// A closed pleated cloak with front/back surfaces, hanging behind the armor.
	cape := Mesh{}
	front := []string{}
	back := []string{}
	for _, p := range []Vec{{-.20, 1.47, -.15}, {.20, 1.47, -.15}, {.37, .43, -.27}, {.18, .38, -.32}, {0, .44, -.29}, {-.18, .38, -.32}, {-.37, .43, -.27}} {
		front = append(front, cape.vertex(p))
		back = append(back, cape.vertex(p.Add(Vec{0, 0, -.025})))
	}
	// Triangulated fan surfaces allow the hem to fold in depth.
	cf := cape.vertex(Vec{0, 1, -.22})
	cb := cape.vertex(Vec{0, 1, -.245})
	for i := range front {
		j := (i + 1) % len(front)
		cape.face([]string{cf, front[j], front[i]})
		cape.face([]string{cb, back[i], back[j]})
		cape.face([]string{front[i], front[j], back[j], back[i]})
	}
	b.add("cape", "Crimson cloak", cloth, cape)
	b.box("crest", "Helmet crest", Vec{0, 1.91, -.005}, Vec{.045, .10, .22}, cloth)
	b.box("chest_emblem", "Chest emblem", Vec{0, 1.32, .174}, Vec{.07, .17, .015}, gold)
	b.box("chest_emblem_bar", "Chest emblem bar", Vec{0, 1.34, .18}, Vec{.14, .045, .015}, gold)
	// Shield is a flattened cylinder rotated from Y to Z, with a raised boss.
	shield := loft([]profileRing{{0, .29, .34}, {.035, .30, .35}, {.065, .28, .33}}, 12, 0)
	for i := range shield.Vertices {
		p := shield.Vertices[i].Position
		shield.Vertices[i].Position = Vec{p[0] - .72, p[2] + 1.05, -p[1] + .255}
	}
	b.add("shield", "Shield · brass rim", gold, shield)
	disc := loft([]profileRing{{0, .257, .303}, {.02, .257, .303}}, 12, 0)
	for i := range disc.Vertices {
		p := disc.Vertices[i].Position
		disc.Vertices[i].Position = Vec{p[0] - .72, p[2] + 1.05, -p[1] + .275}
	}
	b.add("shield_face", "Shield · painted face", Vec{.025, .08, .14}, disc)
	b.limb("shield_boss", "Shield boss", Vec{-.72, 1.05, .27}, Vec{-.72, 1.05, .34}, .085, .028, brightSteel)
	b.box("shield_emblem", "Shield stripe", Vec{-.72, 1.05, .28}, Vec{.035, .47, .012}, gold)
	swordParts(b, "sword_", Vec{.79, .79, .11}, .74)
	return b.finish()
}

func terrainHeight(x, z float64) float64 {
	river := .65 * math.Sin(z*.55)
	bank := 1 - math.Exp(-math.Pow((x-river)/1.05, 4))
	hills := .18 + .75*math.Exp(-((x+3.5)*(x+3.5)+(z-2)*(z-2))/5) + .5*math.Exp(-((x-3)*(x-3)+(z+2)*(z+2))/4)
	return .23 + bank*(.28+hills) + .04*math.Sin(x*1.5)*math.Cos(z*1.3)*bank
}

// A watertight terrain tile with triangulated relief, vertical sides, and base.
func terrainMesh() Mesh {
	const nx, nz = 24, 20
	m := Mesh{}
	v := make([][]string, nx+1)
	for i := 0; i <= nx; i++ {
		v[i] = make([]string, nz+1)
		for j := 0; j <= nz; j++ {
			x, z := float64(i)*.5-6, float64(j)*.5-5
			v[i][j] = m.vertex(Vec{x, terrainHeight(x, z), z})
		}
	}
	for i := 0; i < nx; i++ {
		for j := 0; j < nz; j++ {
			m.face([]string{v[i][j], v[i][j+1], v[i+1][j+1]})
			m.face([]string{v[i][j], v[i+1][j+1], v[i+1][j]})
		}
	}
	rim := []string{}
	for j := 0; j <= nz; j++ {
		rim = append(rim, v[0][j])
	}
	for i := 1; i <= nx; i++ {
		rim = append(rim, v[i][nz])
	}
	for j := nz - 1; j >= 0; j-- {
		rim = append(rim, v[nx][j])
	}
	for i := nx - 1; i > 0; i-- {
		rim = append(rim, v[i][0])
	}
	positions := m.Positions()
	bottom := make([]string, len(rim))
	center := m.vertex(Vec{0, -.25, 0})
	for i, id := range rim {
		p := positions[id]
		p[1] = -.25
		bottom[i] = m.vertex(p)
	}
	for i := range rim {
		j := (i + 1) % len(rim)
		m.face([]string{rim[j], rim[i], bottom[i], bottom[j]})
		m.face([]string{center, bottom[j], bottom[i]})
	}
	return m
}
func ExampleLandscape() (Document, error) {
	b := newExample()
	b.add("terrain", "Meadow · editable terrain", Vec{.13, .29, .075}, terrainMesh())
	// Closed winding river strip, split into quads for local bank edits.
	water := Mesh{}
	const count = 24
	ids := make([][4]string, count+1)
	for i := 0; i <= count; i++ {
		z := -4.98 + 9.96*float64(i)/count
		x := .65 * math.Sin(z*.55)
		for j, p := range []Vec{{x - .58, .36, z}, {x + .58, .36, z}, {x + .58, .19, z}, {x - .58, .19, z}} {
			ids[i][j] = water.vertex(p)
		}
	}
	for i := 0; i < count; i++ {
		for j := 0; j < 4; j++ {
			k := (j + 1) % 4
			water.face([]string{ids[i][j], ids[i+1][j], ids[i+1][k], ids[i][k]})
		}
	}
	water.face(ids[0][:])
	water.face([]string{ids[count][3], ids[count][2], ids[count][1], ids[count][0]})
	b.add("river", "River", Vec{.035, .30, .40}, water)
	for i, p := range []Vec{{-4, 0, 3}, {-3.1, 0, 2.7}, {-4.7, 0, 1.7}, {3.8, 0, 3}, {4.6, 0, 1.6}, {3.1, 0, -3.2}, {-4.5, 0, -3}, {-2.8, 0, -3.4}, {4.8, 0, -1.5}} {
		p[1] = terrainHeight(p[0], p[2])
		id := fmt.Sprintf("pine_%02d_", i)
		height := 1.25 + .16*float64(i%4)
		b.profile(id+"trunk", "Pine trunk", p, []profileRing{{0, .09, .09}, {height * .7, .065, .065}}, 7, leather)
		for j := 0; j < 3; j++ {
			r := .52 - float64(j)*.10
			y := .3 + float64(j)*.34
			b.profile(fmt.Sprintf("%sfoliage_%d", id, j), "Pine canopy", p.Add(Vec{0, y, 0}), []profileRing{{0, r, r}, {height * .6, .022, .022}}, 7, Vec{.025 + float64(j)*.015, .13 + float64(i%3)*.025, .045})
		}
	}
	for i, p := range []Vec{{-4.8, 0, 3.8}, {-3.8, 0, 4}, {-2.8, 0, 3.8}, {3.5, 0, 3.9}, {4.7, 0, 3.6}, {-2, 0, -1.8}, {2.3, 0, 1.1}, {4.7, 0, -4}} {
		p[1] = terrainHeight(p[0], p[2]) - .08
		r := .4 + .13*float64(i%3)
		h := .6 + .35*float64(i%3)
		b.profile(fmt.Sprintf("rock_%02d", i), "Faceted granite", p, []profileRing{{0, r, r * .8}, {h * .38, r * 1.05, r * .85}, {h, .17, .13}}, 7, Vec{.27, .29, .26})
	}
	// A plank bridge spans the river near the center of the tile.
	for i := 0; i < 11; i++ {
		b.box(fmt.Sprintf("bridge_plank_%02d", i), "Bridge deck plank", Vec{-1.45 + float64(i)*.25, .76, -.5}, Vec{.235, .09, 1.15}, Vec{.30, .13, .045})
	}
	for _, x := range []float64{-1.4, -.15, 1.1} {
		for _, z := range []float64{-1.03, .03} {
			id := fmt.Sprintf("bridge_post_%g_%g", x, z)
			b.box(id, "Bridge post", Vec{x, .97, z}, Vec{.09, .68, .09}, leather)
		}
	}
	for i, z := range []float64{-1.03, .03} {
		b.box(fmt.Sprintf("bridge_rail_%d", i), "Bridge handrail", Vec{-.15, 1.23, z}, Vec{2.65, .065, .065}, Vec{.25, .10, .035})
	}
	for i := 0; i < 6; i++ {
		x := -1.9 - float64(i)*.42
		z := -.5 - .14*math.Sin(float64(i))
		b.profile(fmt.Sprintf("path_stone_%d", i), "Stepping stone", Vec{x, terrainHeight(x, z), z}, []profileRing{{0, .19, .23}, {.055, .18, .21}}, 7, Vec{.32, .31, .25})
	}
	// Ruined watchtower: individually editable masonry, doorway facing the river.
	center := Vec{3.25, terrainHeight(3.25, -.8), -.8}
	for row := 0; row < 4; row++ {
		for col := 0; col < 12; col++ {
			if col >= 2 && col <= 4 && row < 3 {
				continue
			}
			a := 2 * math.Pi * (float64(col) + .5*float64(row%2)) / 12
			p := center.Add(Vec{.64 * math.Cos(a), .14 + float64(row)*.28, .64 * math.Sin(a)})
			b.box(fmt.Sprintf("ruin_block_%d_%d", row, col), "Ruined tower masonry", p, Vec{.29, .25, .29}, Vec{.34 + float64(col%3)*.025, .32, .26})
		}
	}
	return b.finish()
}
