package engine

func ptr(v Vec) *Vec { return &v }

// ExampleCar is built by the same typed commands exposed to MCP and the editor.
func ExampleCar() (Document, error) {
	r, err := Evaluate(EditRequest{Document: Empty(), Commands: []Command{
		{Op: "primitive.add", NodeID: "body", Name: "Body", Shape: "box", Size: ptr(Vec{2.5, .55, 1.25}), Translation: ptr(Vec{0, .65, 0}), Color: ptr(Vec{.72, .045, .065})},
	}})
	if err != nil {
		return Document{}, err
	}
	top, err := Select(r.Document, Query{NodeID: "body", Kind: "face", Normal: ptr(Vec{0, 1, 0})})
	if err != nil {
		return Document{}, err
	}
	r, err = Evaluate(EditRequest{Document: r.Document, Selections: map[string]Selection{"top": top}, Commands: []Command{
		{Op: "inset", NodeID: "body", Selection: "top", Amount: .3, ResultSelection: "roof"},
		{Op: "extrude", NodeID: "body", Selection: "roof", Direction: ptr(Vec{0, 1, 0}), Distance: .65, ResultSelection: "roof"},
		{Op: "transform", NodeID: "body", Selection: "roof", Scale: ptr(Vec{.7, 1, .8})},
	}})
	if err != nil {
		return Document{}, err
	}
	commands := []Command{}
	for i, p := range []Vec{{-.8, .38, .69}, {.8, .38, .69}, {-.8, .38, -.69}, {.8, .38, -.69}} {
		id := []string{"wheel_front_left", "wheel_rear_left", "wheel_front_right", "wheel_rear_right"}[i]
		commands = append(commands, Command{Op: "primitive.add", NodeID: id, Shape: "cylinder", Size: ptr(Vec{.76, .22, .76}), Segments: 12, Color: ptr(Vec{.035, .045, .065})}, Command{Op: "transform", NodeID: id, Rotation: ptr(Vec{90, 0, 0}), Translation: ptr(p)})
	}
	commands = append(commands,
		Command{Op: "primitive.add", NodeID: "windshield", Shape: "box", Size: ptr(Vec{.045, .37, .67}), Translation: ptr(Vec{-.78, 1.23, 0}), Color: ptr(Vec{.19, .57, .7})},
		Command{Op: "transform", NodeID: "windshield", Rotation: ptr(Vec{0, 0, -22})},
		Command{Op: "primitive.add", NodeID: "window_left", Shape: "box", Size: ptr(Vec{1.06, .36, .025}), Translation: ptr(Vec{0, 1.25, .42}), Color: ptr(Vec{.19, .57, .7})},
		Command{Op: "transform", NodeID: "window_left", Rotation: ptr(Vec{-8, 0, 0})},
		Command{Op: "mesh.mirror", NodeID: "window_left", TargetID: "window_right", Axis: "z"},
		Command{Op: "primitive.add", NodeID: "headlight_left", Shape: "box", Size: ptr(Vec{.05, .15, .25}), Translation: ptr(Vec{-1.27, .7, .4}), Color: ptr(Vec{.95, .84, .4})},
		Command{Op: "primitive.add", NodeID: "headlight_right", Shape: "box", Size: ptr(Vec{.05, .15, .25}), Translation: ptr(Vec{-1.27, .7, -.4}), Color: ptr(Vec{.95, .84, .4})},
	)
	r, err = Evaluate(EditRequest{Document: r.Document, Commands: commands})
	return r.Document, err
}
